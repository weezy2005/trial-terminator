// base.js provides shared utilities for all automation scripts.
// Every automation (Netflix, Spotify, etc.) builds on top of these helpers.
import { chromium } from 'playwright';
import path from 'path';
import fs from 'fs/promises';
import { config } from '../config.js';

// launchBrowser starts a Chromium browser instance.
//
// ARCHITECTURAL DECISION: Why Chromium over Firefox or WebKit?
// Most subscription services are built and tested for Chrome.
// Using Chromium gives the highest compatibility. If a specific service
// breaks in Chromium, you can override to Firefox per-automation.
//
// headless: true means no visible browser window — required in Docker.
// slowMo: 50 adds 50ms between each Playwright action in dev mode.
// This makes the automation visible and debuggable when you run it locally
// with HEADLESS=false in your .env.
export async function launchBrowser() {
  const headless = process.env.HEADLESS !== 'false';
  return chromium.launch({
    headless,
    slowMo: headless ? 0 : 50,
    // args: these flags are required when running Chrome inside Docker
    // (no GPU, no sandbox due to container permissions).
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  });
}

// takeScreenshot captures the current browser state and saves it to the evidence folder.
// Returns the relative path to the saved file.
//
// ARCHITECTURAL DECISION: Why save screenshots to the filesystem instead of S3/cloud?
// For Sprint 3, local filesystem is sufficient and keeps complexity low.
// In production, you'd upload to S3 and store the URL in evidence_path.
// The interface (a path string in the DB) stays the same — only the storage backend changes.
export async function takeScreenshot(page, taskId) {
  const evidenceDir = path.resolve(config.worker.evidencePath);

  // Create the evidence directory if it doesn't exist.
  // This runs every time but is idempotent — mkdir with recursive:true
  // is a no-op if the directory already exists.
  await fs.mkdir(evidenceDir, { recursive: true });

  const filename = `${taskId}-${Date.now()}.png`;
  const fullPath = path.join(evidenceDir, filename);
  const relativePath = path.join('evidence', filename);

  await page.screenshot({ path: fullPath, fullPage: true });
  console.log(`[screenshot] saved to ${relativePath}`);

  return relativePath;
}

// withBrowser is a higher-order function that wraps an automation in browser lifecycle management.
// It guarantees the browser is always closed — even if the automation throws.
//
// Usage:
//   const result = await withBrowser(async (browser) => {
//     const page = await browser.newPage();
//     // ... automation code ...
//     return { success: true };
//   });
//
// ARCHITECTURAL DECISION: Why a wrapper function instead of try/finally everywhere?
// Every automation needs the same setup/teardown. Without this wrapper, every
// automation script would have to remember to close the browser in a finally block.
// One forgotten finally = a zombie Chrome process eating memory in production.
// The wrapper makes the correct behavior the default.
export async function withBrowser(fn) {
  const browser = await launchBrowser();
  try {
    return await fn(browser);
  } finally {
    await browser.close();
  }
}
