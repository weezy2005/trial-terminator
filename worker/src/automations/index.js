// automations/index.js is the registry that maps service_name → automation function.
//
// ARCHITECTURAL DECISION: Why a registry (map) instead of dynamic imports?
// Dynamic imports (import(`./automations/${serviceName}.js`)) are dangerous —
// a malicious or corrupted task payload could set service_name to
// "../../etc/passwd" and cause a path traversal. An explicit registry means
// only the services we've intentionally registered can be executed.
// This is the principle of allowlisting over denylisting.
import { cancel as cancelNetflix } from './netflix.js';

// The registry maps service_name values (from the tasks table) to automation functions.
// To add a new service (e.g. Spotify), import its cancel function and add it here.
// The worker.js loop doesn't need to change at all — open/closed principle.
export const automations = {
  netflix: cancelNetflix,
  // spotify: cancelSpotify,   ← Sprint 4+
  // hulu:    cancelHulu,
};

// getAutomation returns the automation function for a given service name,
// or null if the service isn't supported yet.
export function getAutomation(serviceName) {
  return automations[serviceName.toLowerCase()] ?? null;
}
