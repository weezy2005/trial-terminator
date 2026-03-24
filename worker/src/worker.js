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
  pool,
} from './db.js';
import { getAutomation } from './automations/index.js';
import { takeScreenshot } from './automations/base.js';
import { config } from './config.js';

// Each worker instance gets a unique ID at startup.
// This ID is stored in locked_by so we can trace which worker held a task.
// Using a UUID means IDs are unique even across multiple worker machines.
const WORKER_ID = `worker-${uuidv4()}`;

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
  await redis.connect();
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
    await updateTaskDeadLetter(taskId, error, null);
    return;
  }

  // --- Step 3: Run the automation ---
  let result;
  try {
    result = await automation(task);
  } catch (unexpectedErr) {
    // This catches errors the automation itself didn't handle — true crashes.
    // Treat as a failure.
    result = { success: false, error: unexpectedErr.message, page: null };
  }

  // --- Step 4: Handle the result ---
  if (result.success) {
    await updateTaskSuccess(taskId);
    console.log(`[worker] task ${taskId} completed successfully`);
    return;
  }

  // --- Failure path ---
  console.warn(`[worker] task ${taskId} failed: ${result.error}`);

  // Take a screenshot of whatever the browser was showing when it failed.
  // This is the "evidence" for the Dead Letter Queue.
  let screenshotPath = null;
  if (result.page) {
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
    await updateTaskDeadLetter(taskId, result.error, screenshotPath);
  } else {
    // Retries remain → re-queue.
    console.log(`[worker] task ${taskId} will retry (${task.attempts}/${task.max_attempts} attempts used)`);
    await updateTaskRetry(taskId, result.error);

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

// shutdown sets the flag that causes the main loop to exit after the current task.
function shutdown() {
  console.log('[worker] shutdown signal received, finishing current task...');
  isShuttingDown = true;
}
