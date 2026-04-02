// spotify.js — Playwright automation for cancelling a Spotify Premium subscription.
import { withBrowser, takeScreenshot } from './base.js';
import { detectTrialEndDate } from './detect.js';

export async function cancel(task) {
  return withBrowser(async (browser) => {
    const page = await browser.newPage();
    await page.setViewportSize({ width: 1280, height: 800 });
    page.setDefaultTimeout(30_000);

    try {
      // --- Step 1: Navigate to Spotify login ---
      console.log(`[spotify] navigating to login for ${task.user_email}`);
      await page.goto('https://accounts.spotify.com/login');

      // --- Step 2: Fill credentials ---
      await page.locator('[data-testid="login-username"]').fill(task.user_email);
      await page.locator('[data-testid="login-password"]').fill(task.payload?.password ?? '');
      await page.locator('[data-testid="login-button"]').click();

      // --- Step 3: Wait for redirect to home ---
      await page.waitForURL('**/open.spotify.com/**', { timeout: 15_000 });
      console.log(`[spotify] login successful`);

      // --- Step 4: Navigate to account subscription page ---
      await page.goto('https://www.spotify.com/account/subscription/');
      await page.waitForLoadState('networkidle');

      // --- Step 4b: Auto-detect trial end date (AI phase) ---
      // If the user requested auto-scheduling and we haven't detected a date yet
      // (scheduled_for is null), take a screenshot and ask Claude to read the date.
      // We return { scheduled } so the worker stores the date and exits without cancelling.
      // The scheduler will re-enqueue this task on the right day for the actual cancel.
      if (task.payload?.auto_schedule && !task.scheduled_for) {
        const endDate = await detectTrialEndDate(page);
        if (!endDate) {
          return { success: false, error: 'Could not detect trial end date from Spotify billing page', page };
        }
        // Schedule for 1 day before the end date so the user gets the full trial.
        const cancelDate = new Date(endDate);
        cancelDate.setUTCDate(cancelDate.getUTCDate() - 1);
        return { scheduled: true, scheduledFor: cancelDate };
      }

      // --- Step 5: Click "Cancel Premium" ---
      await page.locator('[data-testid="cancel-premium-button"]').click();

      // --- Step 6: Work through the cancellation survey/confirm flow ---
      // Spotify shows a retention flow — skip through it.
      await page.locator('[data-testid="cancel-survey-continue"]').click();
      await page.locator('[data-testid="confirm-cancel-button"]').click();

      // --- Step 7: Verify cancellation confirmed ---
      await page.locator('[data-testid="cancellation-confirmed"]').waitFor();
      console.log(`[spotify] cancellation confirmed for ${task.user_email}`);

      return { success: true };

    } catch (err) {
      console.error(`[spotify] automation failed: ${err.message}`);
      return { success: false, error: err.message, page };
    }
  });
}
