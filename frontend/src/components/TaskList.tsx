'use client';

import { useEffect, useState } from 'react';
import { Task } from '@/lib/types';
import { getTask } from '@/lib/api';
import { StatusBadge } from './StatusBadge';
import { WatchPanel } from './WatchPanel';

interface Props {
  tasks: Task[];
}

const TERMINAL_STATUSES = new Set(['SUCCESS', 'DEAD_LETTER']);

// Human-friendly short host — e.g. "https://app.beehiiv.com/users/sign_in" → "app.beehiiv.com"
function hostFromPayload(payload: unknown): string | null {
  if (!payload || typeof payload !== 'object') return null;
  const url = (payload as { url?: string }).url;
  if (!url) return null;
  try {
    return new URL(url).host;
  } catch {
    return null;
  }
}

// Relative time ago — "just now", "2 min ago", "1 hr ago", or absolute date for older.
function relativeTime(iso: string): string {
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return 'just now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hr ago`;
  return new Date(iso).toLocaleDateString();
}

// TaskList shows all submitted tasks and polls active ones every 2 seconds.
// Once a task reaches a terminal state, polling stops for that task.
export function TaskList({ tasks: initialTasks }: Props) {
  const [tasks, setTasks] = useState<Task[]>(initialTasks);

  useEffect(() => {
    setTasks(initialTasks);
  }, [initialTasks]);

  useEffect(() => {
    const activeTasks = tasks.filter((t) => !TERMINAL_STATUSES.has(t.status));
    if (activeTasks.length === 0) return;

    const interval = setInterval(async () => {
      const updated = await Promise.all(
        activeTasks.map((t) => getTask(t.id).catch(() => t))
      );
      setTasks((prev) =>
        prev.map((t) => updated.find((u) => u.id === t.id) ?? t)
      );
    }, 2000);

    return () => clearInterval(interval);
  }, [tasks]);

  if (tasks.length === 0) {
    return (
      <div className="bg-white rounded-2xl border border-dashed border-gray-200 p-10 text-sm text-gray-400 text-center">
        No cancellations yet. Submit one above to get started.
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {tasks.map((task) => {
        const host = hostFromPayload(task.payload);
        return (
          <div
            key={task.id}
            className="bg-white rounded-2xl border border-gray-100 shadow-sm shadow-gray-900/[0.02] p-5 hover:shadow-md transition-shadow"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <StatusBadge status={task.status} />
                  {host && (
                    <span className="text-sm font-medium text-gray-900 truncate">
                      {host}
                    </span>
                  )}
                  {!host && (
                    <span className="text-sm font-medium text-gray-900 capitalize">
                      {task.service_name}
                    </span>
                  )}
                </div>
                <div className="mt-1 text-xs text-gray-500 flex items-center gap-3 flex-wrap">
                  <span>{task.user_email}</span>
                  <span>·</span>
                  <span>{relativeTime(task.created_at)}</span>
                  <span>·</span>
                  <span>attempt {task.attempts}/{task.max_attempts}</span>
                </div>
              </div>
            </div>

            {task.scheduled_for && task.status === 'PENDING' && (
              <div className="mt-3 text-xs text-indigo-700 bg-indigo-50 rounded-lg px-3 py-2">
                Scheduled for {new Date(task.scheduled_for).toLocaleDateString(undefined, {
                  weekday: 'long', year: 'numeric', month: 'long', day: 'numeric',
                })}
              </div>
            )}

            {task.error_message && (
              <div className="mt-3 text-xs text-rose-700 bg-rose-50 rounded-lg px-3 py-2">
                {task.error_message}
              </div>
            )}

            {/* Watch Mode — live screenshots for the AI agent while it's running,
                final frame after it finishes. */}
            {task.service_name === 'universal' && (
              task.status === 'IN_PROGRESS' ||
              task.status === 'PENDING' ||
              !!task.evidence_path
            ) && <WatchPanel task={task} />}

            {task.evidence_path && (
              <div className="mt-2 text-[10px] text-gray-300 font-mono truncate">
                {task.evidence_path}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
