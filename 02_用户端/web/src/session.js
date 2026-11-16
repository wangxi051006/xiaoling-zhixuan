export const TOKEN_KEY = 'campus_token';
export const SESSION_KEY = 'campus_session';
export const SESSION_EXPIRED_EVENT = 'campus:session-expired';

// An older request must never sign out a session established after it began.
// Pending orders and commands have separate keys and remain recoverable.
export function discardRejectedSession(storage, rejectedToken) {
  if (storage.getItem(TOKEN_KEY) !== rejectedToken) return false;
  storage.removeItem(TOKEN_KEY);
  storage.removeItem(SESSION_KEY);
  return true;
}
