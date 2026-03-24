// Mirrors the Go models.Task struct.
// Keeping these in sync is the responsibility of the API contract.
// In a larger team you'd auto-generate these from an OpenAPI spec.
export type TaskStatus =
  | 'PENDING'
  | 'IN_PROGRESS'
  | 'SUCCESS'
  | 'FAILED'
  | 'RETRY'
  | 'DEAD_LETTER';

export interface Task {
  id: string;
  idempotency_key: string;
  service_name: string;
  user_email: string;
  status: TaskStatus;
  attempts: number;
  max_attempts: number;
  payload?: Record<string, unknown>;
  error_message?: string;
  evidence_path?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateTaskPayload {
  idempotency_key: string;
  service_name: string;
  user_email: string;
  payload?: Record<string, unknown>;
}
