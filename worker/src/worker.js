// worker.js is the core of Sprint 3.
// It runs a continuous loop: pop a task from Redis, run the Playwright
// automation, handle success/failure/dead-letter, repeat.
import { v4 as uuidv4 } from 'uuid';
import { redis, QUEUE_KEY } from './queue.js';
import {
  claimTask,
  updateTaskSuccess,
  updateTaskRetry,
  updateTaskDeadLetter,
  scheduleTask,
  heartbeat,
  pool,
} from './db.js';
import { getAutomation } from './automations/index.js';
import { takeScreenshot } from './automations/base.js';
import { config } from './config.js';

// Each worker instance gets a unique ID at startup.
// This ID is stored in locked_by so we can trace which worker held a task.
// Using a UUID means IDs are unique even across multiple worker machines.
const WORKER_ID = `worker-${uuidv4()}`;

// How often a running task refreshes its locked_at timestamp.
// The Go requeuer treats a task as abandoned after 30s without a heartbeat,
// so 10s gives us room for two missed beats (e.g. a slow DB round trip)
// before a live worker could be mistaken for a dead one.
const HEARTBEAT_INTERVAL_MS = 10_000;

// isShuttingDown is set to true when SIGTERM/SIGINT is received.
// The main loop checks this flag to exit cleanly after finishing the current task.
let isShuttingDown = false;

// start launches the worker and returns a promise that resolves when the worker shuts down.
export async function start() {
  console.log(`[worker] started with id: ${WORKER_ID}`);

  // Register shutdown handlers.
  // SIGTERM: sent by Docker/Kubernetes when stopping a container.
  // SIGINT:  sent by Ctrl+C during local development.
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);

  // Connect to Redis explicitly (we set lazyConnect: true).
  // Retry up to 10 times with a 2s delay — Docker networking can take a moment
  // to fully route traffic even after the health check passes.
  for (let attempt = 1; attempt <= 10; attempt++) {
    try {
      await redis.connect();
      break;
    } catch (err) {
      if (attempt === 10) throw err;
      console.log(`[worker] redis connect attempt ${attempt} failed, retrying in 2s...`);
      await new Promise((r) => setTimeout(r, 2000));
    }
  }
  console.log('[worker] redis connected');

  // Run the main loop until shutdown is requested.
  await loop();

  // Cleanup after loop exits.
  await redis.quit();
  await pool.end();
  console.log('[worker] shutdown complete');
}

// loop is the main BRPOP → process cycle.
async function loop() {
  console.log('[worker] listening for tasks...');

  while (!isShuttingDown) {
    // BRPOP blocks until a task ID appears in the queue or the timeout expires.
    //
    // ARCHITECTURAL DECISION: Why a timeout instead of blocking forever?
    // If we block forever (timeout=0), the process can never check isShuttingDown.
    // A 5-second timeout means: "wait up to 5 seconds for a task, then loop and
    // check if we should shut down." Shutdown delay is at most 5 seconds — acceptable.
    //
    // BRPOP returns: [queueKey, taskId] or null on timeout.
    const result = await redis.brpop(QUEUE_KEY, config.worker.brpopTimeout);

    if (!result) {
      // Timeout — no task arrived. Loop and check isShuttingDown.
      continue;
    }

    const [, taskId] = result;
    console.log(`[worker] received task: ${taskId}`);
    await processTask(taskId);
  }
}

// processTask handles the full lifecycle of a single task.
async function processTask(taskId) {
  // --- Step 1: Claim the task ---
  // This is the optimistic lock. If another worker already claimed it, we get null.
  const task = await claimTask(taskId, WORKER_ID);

  if (!task) {
    // Another worker won the race. This is normal — skip it.
    console.log(`[worker] task ${taskId} already claimed by another worker, skipping`);
    return;
  }

  console.log(`[worker] claimed task ${taskId} (attempt ${task.attempts}/${task.max_attempts})`);

  // --- Step 2: Find the automation for this service ---
  const automation = getAutomation(task.service_name);

  if (!automation) {
    // We received a task for a service we don't support.
    // Move it to DEAD_LETTER immediately — retrying won't help.
    const error = `No automation found for service: ${task.service_name}`;
    console.error(`[worker] ${error}`);
    await updateTaskDeadLetter(taskId, WORKER_ID, error, null);
    return;
  }

  // --- Step 3: Run the automation ---
  // Heartbeat while the automation runs. An AI agent run can take minutes
  // (up to 20 steps plus Gemini backoff), so a fixed timeout on locked_at
  // alone would requeue tasks that are still alive.
  const beat = setInterval(() => {
    heartbeat(taskId, WORKER_ID)
      .then((stillOwned) => {
        if (!stillOwned) {
          console.warn(`[worker] lost ownership of task ${taskId}, result will be discarded`);
        }
      })
      .catch((err) => console.warn(`[worker] heartbeat failed for ${taskId}: ${err.message}`));
  }, HEARTBEAT_INTERVAL_MS);

  let result;
  try {
    result = await automation(task);
  } catch (unexpectedErr) {
    // This catches errors the automation itself didn't handle — true crashes.
    // Treat as a failure.
    result = { success: false, error: unexpectedErr.message, page: null };
  } finally {
    clearInterval(beat);
  }

  // --- Step 4: Handle the result ---

  // Every write below is fenced on WORKER_ID. If it returns false, the
  // requeuer gave this task to another worker while we were running, and
  // that worker now owns the result. We log and drop ours.

  // Detection phase complete: the trial end date was found.
  // Store it and let the scheduler enqueue the task when the time comes.
  if (result.scheduled) {
    if (!(await scheduleTask(taskId, WORKER_ID, result.scheduledFor))) {
      return logLostOwnership(taskId);
    }
    console.log(`[worker] task ${taskId} auto-scheduled for ${result.scheduledFor.toISOString()}`);
    return;
  }

  if (result.success) {
    if (!(await updateTaskSuccess(taskId, WORKER_ID))) {
      return logLostOwnership(taskId);
    }
    console.log(`[worker] task ${taskId} completed successfully`);
    return;
  }

  // --- Failure path ---
  console.warn(`[worker] task ${taskId} failed: ${result.error}`);

  // Take a screenshot of whatever the browser was showing when it failed.
  // This is the "evidence" for the Dead Letter Queue.
  //
  // Automations that know the browser is already closed (e.g. universal.js,
  // which runs inside withBrowser's lifecycle) pre-save evidence and return
  // the path in result.evidencePath. We prefer that path when available.
  // Falling back to re-screenshotting only works for automations that keep
  // the page alive past the error — which is nobody right now, but we leave
  // the fallback in for future automations that manage their own browser.
  let screenshotPath = result.evidencePath ?? null;
  if (!screenshotPath && result.page) {
    try {
      screenshotPath = await takeScreenshot(result.page, taskId);
    } catch (screenshotErr) {
      // Don't let a screenshot failure block the failure-handling logic.
      console.error(`[worker] failed to take screenshot: ${screenshotErr.message}`);
    }
  }

  // --- Step 5: Retry or Dead Letter? ---
  if (task.attempts >= task.max_attempts) {
    // All attempts exhausted → Dead Letter Queue.
    //
    // ARCHITECTURAL DECISION: Why Dead Letter instead of just FAILED?
    // DEAD_LETTER is a signal to humans: "this task needs manual intervention."
    // It has an evidence_path (screenshot) attached so the engineer knows
    // exactly what the UI looked like when it failed. Without this, debugging
    // production failures is guesswork.
    console.error(`[worker] task ${taskId} exhausted all ${task.max_attempts} attempts → DEAD_LETTER`);
    if (!(await updateTaskDeadLetter(taskId, WORKER_ID, result.error, screenshotPath))) {
      return logLostOwnership(taskId);
    }
  } else {
    // Retries remain → re-queue.
    console.log(`[worker] task ${taskId} will retry (${task.attempts}/${task.max_attempts} attempts used)`);
    if (!(await updateTaskRetry(taskId, WORKER_ID, result.error))) {
      // Don't push to Redis — the new owner is handling this task.
      return logLostOwnership(taskId);
    }

    // Push the task back onto the Redis queue for immediate retry.
    // ARCHITECTURAL DECISION: Why push back immediately instead of waiting
    // for the Go requeue goroutine?
    // The requeue goroutine only rescues STALE IN_PROGRESS tasks (crashed workers).
    // A gracefully-failed task (RETRY status) would never be picked up by it.
    // The worker is responsible for re-queuing its own failures.
    await redis.lpush(QUEUE_KEY, taskId);
    console.log(`[worker] task ${taskId} re-queued for retry`);
  }
}

// logLostOwnership records a result that was discarded because another
// worker took over the task after this one missed its heartbeats.
function logLostOwnership(taskId) {
  console.warn(`[worker] task ${taskId} was reassigned to another worker, discarding result`);
}

// shutdown sets the flag that causes the main loop to exit after the current task.
function shutdown() {
  console.log('[worker] shutdown signal received, finishing current task...');
  isShuttingDown = true;
}
