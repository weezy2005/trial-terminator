// api.ts is the single place the frontend calls the Go API.
// All fetch() calls live here — never scattered across components.
import { Task, CreateTaskPayload } from './types';

const API_URL = process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8080';

export async function createTask(payload: CreateTaskPayload): Promise<Task> {
  const res = await fetch(`${API_URL}/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'unknown error' }));
    throw new Error(err.error || `HTTP ${res.status}`);
  }
  return res.json();
}

export async function getTask(id: string): Promise<Task> {
  const res = await fetch(`${API_URL}/tasks/${id}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

// screenshotUrl returns the URL for a task's live screenshot. The query string
// cache-buster is appended by the caller on each poll so the <img> actually
// re-fetches instead of using the browser cache.
export function screenshotUrl(id: string): string {
  return `${API_URL}/tasks/${id}/screenshot`;
}
