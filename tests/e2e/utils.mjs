// utils.mjs — shared helpers for all e2e tests.
import { randomUUID } from 'crypto';

export const API_URL = process.env.API_URL || 'http://localhost:8080';

// createTask calls POST /tasks and returns the created task object.
// A fresh idempotency key is generated per call — each test run is independent.
export async function createTask(serviceName, userEmail, payload = {}) {
  const body = {
    idempotency_key: randomUUID(),
    service_name: serviceName,
    user_email: userEmail,
    payload,
  };

  const res = await fetch(`${API_URL}/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const text = await res.text();
    throw new Error(`POST /tasks failed (${res.status}): ${text}`);
  }

  return res.json();
}

// getTask fetches a task by ID.
export async function getTask(taskId) {
  const res = await fetch(`${API_URL}/tasks/${taskId}`);
  if (!res.ok) throw new Error(`GET /tasks/${taskId} failed (${res.status})`);
  return res.json();
}

// pollUntil repeatedly calls fn() until the predicate returns true or timeout is reached.
// This is how we wait for asynchronous state changes without Thread.sleep loops.
//
// ARCHITECTURAL DECISION: Why poll instead of websockets?
// For an e2e test script, polling is simpler and sufficient. In the production
// frontend (Sprint 5), we use the same polling approach for the same reason —
// adding websockets would require a separate Go goroutine, a websocket library,
// and frontend state management. Polling every 2 seconds is imperceptible to users
// and has zero infrastructure cost.
export async function pollUntil(fn, predicate, { intervalMs = 2000, timeoutMs = 60000 } = {}) {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    const result = await fn();
    if (predicate(result)) return result;
    await new Promise((r) => setTimeout(r, intervalMs));
  }

  throw new Error(`pollUntil timed out after ${timeoutMs}ms`);
}

// assert throws if condition is false — lightweight assertion without a test framework.
export function assert(condition, message) {
  if (!condition) throw new Error(`ASSERTION FAILED: ${message}`);
  console.log(`  ✓ ${message}`);
}
