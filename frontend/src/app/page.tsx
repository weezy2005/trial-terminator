'use client';

import { useState } from 'react';
import { Task } from '@/lib/types';
import { CreateTaskForm } from '@/components/CreateTaskForm';
import { TaskList } from '@/components/TaskList';

export default function Home() {
  // Tasks are stored in component state — this is a client-side dashboard.
  // Newly created tasks are prepended so the most recent appears at the top.
  const [tasks, setTasks] = useState<Task[]>([]);
  const [showMetrics, setShowMetrics] = useState(false);

  function handleTaskCreated(task: Task) {
    setTasks((prev) => {
      // If the task already exists (idempotent replay), update it in place.
      const exists = prev.find((t) => t.id === task.id);
      if (exists) return prev.map((t) => (t.id === task.id ? task : t));
      return [task, ...prev];
    });
  }

  return (
    <div className="min-h-screen flex flex-col">
      {/* Header */}
      <header className="border-b border-gray-100 bg-white/80 backdrop-blur-sm sticky top-0 z-10">
        <div className="max-w-3xl mx-auto px-6 py-4 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <div className="w-7 h-7 rounded-lg gradient-brand flex items-center justify-center text-white text-sm">
              ✂
            </div>
            <span className="font-semibold text-gray-900">TrialTerminator</span>
          </div>
          <button
            onClick={() => setShowMetrics((v) => !v)}
            className="text-xs text-gray-500 hover:text-gray-800 transition"
          >
            {showMetrics ? 'Hide metrics' : 'Metrics'}
          </button>
        </div>
        {showMetrics && (
          <div className="max-w-3xl mx-auto px-6 pb-3 flex gap-4 text-xs text-gray-500">
            <a href="http://localhost:3001/d/trialterminator-main" target="_blank" rel="noreferrer" className="hover:text-gray-800 underline underline-offset-2">
              Grafana dashboard →
            </a>
            <a href="http://localhost:9090/graph?g0.expr=sum(trialterminator_http_request_duration_seconds_count)&g0.tab=0" target="_blank" rel="noreferrer" className="hover:text-gray-800 underline underline-offset-2">
              Prometheus graph →
            </a>
            <a href="http://localhost:8081/metrics" target="_blank" rel="noreferrer" className="hover:text-gray-800 underline underline-offset-2">
              Raw metrics →
            </a>
            <a href="http://localhost:3002" target="_blank" rel="noreferrer" className="hover:text-gray-800 underline underline-offset-2">
              MockBox demo site →
            </a>
          </div>
        )}
      </header>

      {/* Hero */}
      <section className="max-w-3xl w-full mx-auto px-6 pt-16 pb-10 text-center">
        <div className="inline-flex items-center justify-center w-14 h-14 rounded-2xl gradient-brand text-white text-2xl mb-6 shadow-lg shadow-indigo-500/20">
          ✂
        </div>
        <h1 className="text-4xl md:text-5xl font-bold tracking-tight text-gray-900">
          Cancel what&apos;s{' '}
          <span className="gradient-brand-text">draining your card</span>
        </h1>
        <p className="mt-4 text-lg text-gray-500 max-w-xl mx-auto">
          AI-powered subscription cancellation. Enter a URL, we handle the rest.
        </p>
      </section>

      {/* Form */}
      <section className="max-w-3xl w-full mx-auto px-6">
        <CreateTaskForm onTaskCreated={handleTaskCreated} />
      </section>

      {/* Feed */}
      <section className="max-w-3xl w-full mx-auto px-6 pt-10 pb-20 flex-1">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-gray-700 uppercase tracking-wider">
            Recent cancellations
          </h2>
          {tasks.length > 0 && (
            <span className="text-xs text-gray-400">{tasks.length} total</span>
          )}
        </div>
        <TaskList tasks={tasks} />
      </section>

      {/* Footer */}
      <footer className="border-t border-gray-100 bg-white/50">
        <div className="max-w-3xl mx-auto px-6 py-5 text-xs text-gray-400 flex items-center justify-between">
          <span>Go · Redis · Playwright · Gemini</span>
          <span>Portfolio demo</span>
        </div>
      </footer>
    </div>
  );
}
