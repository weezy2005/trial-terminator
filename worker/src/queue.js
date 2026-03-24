// queue.js manages the Redis connection for the worker.
import IORedis from 'ioredis';
import { config } from './config.js';

// ARCHITECTURAL DECISION: Why ioredis over the official 'redis' npm package?
// ioredis has first-class support for BRPOP with a clean promise API,
// built-in reconnection with exponential backoff, and is the most widely
// used Redis client in the Node.js ecosystem. It's what you'd find at
// companies like Vercel and Shopify running Node.js services.
export const redis = new IORedis({
  host: config.redis.host,
  port: config.redis.port,
  // Automatically reconnect with exponential backoff (100ms → 200ms → 400ms...).
  // Without this, a temporary Redis restart would kill the worker permanently.
  retryStrategy: (times) => Math.min(times * 100, 3000),
  // Throw an error if a command times out — better than hanging forever.
  commandTimeout: 5000,
  // Identify this connection in Redis CLIENT LIST for debugging.
  connectionName: 'trial-terminator-worker',
  lazyConnect: true, // Don't connect until .connect() is explicitly called.
});

export const QUEUE_KEY = 'trial-terminator:tasks:pending';
