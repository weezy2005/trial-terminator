// index.js is the entry point for the worker process.
// Its only job: start the worker and handle top-level unhandled errors.
import { start } from './worker.js';

// Top-level unhandled rejection handler.
// If a promise rejects and nothing catches it, Node.js would silently swallow it
// in older versions. This ensures the process exits with a non-zero code,
// which Docker/Kubernetes treats as a crash and restarts the container.
process.on('unhandledRejection', (reason) => {
  console.error('[fatal] unhandled promise rejection:', reason);
  process.exit(1);
});

start().catch((err) => {
  console.error('[fatal] worker crashed on startup:', err);
  process.exit(1);
});
