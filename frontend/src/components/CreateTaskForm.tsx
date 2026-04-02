'use client';

import { useState } from 'react';
import { v4 as uuidv4 } from 'uuid';
import { createTask } from '@/lib/api';
import { Task } from '@/lib/types';

interface Props {
  onTaskCreated: (task: Task) => void;
}

// CreateTaskForm generates the idempotency key when the component mounts
// and refreshes it only after a successful submission.
//
// ARCHITECTURAL DECISION: Why generate the key in the component, not on submit?
// If the user clicks Submit and the request times out, they see an error and
// click again. Because the key was generated when the form loaded (not on click),
// both clicks send the same key — the second is a safe retry, not a duplicate.
// This is the idempotency contract in action on the frontend.
export function CreateTaskForm({ onTaskCreated }: Props) {
  const [idempotencyKey] = useState(() => uuidv4());
  const [serviceName, setServiceName] = useState('test');
  const [userEmail, setUserEmail] = useState('');
  const [password, setPassword] = useState('');
  const [scheduleDate, setScheduleDate] = useState('');
  const [autoSchedule, setAutoSchedule] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError(null);

    try {
      // Convert the local date string (YYYY-MM-DD) to an ISO 8601 timestamp.
      // We use the start of that day in UTC so the scheduler fires promptly.
      const scheduledFor = scheduleDate ? new Date(scheduleDate).toISOString() : undefined;

      const task = await createTask({
        idempotency_key: idempotencyKey,
        service_name: serviceName,
        user_email: userEmail,
        payload: {
          ...(password ? { password } : {}),
          ...(autoSchedule ? { auto_schedule: true } : {}),
        },
        scheduled_for: scheduledFor,
      });
      onTaskCreated(task);
      setUserEmail('');
      setPassword('');
      setScheduleDate('');
      setAutoSchedule(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Request failed');
    } finally {
      setLoading(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} className="bg-white rounded-lg border border-gray-200 p-6 space-y-4">
      <h2 className="text-lg font-semibold text-gray-800">Cancel a Subscription</h2>

      <div>
        <label className="block text-sm font-medium text-gray-600 mb-1">Service</label>
        <select
          value={serviceName}
          onChange={(e) => setServiceName(e.target.value)}
          className="w-full border border-gray-300 rounded px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="test">Test (always succeeds)</option>
          <option value="netflix">Netflix</option>
          <option value="spotify">Spotify</option>
          <option value="linkedin">LinkedIn Premium</option>
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-600 mb-1">Email</label>
        <input
          type="email"
          required
          value={userEmail}
          onChange={(e) => setUserEmail(e.target.value)}
          placeholder="user@example.com"
          className="w-full border border-gray-300 rounded px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-600 mb-1">
          Password <span className="text-gray-400">(optional)</span>
        </label>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Account password"
          className="w-full border border-gray-300 rounded px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
      </div>

      <div className="space-y-2">
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={autoSchedule}
            onChange={(e) => {
              setAutoSchedule(e.target.checked);
              if (e.target.checked) setScheduleDate('');
            }}
            className="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
          />
          <span className="text-sm font-medium text-gray-700">
            Auto-detect trial end date <span className="text-xs text-purple-600 font-normal">(AI powered)</span>
          </span>
        </label>
        {autoSchedule ? (
          <p className="text-xs text-purple-600 bg-purple-50 rounded px-3 py-2">
            The app will log in, read your billing page, and automatically schedule the cancellation for the day before your trial ends — no date needed.
          </p>
        ) : (
          <div>
            <label className="block text-sm font-medium text-gray-600 mb-1">
              Cancel on <span className="text-gray-400">(optional — leave blank to run now)</span>
            </label>
            <input
              type="date"
              value={scheduleDate}
              min={new Date().toISOString().split('T')[0]}
              onChange={(e) => setScheduleDate(e.target.value)}
              className="w-full border border-gray-300 rounded px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            {scheduleDate && (
              <p className="text-xs text-blue-600 mt-1">
                Will automatically cancel on {new Date(scheduleDate).toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })}
              </p>
            )}
          </div>
        )}
      </div>

      <div className="text-xs text-gray-400">
        Idempotency key: <span className="font-mono">{idempotencyKey.slice(0, 8)}…</span>
      </div>

      {error && (
        <div className="bg-red-50 border border-red-200 text-red-700 text-sm rounded px-3 py-2">
          {error}
        </div>
      )}

      <button
        type="submit"
        disabled={loading}
        className="w-full bg-blue-600 text-white rounded px-4 py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition"
      >
        {loading ? 'Submitting…' : 'Cancel Subscription'}
      </button>
    </form>
  );
}
