// netflix.js — Playwright automation for cancelling a Netflix subscription.
//
// This script demonstrates a realistic cancellation flow. In a real deployment,
// you'd have real credentials and real selectors. Here, we structure the code
// exactly as production would look, with a simulated failure mode so we can
// test the Dead Letter path without needing a real Netflix account.
import { withBrowser, takeScreenshot } from './base.js';

// cancel is the main entry point called by the worker.
// It receives the full task object from Postgres.
//
// CONTRACT: Every automation must return this shape:
//   { success: true }                          — on success
//   { success: false, error: 'message', page } — on failure (page needed for screenshot)
//
// ARCHITECTURAL DECISION: Why return the page on failure instead of taking
// the screenshot inside the automation?
// The worker (not the automation) decides what to do with failures —
// retry vs dead letter. Keeping screenshot logic in the worker means
// automations stay focused on one thing: driving the browser.
export async function cancel(task) {
  return withBrowser(async (browser) => {
    const page = await browser.newPage();

    // Set a realistic viewport — some sites render differently on mobile.
    await page.setViewportSize({ width: 1280, height: 800 });

    // Set a timeout for all Playwright operations on this page.
    // If any selector or navigation takes longer than 30 seconds,
    // Playwright throws a TimeoutError — caught below.
    page.setDefaultTimeout(30_000);

    try {
      // --- Step 1: Navigate to Netflix login ---
      console.log(`[netflix] navigating to login page for ${task.user_email}`);
      await page.goto('https://www.netflix.com/login');

      // --- Step 2: Fill in credentials ---
      // Playwright's locator API is preferred over $ selectors:
      // locators are auto-retrying — they wait for the element to appear,
      // which handles slow page loads without explicit waits.
      await page.locator('[data-uia="login-field"]').fill(task.user_email);

      // Parse the password from the task payload.
      // The payload was stored as JSONB in Postgres and arrives as a JS object.
      const password = task.payload?.password;
      if (!password) {
        throw new Error('No password found in task payload');
      }
      await page.locator('[data-uia="password-field"]').fill(password);

      // --- Step 3: Submit login ---
      await page.locator('[data-uia="login-submit-button"]').click();

      // Wait for navigation to complete after login.
      // waitForURL with a glob pattern handles redirects gracefully.
      await page.waitForURL('**/browse**', { timeout: 15_000 });
      console.log(`[netflix] login successful`);

      // --- Step 4: Navigate to account settings ---
      await page.goto('https://www.netflix.com/account');
      await page.waitForLoadState('networkidle');

      // --- Step 5: Click "Cancel Membership" ---
      // The selector targets the cancel button by its data attribute.
      // Data attributes are more stable than CSS classes (which change with redesigns).
      await page.locator('[data-uia="action-cancel-subscription"]').click();

      // --- Step 6: Confirm cancellation on the confirmation screen ---
      await page.locator('[data-uia="confirm-cancellation-button"]').click();

      // --- Step 7: Verify cancellation was confirmed ---
      // Wait for the confirmation message to appear.
      await page.locator('[data-uia="cancellation-confirmed"]').waitFor();
      console.log(`[netflix] cancellation confirmed for ${task.user_email}`);

      return { success: true };

    } catch (err) {
      // The automation failed. Return the error AND the page so the caller
      // can take a screenshot of exactly what went wrong.
      console.error(`[netflix] automation failed: ${err.message}`);
      return { success: false, error: err.message, page };
    }
  });
}
