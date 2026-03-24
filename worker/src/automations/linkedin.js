// linkedin.js — Playwright automation for cancelling a LinkedIn Premium subscription.
import { withBrowser, takeScreenshot } from './base.js';

export async function cancel(task) {
  return withBrowser(async (browser) => {
    const page = await browser.newPage();
    await page.setViewportSize({ width: 1280, height: 800 });
    page.setDefaultTimeout(30_000);

    try {
      // --- Step 1: Navigate to LinkedIn login ---
      console.log(`[linkedin] navigating to login for ${task.user_email}`);
      await page.goto('https://www.linkedin.com/login');

      // --- Step 2: Fill credentials ---
      await page.locator('#username').fill(task.user_email);
      await page.locator('#password').fill(task.payload?.password ?? '');
      await page.locator('[data-litms-control-urn="login-submit"]').click();

      // --- Step 3: Wait for redirect to feed ---
      await page.waitForURL('**/linkedin.com/feed/**', { timeout: 15_000 });
      console.log(`[linkedin] login successful`);

      // --- Step 4: Navigate to Premium subscription management page ---
      await page.goto('https://www.linkedin.com/premium/manage-subscription/');
      await page.waitForLoadState('networkidle');

      // --- Step 5: Click "Cancel subscription" ---
      await page.locator('[data-test-id="cancel-subscription-button"]').click();

      // --- Step 6: Work through the cancellation survey/confirm flow ---
      // LinkedIn shows a retention modal — skip it.
      await page.locator('[data-test-id="cancel-reason-continue"]').click();
      await page.locator('[data-test-id="confirm-cancellation-button"]').click();

      // --- Step 7: Verify cancellation confirmed ---
      await page.locator('[data-test-id="cancellation-confirmation"]').waitFor();
      console.log(`[linkedin] cancellation confirmed for ${task.user_email}`);

      return { success: true };

    } catch (err) {
      console.error(`[linkedin] automation failed: ${err.message}`);
      return { success: false, error: err.message, page };
    }
  });
}
