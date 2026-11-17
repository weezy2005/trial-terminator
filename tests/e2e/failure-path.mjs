// failure-path.mjs — proves the Dead Letter Queue flow works end-to-end.
//
// Flow being tested:
//   POST /tasks (service=universal, url=mock site's CAPTCHA page) → PENDING
//   → Worker claims task → agent detects the human-verification wall
//   → Screenshot saved to evidence/
//   → Task retried max_attempts times
//   → Task reaches DEAD_LETTER with evidence_path set
//
// Run with: node tests/e2e/failure-path.mjs
// Requires: docker compose up (all services running) with GEMINI_API_KEY set.
// The agent bails on the wall before calling Gemini, but it refuses to start
// a browser at all without a key.
//
// WHY THE MOCK SITE AND NOT A REAL ONE?
// A failure test needs a target that fails the same way every run. Real sites
// change their markup and bot defences; /challenge on the mock site always
// shows a CAPTCHA wall, which is a failure mode the agent hits in production.
import { createTask, getTask, pollUntil, assert } from './utils.mjs';

// The worker reaches the mock site over the Docker network, not localhost.
const CHALLENGE_URL = process.env.MOCK_CHALLENGE_URL || 'http://mock-subscription:3002/challenge';

async function run() {
  console.log('\n=== FAILURE PATH TEST ===\n');

  // --- Step 1: Create a task guaranteed to fail ---
  console.log('1. Creating task pointed at a CAPTCHA wall (will fail)...');
  const task = await createTask('universal', 'fake@notreal.com', { url: CHALLENGE_URL });

  assert(task.id, 'task has an ID');
  assert(task.status === 'PENDING', `task starts as PENDING (got: ${task.status})`);
  console.log(`   Task ID: ${task.id}`);
  console.log(`   Max attempts: ${task.max_attempts}`);

  // --- Step 2: Poll and watch the retry progression ---
  // We poll more frequently here so we can log each state transition.
  console.log('\n2. Watching task progress through retries...');

  let lastStatus = task.status;
  let lastAttempts = 0;

  // Each attempt launches a browser and fails on the first page, so a full
  // run is usually well under a minute. The timeout leaves room for a cold
  // Chromium start on a slow machine.
  const finalTask = await pollUntil(
    async () => {
      const t = await getTask(task.id);
      if (t.status !== lastStatus || t.attempts !== lastAttempts) {
        console.log(`   → status: ${t.status}, attempts: ${t.attempts}/${t.max_attempts}`);
        lastStatus = t.status;
        lastAttempts = t.attempts;
      }
      return t;
    },
    (t) => t.status === 'DEAD_LETTER',
    { intervalMs: 2000, timeoutMs: 180000 } // 3 minutes total
  );

  console.log(`\n   Final status: ${finalTask.status}`);

  // A missing key fails every attempt too, but for the wrong reason and with
  // no screenshot. Say so plainly instead of failing on the evidence check.
  if (finalTask.error_message?.includes('GEMINI_API_KEY')) {
    throw new Error('GEMINI_API_KEY is not set for the worker; add it to .env and restart the stack');
  }

  // --- Step 3: Assert the dead letter outcome ---
  assert(finalTask.status === 'DEAD_LETTER', `task reached DEAD_LETTER (got: ${finalTask.status})`);
  assert(finalTask.attempts === finalTask.max_attempts, `all ${finalTask.max_attempts} attempts were used`);
  assert(/CAPTCHA|human-verification/.test(finalTask.error_message ?? ''), `error names the human wall: ${finalTask.error_message}`);
  assert(finalTask.evidence_path, `screenshot was saved at: ${finalTask.evidence_path}`);

  console.log(`\n   Evidence path:  ${finalTask.evidence_path}`);
  console.log('\n✅ FAILURE PATH PASSED — Dead Letter Queue is working\n');
}

run().catch((err) => {
  console.error('\n❌ FAILURE PATH FAILED:', err.message);
  process.exit(1);
});
