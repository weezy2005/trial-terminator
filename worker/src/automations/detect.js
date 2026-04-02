// detect.js — Uses Google Gemini's vision API to read the trial end date
// from a billing page screenshot.
//
// WHY AI INSTEAD OF CSS SELECTORS?
// Every subscription service shows billing dates differently — different HTML,
// different text, different layouts that change without warning. A brittle CSS
// selector breaks silently. Gemini reads the screenshot like a human and finds
// the date regardless of how the page is laid out.
//
// WHY GEMINI FLASH?
// It's free (via Google AI Studio), fast, and more than capable of extracting
// a date from a screenshot — this is a simple visual task, not deep reasoning.

import { GoogleGenerativeAI } from '@google/generative-ai';

const genAI = new GoogleGenerativeAI(process.env.GEMINI_API_KEY);
const model = genAI.getGenerativeModel({ model: 'gemini-1.5-flash' });

const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

// detectTrialEndDate takes a screenshot of the current Playwright page and asks
// Gemini to extract the trial end date (or next billing date).
//
// Returns a Date object for the detected end date, or null if not found.
export async function detectTrialEndDate(page) {
  console.log('[detect] taking screenshot of billing page');
  const screenshotBuffer = await page.screenshot({ fullPage: true });
  const base64Image = screenshotBuffer.toString('base64');

  console.log('[detect] sending screenshot to Gemini for date extraction');

  const result = await model.generateContent([
    {
      inlineData: {
        mimeType: 'image/png',
        data: base64Image,
      },
    },
    // The prompt is strict about output format so we can parse it reliably.
    // "not found" is the explicit sentinel for "I saw the page but no date".
    `This is a screenshot of a subscription billing or account settings page.
Find the trial end date or next billing date.
Reply with ONLY a date in YYYY-MM-DD format (example: 2026-04-10).
If you cannot find a clear trial end date or billing date, reply with exactly: not found`,
  ]);

  const raw = result.response.text().trim();
  console.log(`[detect] Gemini responded: "${raw}"`);

  if (raw === 'not found' || !DATE_PATTERN.test(raw)) {
    console.warn('[detect] could not extract a valid date from the billing page');
    return null;
  }

  const endDate = new Date(`${raw}T00:00:00Z`);
  console.log(`[detect] trial end date detected: ${endDate.toISOString()}`);
  return endDate;
}
