// test-service.js is a fake automation used exclusively for testing.
// It always succeeds immediately without opening a browser.
//
// ARCHITECTURAL DECISION: Why a fake automation instead of mocking?
// End-to-end tests should test the real system path as much as possible.
// Using a real (but trivial) automation exercises the full worker loop:
// BRPOP → claimTask → run automation → updateTaskSuccess.
// The only thing we skip is the browser — because we're testing the
// plumbing, not Playwright's ability to drive Chrome.
export async function cancel(task) {
  console.log(`[test-service] instantly succeeding task for ${task.user_email}`);
  // Simulate a small amount of work so the timing looks realistic in logs.
  await new Promise((resolve) => setTimeout(resolve, 500));
  return { success: true };
}
