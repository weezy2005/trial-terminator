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
  const [idempotencyKey, setIdempotencyKey] = useState(() => uuidv4());
  const [serviceName, setServiceName] = useState<'test' | 'universal'>('universal');
  const [userEmail, setUserEmail] = useState('');
  const [password, setPassword] = useState('');
  const [websiteUrl, setWebsiteUrl] = useState('');
  const [scheduleDate, setScheduleDate] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError(null);

    try {
      const scheduledFor = scheduleDate ? new Date(scheduleDate).toISOString() : undefined;

      const task = await createTask({
        idempotency_key: idempotencyKey,
        service_name: serviceName,
        user_email: userEmail,
        payload: {
          ...(password ? { password } : {}),
          ...(serviceName === 'universal' && websiteUrl ? { url: websiteUrl } : {}),
        },
        scheduled_for: scheduledFor,
      });
      onTaskCreated(task);
      // Reset for the next submission.
      setUserEmail('');
      setPassword('');
      setWebsiteUrl('');
      setScheduleDate('');
      setIdempotencyKey(uuidv4());
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Request failed');
    } finally {
      setLoading(false);
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="bg-white rounded-2xl border border-gray-100 shadow-sm shadow-gray-900/5 p-6 space-y-5"
    >
      {/* Service tabs */}
      <div className="flex gap-2 p-1 bg-gray-100 rounded-xl">
        <button
          type="button"
          onClick={() => setServiceName('universal')}
          className={`flex-1 py-2 px-3 rounded-lg text-sm font-medium transition ${
            serviceName === 'universal'
              ? 'bg-white shadow-sm text-gray-900'
              : 'text-gray-500 hover:text-gray-800'
          }`}
        >
          AI Agent
        </button>
        <button
          type="button"
          onClick={() => setServiceName('test')}
          className={`flex-1 py-2 px-3 rounded-lg text-sm font-medium transition ${
            serviceName === 'test'
              ? 'bg-white shadow-sm text-gray-900'
              : 'text-gray-500 hover:text-gray-800'
          }`}
        >
          Test (always succeeds)
        </button>
      </div>

      {/* URL — only for AI agent */}
      {serviceName === 'universal' && (
        <div>
          <label className="block text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">
            Subscription URL
          </label>
          <input
            type="url"
            required
            value={websiteUrl}
            onChange={(e) => setWebsiteUrl(e.target.value)}
            placeholder="https://patreon.com or any login page"
            className="w-full border border-gray-200 rounded-xl px-4 py-3 text-sm placeholder:text-gray-400 focus:outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 transition"
          />
        </div>
      )}

      {/* Email + password row */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div>
          <label className="block text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">
            Email
          </label>
          <input
            type="email"
            required
            value={userEmail}
            onChange={(e) => setUserEmail(e.target.value)}
            placeholder="you@example.com"
            className="w-full border border-gray-200 rounded-xl px-4 py-3 text-sm placeholder:text-gray-400 focus:outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 transition"
          />
        </div>
        <div>
          <label className="block text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">
            Password <span className="text-gray-300 font-normal normal-case">(optional)</span>
          </label>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="••••••••"
            className="w-full border border-gray-200 rounded-xl px-4 py-3 text-sm placeholder:text-gray-400 focus:outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 transition"
          />
        </div>
      </div>

      {/* Schedule date */}
      <div>
        <label className="block text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">
          Cancel on <span className="text-gray-300 font-normal normal-case">(optional — blank = run now)</span>
        </label>
        <input
          type="date"
          value={scheduleDate}
          min={new Date().toISOString().split('T')[0]}
          onChange={(e) => setScheduleDate(e.target.value)}
          className="w-full border border-gray-200 rounded-xl px-4 py-3 text-sm placeholder:text-gray-400 focus:outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 transition"
        />
        {scheduleDate && (
          <p className="text-xs text-indigo-600 mt-2">
            Scheduled for {new Date(scheduleDate).toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })}
          </p>
        )}
      </div>

      {error && (
        <div className="bg-red-50 border border-red-100 text-red-700 text-sm rounded-xl px-4 py-3">
          {error}
        </div>
      )}

      <button
        type="submit"
        disabled={loading}
        className="w-full gradient-brand text-white rounded-xl px-5 py-3 text-sm font-semibold shadow-lg shadow-indigo-500/25 hover:shadow-xl hover:shadow-indigo-500/30 transition-all disabled:opacity-50 disabled:cursor-not-allowed disabled:shadow-none"
      >
        {loading ? 'Submitting…' : 'Cancel subscription →'}
      </button>

      <div className="text-[10px] text-gray-300 text-center font-mono">
        key: {idempotencyKey.slice(0, 8)}…
      </div>
    </form>
  );
}
