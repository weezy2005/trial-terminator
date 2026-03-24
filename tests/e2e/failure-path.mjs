// failure-path.mjs — proves the Dead Letter Queue flow works end-to-end.
//
// Flow being tested:
//   POST /tasks (service=netflix, fake credentials) → PENDING
//   → Worker claims task → Playwright fails (bad selectors / fake creds)
//   → Screenshot saved to evidence/
//   → Task retried max_attempts times
//   → Task reaches DEAD_LETTER with evidence_path set
//
// Run with: node tests/e2e/failure-path.mjs
// Requires: docker compose up (all services running)
//
// NOTE: This test intentionally uses fake credentials so Netflix login fails.
// The purpose is to exercise the full failure + dead-letter path.
import { createTask, getTask, pollUntil, assert } from './utils.mjs';

async function run() {
  console.log('\n=== FAILURE PATH TEST ===\n');

  // --- Step 1: Create a task guaranteed to fail ---
  console.log('1. Creating task with fake credentials (will fail)...');
  const task = await createTask(
    'netflix',
    'fake@notreal.com',
    { password: 'wrongpassword123' }
  );

  assert(task.id, 'task has an ID');
  assert(task.status === 'PENDING', `task starts as PENDING (got: ${task.status})`);
  console.log(`   Task ID: ${task.id}`);
  console.log(`   Max attempts: ${task.max_attempts}`);

  // --- Step 2: Poll and watch the retry progression ---
  // We poll more frequently here so we can log each state transition.
  console.log('\n2. Watching task progress through retries...');

  let lastStatus = task.status;
  let lastAttempts = 0;

  // This timeout needs to be generous: each Playwright attempt waits up to 30s
  // for selectors before failing. With 3 max_attempts, that's up to ~90s.
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
    { intervalMs: 3000, timeoutMs: 180000 } // 3 minutes total
  );

  console.log(`\n   Final status: ${finalTask.status}`);

  // --- Step 3: Assert the dead letter outcome ---
  assert(finalTask.status === 'DEAD_LETTER', `task reached DEAD_LETTER (got: ${finalTask.status})`);
  assert(finalTask.attempts === finalTask.max_attempts, `all ${finalTask.max_attempts} attempts were used`);
  assert(finalTask.error_message, 'error message was recorded');
  assert(finalTask.evidence_path, `screenshot was saved at: ${finalTask.evidence_path}`);

  console.log(`\n   Error recorded: ${finalTask.error_message}`);
  console.log(`   Evidence path:  ${finalTask.evidence_path}`);
  console.log('\n✅ FAILURE PATH PASSED — Dead Letter Queue is working\n');
}

run().catch((err) => {
  console.error('\n❌ FAILURE PATH FAILED:', err.message);
  process.exit(1);
});
