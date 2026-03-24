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
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError(null);

    try {
      const task = await createTask({
        idempotency_key: idempotencyKey,
        service_name: serviceName,
        user_email: userEmail,
        payload: password ? { password } : undefined,
      });
      onTaskCreated(task);
      // Reset form fields but NOT the key — the parent component
      // will show the new task. If they submit again, the key stays the same
      // (idempotent replay) until they navigate away or refresh.
      setUserEmail('');
      setPassword('');
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
