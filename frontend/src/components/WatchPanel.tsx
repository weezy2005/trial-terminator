'use client';

import { useEffect, useState } from 'react';
import { Task } from '@/lib/types';
import { screenshotUrl } from '@/lib/api';

interface Props {
  task: Task;
}

// WatchPanel shows the live screenshot of the AI agent's browser while a task
// is IN_PROGRESS, and the final evidence screenshot after it dead-letters.
//
// Why re-fetch via a cache-busting query string instead of refreshing the <img>?
// Setting a new src with ?t=<timestamp> forces the browser to issue a fresh
// HTTP request even when caching headers aren't perfectly honored. The Go API
// also sends no-store headers, but the query string is a belt-and-suspenders
// guarantee that we always see the latest frame.
export function WatchPanel({ task }: Props) {
  const [tick, setTick] = useState(() => Date.now());
  // haveFrame starts false and flips true the first time ANY img load succeeds.
  // Once true it stays true — a transient 404 between polls shouldn't flash
  // "waiting" over a frame we already showed.
  const [haveFrame, setHaveFrame] = useState(false);

  const isActive = task.status === 'IN_PROGRESS' || task.status === 'PENDING';

  // Poll fresh screenshot every 1.5s while the task is active.
  useEffect(() => {
    if (!isActive) return;
    const interval = setInterval(() => setTick(Date.now()), 1500);
    return () => clearInterval(interval);
  }, [isActive]);

  // When the task reaches a terminal state, snap once more to the current frame
  // (so the user sees the final "what the agent was looking at" image).
  useEffect(() => {
    if (!isActive) setTick(Date.now());
  }, [isActive]);

  const src = `${screenshotUrl(task.id)}?t=${tick}`;

  return (
    <div className="mt-4 rounded-xl bg-gray-900 p-2">
      <div className="flex items-center justify-between mb-2 px-2 pt-1">
        <span className="text-[10px] uppercase tracking-wider font-semibold text-gray-400">
          {isActive ? 'Live view' : 'Last frame'}
        </span>
        {isActive && (
          <span className="flex items-center gap-1.5 text-[10px] text-red-400 font-medium">
            <span className="w-1.5 h-1.5 rounded-full bg-red-500 animate-pulse" />
            REC
          </span>
        )}
      </div>
      <div className="relative aspect-[16/10] bg-gray-800 overflow-hidden rounded-lg">
        {/* Placeholder underneath the img — visible only until we've shown a frame.
            The img sits above it; onLoad flips haveFrame true and hides this. */}
        {!haveFrame && (
          <div className="absolute inset-0 flex items-center justify-center text-xs text-gray-500">
            Waiting for first screenshot…
          </div>
        )}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          key={src}
          src={src}
          alt={`AI agent view for task ${task.id.slice(0, 8)}`}
          className={`w-full h-full object-cover transition-opacity duration-200 ${haveFrame ? 'opacity-100' : 'opacity-0'}`}
          onLoad={() => setHaveFrame(true)}
          onError={() => { /* keep whatever we had; next tick retries */ }}
        />
      </div>
    </div>
  );
}
