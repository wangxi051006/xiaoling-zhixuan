export const TOKEN_KEY: string;
export const SESSION_KEY: string;
export const SESSION_EXPIRED_EVENT: string;
export function discardRejectedSession(storage: Pick<Storage, 'getItem' | 'removeItem'>, rejectedToken: string): boolean;
