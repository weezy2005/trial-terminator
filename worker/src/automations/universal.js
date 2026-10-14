// universal.js — AI-driven agentic automation that can cancel a subscription
// on ANY website, not just hardcoded services.
//
// HOW IT WORKS:
// Instead of a fixed Playwright script (click this selector, then that one),
// this automation runs an agentic loop:
//   1. Take a screenshot of the current page
//   2. Scan the DOM for CAPTCHA / 2FA walls — if present, bail out early
//   3. Ask Gemini: "what should I do next to cancel this subscription?"
//   4. Execute the action Gemini returns (click, type, navigate, scroll...)
//   5. Repeat until Gemini says "done" or we hit MAX_STEPS
//
// WHY COORDINATES INSTEAD OF CSS SELECTORS?
// Gemini sees a screenshot image, not the raw HTML. It can point to things
// visually ("the red Cancel button near the bottom") but it doesn't know the
// underlying DOM. Clicking by pixel coordinates (x, y) is more reliable than
// asking an AI to guess a CSS selector it cannot see.
//
// WHAT ABOUT CAPTCHAS AND 2FA?
// We can't beat them, so we detect them and fail the task with a clear
// error message instead of looping forever. The screenshot captured at
// that moment is attached to the DEAD_LETTER record so a human can
// finish the cancellation manually.

import path from 'path';
import fs from 'fs/promises';
import { withBrowser } from './base.js';
import { config } from '../config.js';
import { GoogleGenerativeAI } from '@google/generative-ai';

// Lazy-init: we only create the GenAI client the first time cancel() runs.
// This means the worker boots cleanly even when GEMINI_API_KEY is empty,
// as long as nobody actually submits a 'universal' task.
let model = null;
function getModel() {
  if (model) return model;
  const apiKey = process.env.GEMINI_API_KEY;
  if (!apiKey) {
    throw new Error('GEMINI_API_KEY is not set — universal AI agent cannot run');
  }
  const genAI = new GoogleGenerativeAI(apiKey);
  // gemini-3.5-flash — older pinned flash release. We previously tried the
  // newer 3.8-flash and flash-latest aliases but both are chronically 503-ing
  // because every new project points there. Older-but-still-supported versions
  // sit on quieter capacity and are plenty smart for form-filling. If 3.5 ever
  // gets deprecated, Google's 404 response will suggest the next version to use.
  model = genAI.getGenerativeModel({ model: 'gemini-3.5-flash' });
  return model;
}

// Maximum number of AI steps before giving up.
// A typical cancellation flow is 5-10 steps (login → account → cancel → confirm).
// 20 gives plenty of headroom for retention flows and multi-step modals.
const MAX_STEPS = 20;

// If Gemini returns something that isn't valid JSON, how many times
// should we re-ask before giving up on the step entirely.
const MAX_PARSE_RETRIES = 2;

// Common signals that we've hit a human-verification wall.
// If any of these appear in the DOM, we bail with a clear error.
const HUMAN_WALL_SELECTORS = [
  'iframe[src*="recaptcha"]',
  'iframe[src*="hcaptcha"]',
  'iframe[src*="arkoselabs"]',
  'iframe[title*="captcha" i]',
  'div.g-recaptcha',
  '[data-sitekey]',
];

const HUMAN_WALL_TEXT = [
  "verify you're human",
  'verify you are human',
  "i'm not a robot",
  'enter the code sent to',
  'two-factor authentication',
  'two factor authentication',
  'enter the 6-digit code',
  'security code sent to',
];

// detectHumanWall returns a short string describing the wall if one is present,
// or null if the page looks clean. We check both DOM structure (CAPTCHA iframes)
// and visible page text (2FA prompts) because different providers surface
// each one differently.
async function detectHumanWall(page) {
  for (const selector of HUMAN_WALL_SELECTORS) {
    try {
      const count = await page.locator(selector).count();
      if (count > 0) return `CAPTCHA detected (${selector})`;
    } catch {
      // Locator query itself failed — ignore and keep scanning.
    }
  }

  const bodyText = await page.evaluate(() => document.body?.innerText?.toLowerCase() ?? '').catch(() => '');
  for (const phrase of HUMAN_WALL_TEXT) {
    if (bodyText.includes(phrase)) return `human-verification prompt detected ("${phrase}")`;
  }
  return null;
}

// saveLatestScreenshot writes a predictable filename for the UI's Watch Mode.
// The frontend can poll GET /evidence/{taskId}-latest.png to see the agent's
// current view. We also return the relative path so the worker can store it
// if the task eventually fails.
async function saveLatestScreenshot(page, taskId, buffer) {
  const evidenceDir = path.resolve(config.worker.evidencePath);
  await fs.mkdir(evidenceDir, { recursive: true });
  const filename = `${taskId}-latest.png`;
  const fullPath = path.join(evidenceDir, filename);
  await fs.writeFile(fullPath, buffer);
  return path.join('evidence', filename);
}

// saveEvidence writes a timestamped copy of the last screenshot buffer and
// returns the relative path. The worker stores this in the task's evidence_path
// column so the DEAD_LETTER UI card can show "here's what the browser was
// looking at when it failed."
//
// Why not re-screenshot in the worker?
// The browser is already closed by the time the worker handles the failure
// (withBrowser's finally runs on return). The in-memory buffer is the only
// still-valid source of the final frame.
async function saveEvidence(taskId, buffer) {
  if (!buffer) return null;
  const evidenceDir = path.resolve(config.worker.evidencePath);
  await fs.mkdir(evidenceDir, { recursive: true });
  const filename = `${taskId}-${Date.now()}.png`;
  const fullPath = path.join(evidenceDir, filename);
  await fs.writeFile(fullPath, buffer);
  return path.join('evidence', filename);
}

// generateWithBackoff calls Gemini with automatic 429 handling.
// Google's free tier is 15 req/min — a single stuck task can burn through this
// in seconds. Rather than fail the whole task, we parse the suggested retry
// delay out of the error and sleep that long before trying again.
async function generateWithBackoff(geminiModel, prompt, screenshotB64) {
  // 5 retries (not 3) because Google 503s on popular flash models sometimes
  // last several minutes. Each retry waits 15-45s, so the worst case is ~3.5
  // minutes per step — still faster than failing a task mid-flow and losing
  // all progress.
  const MAX_RATE_LIMIT_RETRIES = 5;
  for (let i = 0; i <= MAX_RATE_LIMIT_RETRIES; i++) {
    try {
      return await geminiModel.generateContent([
        { inlineData: { mimeType: 'image/png', data: screenshotB64 } },
        prompt,
      ]);
    } catch (err) {
      const msg = err?.message ?? '';
      // Both 429 (rate limit) and 503 (overloaded) are transient — retry both.
      // We get 429 when WE'VE sent too many requests; 503 when GOOGLE'S model
      // is slammed by all users. Same recovery strategy applies: wait and retry.
      const isTransient =
        msg.includes('[429') ||
        msg.includes('[503') ||
        msg.toLowerCase().includes('quota') ||
        msg.toLowerCase().includes('rate limit') ||
        msg.toLowerCase().includes('high demand') ||
        msg.toLowerCase().includes('overloaded');
      if (!isTransient || i === MAX_RATE_LIMIT_RETRIES) throw err;

      // Google tells us how long to wait: "Please retry in 23.6s" or "retryDelay":"24s".
      // 503s don't include that hint, so we fall back to exponential-ish (15s, 30s, 45s).
      const match = msg.match(/retry in (\d+(?:\.\d+)?)/i) || msg.match(/"retryDelay":"(\d+)s"/);
      const parsed = match ? Math.ceil(parseFloat(match[1])) : null;
      const waitSec = Math.min(parsed ?? (15 * (i + 1)), 45);
      const reason = msg.includes('[503') || msg.toLowerCase().includes('high demand') ? 'overloaded' : 'rate-limited';
      console.log(`[universal] Gemini ${reason} (attempt ${i + 1}/${MAX_RATE_LIMIT_RETRIES + 1}) — waiting ${waitSec}s...`);
      await new Promise((r) => setTimeout(r, waitSec * 1000));
    }
  }
  throw new Error('rate limit retries exhausted');
}

// askGeminiForAction sends the screenshot + prompt and returns a parsed
// action object. On parse failure it re-asks with a strict reminder,
// up to MAX_PARSE_RETRIES times. Rate limits are handled transparently
// one layer below via generateWithBackoff.
async function askGeminiForAction(screenshotB64, prompt) {
  const geminiModel = getModel();

  for (let attempt = 1; attempt <= MAX_PARSE_RETRIES + 1; attempt++) {
    const promptForAttempt = attempt === 1
      ? prompt
      : `${prompt}\n\nIMPORTANT: your previous reply was not valid JSON. Reply with ONLY the JSON object, no markdown fences, no commentary.`;

    const result = await generateWithBackoff(geminiModel, promptForAttempt, screenshotB64);

    const raw = result.response.text().trim()
      .replace(/^```json?\n?/, '')
      .replace(/\n?```$/, '')
      .trim();

    try {
      return { ok: true, action: JSON.parse(raw), raw };
    } catch {
      if (attempt > MAX_PARSE_RETRIES) {
        return { ok: false, raw };
      }
      // Fall through — loop re-asks with the stricter prompt.
    }
  }
  return { ok: false, raw: '' };
}

export async function cancel(task) {
  const startUrl = task.payload?.url;

  if (!startUrl) {
    return { success: false, error: 'No URL provided. Add the website URL to the task payload.' };
  }

  // Fail fast if the agent is unusable — don't open a browser we'll immediately close.
  if (!process.env.GEMINI_API_KEY) {
    return {
      success: false,
      error: 'GEMINI_API_KEY is not set. Add it to .env to enable the universal AI agent.',
    };
  }

  return withBrowser(async (browser) => {
    const page = await browser.newPage();
    await page.setViewportSize({ width: 1280, height: 800 });
    page.setDefaultTimeout(15_000);

    // Track the most recent screenshot buffer so failure branches can write
    // it as DEAD_LETTER evidence. The browser is already closing by the time
    // the worker handles the result, so we can't re-screenshot there.
    let lastBuffer = null;

    try {
      console.log(`[universal] navigating to ${startUrl}`);
      await page.goto(startUrl);
      await page.waitForLoadState('networkidle').catch(() => {});

      for (let step = 1; step <= MAX_STEPS; step++) {
        console.log(`[universal] step ${step}/${MAX_STEPS} — url: ${page.url()}`);

        // --- Human-wall check (CAPTCHA / 2FA) ---
        // Done BEFORE calling Gemini so we don't waste an API call on a page
        // the agent fundamentally can't get past. Take a screenshot first so
        // the DEAD_LETTER record shows what the human wall looks like.
        const wall = await detectHumanWall(page);
        if (wall) {
          console.warn(`[universal] ${wall} — aborting`);
          const wallBuffer = await page.screenshot({ fullPage: false }).catch(() => null);
          const evidencePath = await saveEvidence(task.id, wallBuffer).catch(() => null);
          return {
            success: false,
            error: `${wall}. Manual intervention required — see screenshot.`,
            evidencePath,
          };
        }

        // --- Screenshot + save for Watch Mode ---
        const screenshotBuffer = await page.screenshot({ fullPage: false });
        lastBuffer = screenshotBuffer;
        await saveLatestScreenshot(page, task.id, screenshotBuffer).catch((e) => {
          // Non-fatal — the agent can still run without a watch-mode screenshot.
          console.warn(`[universal] could not save watch-mode screenshot: ${e.message}`);
        });
        const base64 = screenshotBuffer.toString('base64');

        // --- Ask Gemini what to do next ---
        const prompt = `You are an AI agent whose goal is to cancel the user's subscription on this website.

User credentials:
- Email: ${task.user_email}
- Password: ${task.payload?.password ?? '(not provided)'}

Current URL: ${page.url()}
Step: ${step} of ${MAX_STEPS}

Follow this priority order:
1. If a login form is visible → fill in the credentials and submit.
2. If logged in but not on account/billing settings → navigate there.
3. If on account/billing → find and click the cancel / unsubscribe / end-subscription action.
4. If a retention modal appears ("Are you sure?", "Keep subscription", "Downgrade instead") → proceed through it to confirm cancellation.
5. Once you see a confirmation that the subscription has been cancelled → return success.

Rules:
- Coordinates are from the top-left of the viewport (0..1280 wide, 0..800 tall).
- If you see a cookie banner blocking the page, dismiss it before anything else.
- Never guess credentials. Only use the ones provided above.
- If you cannot find any cancellation path after exploring, give up with success:false.

For TEXT INPUTS, use the "fill" action with the visible label text.
For BUTTONS/LINKS, use the "click_text" action with the button's visible text.
Both use Playwright's role/label locators which are 100% reliable — no
pixel coordinates to guess. Prefer these over coordinate-based actions.

IMPORTANT: Before choosing a fill action, LOOK at the current screenshot. If
the field already contains the right value, DO NOT fill it again. Instead,
move on to the next step (click a submit button, fill a different field, etc).
Repeatedly filling an already-filled field wastes steps and never progresses.

type_at and click are fallbacks for elements WITHOUT clear text/labels.

If you've attempted the same kind of action 3+ steps in a row without the page
visibly changing, you are stuck. Try: scrolling, pressing Tab, or navigating
to a known URL for the next step (e.g. /account/billing).

Reply with ONLY valid JSON (no markdown, no explanation). Double-check
every quote and comma — a single missing quote invalidates the whole reply.

Fill a labeled input (BEST):  {"action":"fill","label":"Email","text":"hello@example.com","reason":"..."}
Click a button by text (BEST): {"action":"click_text","text":"Sign in","reason":"..."}
Click by coordinates:         {"action":"click","x":123,"y":456,"reason":"..."}
Type into field by coords:    {"action":"type_at","x":123,"y":456,"text":"...","reason":"..."}
Type into focused field:      {"action":"type","text":"...","reason":"..."}
Press a key:                  {"action":"key","key":"Enter","reason":"..."}
Go to a URL:                  {"action":"navigate","url":"https://...","reason":"..."}
Scroll the page:              {"action":"scroll","y":400,"reason":"..."}
Wait a moment:                {"action":"wait","ms":2000,"reason":"..."}
Done — success:               {"action":"done","success":true,"message":"Subscription cancelled"}
Done — give up:               {"action":"done","success":false,"message":"reason"}`;

        const { ok, action, raw } = await askGeminiForAction(base64, prompt);

        if (!ok) {
          console.warn(`[universal] could not parse AI response after retries — raw: ${raw}`);
          // Treat as a hard failure for this task so we don't loop forever.
          const evidencePath = await saveEvidence(task.id, lastBuffer).catch(() => null);
          return {
            success: false,
            error: 'AI agent returned non-JSON responses repeatedly',
            evidencePath,
          };
        }

        console.log(`[universal] AI → ${action.action}${action.reason ? ' — ' + action.reason : ''}`);

        // --- Execute the action ---
        if (action.action === 'done') {
          if (action.success) {
            console.log(`[universal] cancellation confirmed: ${action.message}`);
            return { success: true };
          }
          console.warn(`[universal] agent gave up: ${action.message}`);
          const evidencePath = await saveEvidence(task.id, lastBuffer).catch(() => null);
          return { success: false, error: action.message || 'Agent gave up', evidencePath };
        }

        if (action.action === 'click') {
          await page.mouse.click(action.x, action.y);
        } else if (action.action === 'click_text') {
          // Text-based button/link lookup — same reliability idea as fill, but
          // for clickable elements. Tries role=button first (works for <button>
          // and ARIA-labelled divs), then role=link (for <a>), then raw text.
          const text = action.text;
          let clicked = false;
          for (const locator of [
            page.getByRole('button', { name: text }),
            page.getByRole('link', { name: text }),
            page.getByText(text, { exact: false }),
          ]) {
            try {
              await locator.first().click({ timeout: 3000 });
              clicked = true;
              break;
            } catch { /* try next locator */ }
          }
          if (!clicked) {
            console.warn(`[universal] click_text: could not locate "${text}" — AI should fall back to click with coords`);
          }
        } else if (action.action === 'fill') {
          // Label-based input fill — the gold standard.
          // Playwright's accessibility-tree lookup finds the <input> associated
          // with the given label text (via `for` attribute or wrapping), bypassing
          // all the AI's pixel-coordinate guesswork.
          //
          // Fallback order: label → placeholder → role=textbox with name. If
          // every lookup misses, we throw — the agent will see the exception
          // in the next step via the error-recovery path and should try a
          // different approach (type_at + coordinates).
          const label = action.label;
          const text = action.text;
          let filled = false;
          for (const locator of [
            page.getByLabel(label, { exact: false }),
            page.getByPlaceholder(label, { exact: false }),
            page.getByRole('textbox', { name: label }),
          ]) {
            try {
              await locator.first().fill(text, { timeout: 3000 });
              filled = true;
              break;
            } catch { /* try next locator */ }
          }
          if (!filled) {
            console.warn(`[universal] fill: could not locate field "${label}" — AI should fall back to type_at`);
          }
        } else if (action.action === 'type_at') {
          // Atomic click + type — avoids losing focus between separate actions.
          // Triple-click selects any existing text in the field, so typing overwrites
          // instead of appending (handy if the field had a leftover placeholder/value).
          await page.mouse.click(action.x, action.y, { clickCount: 3 });
          await page.keyboard.type(action.text, { delay: 40 });
        } else if (action.action === 'type') {
          await page.keyboard.type(action.text, { delay: 40 });
        } else if (action.action === 'key') {
          await page.keyboard.press(action.key);
        } else if (action.action === 'navigate') {
          await page.goto(action.url);
        } else if (action.action === 'scroll') {
          await page.evaluate((dy) => window.scrollBy(0, dy), action.y ?? 400);
        } else if (action.action === 'wait') {
          await new Promise((r) => setTimeout(r, Math.min(action.ms ?? 1000, 10_000)));
        } else {
          console.warn(`[universal] unknown action "${action.action}" — skipping`);
        }

        // Let the page settle before the next screenshot.
        await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});
        await new Promise((r) => setTimeout(r, 800));
      }

      // Ran out of steps.
      const evidencePath = await saveEvidence(task.id, lastBuffer).catch(() => null);
      return {
        success: false,
        error: `Agent could not complete cancellation in ${MAX_STEPS} steps`,
        evidencePath,
      };

    } catch (err) {
      console.error(`[universal] automation failed: ${err.message}`);
      // Try a fresh screenshot first (the error may not have closed the page);
      // fall back to the last known buffer if that fails.
      let errBuffer = lastBuffer;
      try {
        errBuffer = await page.screenshot({ fullPage: false });
      } catch { /* page may already be dead */ }
      const evidencePath = await saveEvidence(task.id, errBuffer).catch(() => null);
      return { success: false, error: err.message, evidencePath };
    }
  });
}
