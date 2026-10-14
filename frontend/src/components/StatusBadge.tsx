import { TaskStatus } from '@/lib/types';

// Status styling + icon + friendly label all in one lookup so a new state
// requires one edit here instead of hunting across components.
const config: Record<TaskStatus, { bg: string; text: string; icon: string; label: string }> = {
  PENDING:     { bg: 'bg-gray-100',    text: 'text-gray-600',    icon: '◷',  label: 'Pending'     },
  IN_PROGRESS: { bg: 'bg-indigo-50',   text: 'text-indigo-700',  icon: '⟳',  label: 'In progress' },
  SUCCESS:     { bg: 'bg-emerald-50',  text: 'text-emerald-700', icon: '✓',  label: 'Cancelled'   },
  FAILED:      { bg: 'bg-amber-50',    text: 'text-amber-700',   icon: '!',  label: 'Failed'      },
  RETRY:       { bg: 'bg-amber-50',    text: 'text-amber-700',   icon: '↻',  label: 'Retrying'    },
  DEAD_LETTER: { bg: 'bg-rose-50',     text: 'text-rose-700',    icon: '✗',  label: 'Dead letter' },
};

export function StatusBadge({ status }: { status: TaskStatus }) {
  const c = config[status];
  const spinning = status === 'IN_PROGRESS' || status === 'RETRY';
  return (
    <span className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium ${c.bg} ${c.text}`}>
      <span className={spinning ? 'inline-block animate-spin' : 'inline-block'}>{c.icon}</span>
      {c.label}
    </span>
  );
}
