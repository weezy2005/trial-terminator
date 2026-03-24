import { TaskStatus } from '@/lib/types';

const styles: Record<TaskStatus, string> = {
  PENDING:     'bg-gray-100 text-gray-700',
  IN_PROGRESS: 'bg-blue-100 text-blue-700',
  SUCCESS:     'bg-green-100 text-green-700',
  FAILED:      'bg-yellow-100 text-yellow-700',
  RETRY:       'bg-orange-100 text-orange-700',
  DEAD_LETTER: 'bg-red-100 text-red-700',
};

export function StatusBadge({ status }: { status: TaskStatus }) {
  return (
    <span className={`px-2 py-1 rounded text-xs font-semibold ${styles[status]}`}>
      {status.replace('_', ' ')}
    </span>
  );
}
