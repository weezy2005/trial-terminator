// Mock SaaS — a deliberately-realistic subscription service the AI agent can
// safely cancel 100% of the time. Five pages, one dark-pattern retention step.
//
// WHY SO SIMPLE?
// Nothing fancy here on purpose. The agent needs clear visual elements it can
// identify from a screenshot (big obvious labels, generous whitespace, a single
// clear call-to-action per page). If this looked like real Beehiiv, the AI
// would hit the same ambiguity problems it hits on real sites.
//
// WHY NO REAL AUTH?
// The agent never needs to actually be "signed in" to test the cancellation
// flow — we just need to accept any credentials, drop a session cookie, and
// let the pages advance. A real auth system would add noise without proving
// anything about the agent.

import express from 'express';
import cookieParser from 'cookie-parser';

const app = express();
app.use(express.urlencoded({ extended: false }));
app.use(cookieParser());

const PORT = Number(process.env.PORT) || 3002;

// Session middleware — any truthy "sid" cookie means "logged in."
// We don't track real user data; this is a stage prop.
function requireLogin(req, res, next) {
  if (!req.cookies.sid) {
    return res.redirect('/');
  }
  next();
}

// Shared layout — one function so every page shares the same brand/CSS.
// Keeping it inline avoids a template engine dependency for 5 pages.
function page(title, bodyHtml, options = {}) {
  const { accountEmail } = options;
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>${title} · MockBox</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  * { box-sizing: border-box; }
  body {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    background: #f8fafc;
    color: #0f172a;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  header {
    background: white;
    border-bottom: 1px solid #e2e8f0;
    padding: 16px 32px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  header .brand { font-weight: 700; font-size: 18px; color: #0ea5e9; }
  header nav { display: flex; gap: 24px; font-size: 14px; }
  header nav a { color: #475569; text-decoration: none; }
  header nav a:hover { color: #0f172a; }
  main {
    flex: 1;
    padding: 48px 24px;
    display: flex;
    justify-content: center;
  }
  .card {
    background: white;
    border: 1px solid #e2e8f0;
    border-radius: 12px;
    padding: 40px;
    width: 100%;
    max-width: 480px;
    box-shadow: 0 1px 2px rgba(0,0,0,0.04);
  }
  .card h1 {
    margin: 0 0 8px;
    font-size: 24px;
    font-weight: 700;
  }
  .card .lead { color: #64748b; margin: 0 0 24px; }
  label {
    display: block;
    font-size: 13px;
    font-weight: 600;
    color: #334155;
    margin: 16px 0 6px;
  }
  input[type=email], input[type=password], input[type=text] {
    width: 100%;
    padding: 12px 14px;
    border: 1px solid #cbd5e1;
    border-radius: 8px;
    font-size: 15px;
  }
  input:focus { outline: 2px solid #0ea5e9; outline-offset: 0; border-color: #0ea5e9; }
  button, .btn {
    display: inline-block;
    padding: 12px 20px;
    border-radius: 8px;
    border: none;
    font-size: 15px;
    font-weight: 600;
    cursor: pointer;
    text-decoration: none;
    text-align: center;
  }
  .btn-primary { background: #0ea5e9; color: white; width: 100%; margin-top: 24px; }
  .btn-primary:hover { background: #0284c7; }
  .btn-danger { background: #ef4444; color: white; }
  .btn-danger:hover { background: #dc2626; }
  .btn-secondary { background: #f1f5f9; color: #334155; }
  .btn-link { background: none; color: #64748b; text-decoration: underline; padding: 4px 8px; }
  .plan {
    background: #f8fafc;
    border: 1px solid #e2e8f0;
    border-radius: 8px;
    padding: 20px;
    margin: 16px 0;
  }
  .plan .plan-name { font-weight: 700; font-size: 18px; }
  .plan .plan-price { color: #0ea5e9; font-weight: 600; margin-top: 4px; }
  .plan .plan-renews { color: #64748b; font-size: 13px; margin-top: 8px; }
  .benefits { list-style: none; padding: 0; margin: 16px 0; }
  .benefits li { padding: 8px 0; color: #334155; }
  .benefits li::before { content: '✓'; color: #10b981; font-weight: 700; margin-right: 8px; }
  .cancel-actions {
    margin-top: 24px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: center;
  }
  .success-badge {
    width: 64px; height: 64px; border-radius: 50%;
    background: #d1fae5; color: #10b981;
    display: flex; align-items: center; justify-content: center;
    font-size: 32px; margin: 0 auto 24px;
  }
  .footer-note { text-align: center; margin-top: 24px; font-size: 13px; color: #94a3b8; }
</style>
</head>
<body>
<header>
  <div class="brand">MockBox</div>
  <nav>
    ${accountEmail ? `
      <a href="/dashboard">Dashboard</a>
      <a href="/billing">Billing</a>
      <a href="/logout">Sign out</a>
    ` : ''}
  </nav>
</header>
<main>
  ${bodyHtml}
</main>
</body>
</html>`;
}

// --- Routes ---

// Landing / login
app.get('/', (req, res) => {
  if (req.cookies.sid) return res.redirect('/dashboard');
  res.send(page('Sign in', `
    <div class="card">
      <h1>Sign in to MockBox</h1>
      <p class="lead">Welcome back! Enter your account details.</p>
      <form method="POST" action="/login">
        <label for="email">Email</label>
        <input id="email" name="email" type="email" placeholder="you@example.com" required>
        <label for="password">Password</label>
        <input id="password" name="password" type="password" placeholder="••••••••" required>
        <button type="submit" class="btn btn-primary">Sign in</button>
      </form>
    </div>
  `));
});

// Login handler — accepts anything. Drops a session cookie keyed to the email.
app.post('/login', (req, res) => {
  const { email } = req.body;
  if (!email) return res.redirect('/');
  // 24-hour cookie, no secure flag because we're inside Docker without HTTPS.
  res.cookie('sid', encodeURIComponent(email), { httpOnly: true, maxAge: 24 * 60 * 60 * 1000 });
  res.redirect('/dashboard');
});

// Dashboard — the "home" after login. Linked to billing so the agent sees a path.
app.get('/dashboard', requireLogin, (req, res) => {
  const email = decodeURIComponent(req.cookies.sid);
  res.send(page('Dashboard', `
    <div class="card">
      <h1>Welcome back</h1>
      <p class="lead">Signed in as ${escapeHtml(email)}.</p>
      <p>You're on the <strong>Pro Monthly</strong> plan. Manage your subscription from Billing.</p>
      <a href="/billing" class="btn btn-primary" style="display:block;">Go to Billing →</a>
    </div>
  `, { accountEmail: email }));
});

// Billing — shows the plan with a prominent Cancel button.
app.get('/billing', requireLogin, (req, res) => {
  const email = decodeURIComponent(req.cookies.sid);
  res.send(page('Billing', `
    <div class="card">
      <h1>Billing</h1>
      <p class="lead">Manage your MockBox subscription.</p>
      <div class="plan">
        <div class="plan-name">MockBox Pro Monthly</div>
        <div class="plan-price">$19.00 / month</div>
        <div class="plan-renews">Renews on the 1st of every month</div>
      </div>
      <a href="/cancel" class="btn btn-danger" style="display:block; margin-top:16px;">Cancel subscription</a>
    </div>
  `, { accountEmail: email }));
});

// Cancellation retention page — classic dark pattern.
// Big colorful "keep" button, tiny "yes, cancel" link to the right of it.
// This is the step the AI gets to prove it can beat retention UI.
app.get('/cancel', requireLogin, (req, res) => {
  const email = decodeURIComponent(req.cookies.sid);
  res.send(page('Cancel subscription', `
    <div class="card">
      <h1>Are you sure?</h1>
      <p class="lead">You'll lose access to everything that makes MockBox great.</p>
      <ul class="benefits">
        <li>Unlimited mock widgets</li>
        <li>Priority mock support</li>
        <li>MockBox Pro badge</li>
        <li>Early access to new mock features</li>
      </ul>
      <form method="POST" action="/cancel" class="cancel-actions">
        <a href="/billing" class="btn btn-primary" style="width:100%;">Keep my subscription</a>
        <button type="submit" class="btn btn-link">Yes, cancel anyway</button>
      </form>
    </div>
  `, { accountEmail: email }));
});

// Cancellation confirmation — this is the terminal "success" page.
// The agent needs to see a clearly positive message here to return success:true.
app.post('/cancel', requireLogin, (req, res) => {
  const email = decodeURIComponent(req.cookies.sid);
  res.send(page('Subscription cancelled', `
    <div class="card">
      <div class="success-badge">✓</div>
      <h1 style="text-align:center;">Subscription cancelled</h1>
      <p class="lead" style="text-align:center;">
        Your MockBox Pro subscription has been cancelled.
        You won't be charged again.
      </p>
      <p class="footer-note">Confirmation email sent to ${escapeHtml(email)}.</p>
    </div>
  `, { accountEmail: email }));
});

app.get('/logout', (req, res) => {
  res.clearCookie('sid');
  res.redirect('/');
});

// Tiny HTML escape — we interpolate the user's email into the page.
// Even in a mock, treat untrusted input correctly so the demo doesn't accidentally
// teach bad habits to anyone reading the code.
function escapeHtml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

app.listen(PORT, () => {
  console.log(`[mock-subscription] listening on :${PORT}`);
});
