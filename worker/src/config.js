// config.js loads all environment variables in one place.
// Every other module imports from here — never from process.env directly.
//
// ARCHITECTURAL DECISION: Why centralise env vars?
// If you scatter process.env.POSTGRES_HOST across 5 files, changing the
// variable name means hunting through the entire codebase. Centralising
// means one file to change, one file to validate at startup.
import 'dotenv/config';

export const config = {
  postgres: {
    host:     process.env.POSTGRES_HOST     || 'localhost',
    port:     parseInt(process.env.POSTGRES_PORT || '5432'),
    user:     process.env.POSTGRES_USER     || 'trialterminator',
    password: process.env.POSTGRES_PASSWORD || 'secret',
    database: process.env.POSTGRES_DB       || 'trialterminator',
  },
  redis: {
    host: (process.env.REDIS_ADDR || 'localhost:6379').split(':')[0],
    port: parseInt((process.env.REDIS_ADDR || 'localhost:6379').split(':')[1]),
  },
  worker: {
    // How long (seconds) BRPOP waits before timing out and looping.
    // A timeout of 0 blocks forever — dangerous, as it prevents clean shutdown.
    // 5 seconds means the worker checks for a shutdown signal every 5 seconds
    // at most, keeping shutdown time short.
    brpopTimeout: 5,

    // Path where failure screenshots are saved.
    evidencePath: process.env.EVIDENCE_PATH || '../evidence',
  },
};
