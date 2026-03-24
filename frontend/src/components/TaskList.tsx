'use client';

import { useEffect, useState } from 'react';
import { Task } from '@/lib/types';
import { getTask } from '@/lib/api';
import { StatusBadge } from './StatusBadge';

interface Props {
  tasks: Task[];
}

const TERMINAL_STATUSES = new Set(['SUCCESS', 'DEAD_LETTER']);

// TaskList shows all submitted tasks and polls active ones every 2 seconds.
// Once a task reaches a terminal state, polling stops for that task.
export function TaskList({ tasks: initialTasks }: Props) {
  const [tasks, setTasks] = useState<Task[]>(initialTasks);

  // Sync when parent adds a new task.
  useEffect(() => {
    setTasks(initialTasks);
  }, [initialTasks]);

  // Poll non-terminal tasks every 2 seconds.
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
      <div className="bg-white rounded-lg border border-gray-200 p-6 text-sm text-gray-400 text-center">
        No tasks yet. Submit one using the form.
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {tasks.map((task) => (
        <div key={task.id} className="bg-white rounded-lg border border-gray-200 p-4 space-y-2">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <StatusBadge status={task.status} />
              <span className="text-sm font-medium text-gray-800 capitalize">{task.service_name}</span>
            </div>
            <span className="text-xs text-gray-400">
              {new Date(task.created_at).toLocaleTimeString()}
            </span>
          </div>

          <div className="text-xs text-gray-500 space-y-1">
            <div>Email: {task.user_email}</div>
            <div>Attempts: {task.attempts} / {task.max_attempts}</div>
            <div className="font-mono text-gray-400">ID: {task.id.slice(0, 8)}…</div>
          </div>

          {task.error_message && (
            <div className="text-xs text-red-600 bg-red-50 rounded px-2 py-1">
              {task.error_message}
            </div>
          )}

          {task.evidence_path && (
            <div className="text-xs text-orange-600 bg-orange-50 rounded px-2 py-1">
              📸 Screenshot: {task.evidence_path}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
