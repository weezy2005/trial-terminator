// happy-path.mjs — proves the full success flow works end-to-end.
//
// Flow being tested:
//   POST /tasks (service=test) → PENDING in Postgres
//   → Redis queue receives task ID
//   → Worker claims task (IN_PROGRESS)
//   → test-service automation succeeds immediately
//   → Task reaches SUCCESS in Postgres
//
// Run with: node tests/e2e/happy-path.mjs
// Requires: docker compose up (all services running)
import { createTask, getTask, pollUntil, assert } from './utils.mjs';

async function run() {
  console.log('\n=== HAPPY PATH TEST ===\n');

  // --- Step 1: Create a task ---
  console.log('1. Creating task...');
  const task = await createTask('test', 'user@example.com', { reason: 'too_expensive' });

  assert(task.id, 'task has an ID');
  assert(task.status === 'PENDING', `task starts as PENDING (got: ${task.status})`);
  assert(task.idempotency_key, 'task has idempotency_key');
  console.log(`   Task ID: ${task.id}`);

  // --- Step 2: Test idempotency — send the same key again ---
  console.log('\n2. Testing idempotency (sending same key twice)...');
  const duplicate = await fetch(`${process.env.API_URL || 'http://localhost:8080'}/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      idempotency_key: task.idempotency_key,
      service_name: 'test',
      user_email: 'user@example.com',
    }),
  }).then((r) => r.json());

  assert(duplicate.id === task.id, 'duplicate request returns the same task ID');
  console.log('   Idempotency key correctly deduplicated the request');

  // --- Step 3: Poll until the worker processes the task ---
  console.log('\n3. Waiting for worker to process task...');
  const finalTask = await pollUntil(
    () => getTask(task.id),
    (t) => ['SUCCESS', 'DEAD_LETTER', 'FAILED'].includes(t.status),
    { timeoutMs: 30000 }
  );

  console.log(`   Final status: ${finalTask.status}`);

  // --- Step 4: Assert the happy path outcome ---
  assert(finalTask.status === 'SUCCESS', `task reached SUCCESS (got: ${finalTask.status})`);
  assert(!finalTask.evidence_path, 'no screenshot taken on success');
  assert(!finalTask.error_message, 'no error message on success');

  console.log('\n✅ HAPPY PATH PASSED\n');
}

run().catch((err) => {
  console.error('\n❌ HAPPY PATH FAILED:', err.message);
  process.exit(1);
});
