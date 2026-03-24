'use client';

import { useState } from 'react';
import { Task } from '@/lib/types';
import { CreateTaskForm } from '@/components/CreateTaskForm';
import { TaskList } from '@/components/TaskList';

export default function Home() {
  // Tasks are stored in component state — this is a client-side dashboard.
  // Newly created tasks are prepended so the most recent appears at the top.
  const [tasks, setTasks] = useState<Task[]>([]);

  function handleTaskCreated(task: Task) {
    setTasks((prev) => {
      // If the task already exists (idempotent replay), update it in place.
      const exists = prev.find((t) => t.id === task.id);
      if (exists) return prev.map((t) => (t.id === task.id ? task : t));
      return [task, ...prev];
    });
  }

  return (
    <div className="max-w-4xl mx-auto px-4 py-10">
      {/* Header */}
      <div className="mb-8">
        <h1 className="text-3xl font-bold text-gray-900">TrialTerminator</h1>
        <p className="text-gray-500 mt-1">
          Distributed subscription cancellation — Go + Redis + Playwright
        </p>
        <div className="flex gap-4 mt-3 text-xs text-gray-400">
          <a href="http://localhost:3001" target="_blank" className="hover:text-gray-600 underline">
            Grafana Dashboard →
          </a>
          <a href="http://localhost:9090" target="_blank" className="hover:text-gray-600 underline">
            Prometheus →
          </a>
          <a href="http://localhost:8080/metrics" target="_blank" className="hover:text-gray-600 underline">
            Raw Metrics →
          </a>
        </div>
      </div>

      {/* Two-column layout */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="md:col-span-1">
          <CreateTaskForm onTaskCreated={handleTaskCreated} />
        </div>
        <div className="md:col-span-2">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-semibold text-gray-700 uppercase tracking-wide">
              Live Task Feed
            </h2>
            {tasks.length > 0 && (
              <span className="text-xs text-gray-400">{tasks.length} tasks</span>
            )}
          </div>
          <TaskList tasks={tasks} />
        </div>
      </div>
    </div>
  );
}
