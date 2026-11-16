export function parseSSE(buffer: string): {events: Array<{event:string;id:string;data:any}>;rest:string};
export function mergeNotifications(previous: any[], incoming: any[]): any[];
