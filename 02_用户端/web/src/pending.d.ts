export type PendingCommand = {requestId:string; payload?:any; body?:any; createdAt?:string;batchId?:string;progress?:any;path?:string};
export type PendingStore = {get(key:string):PendingCommand|null;put(key:string,value:PendingCommand):void;clear(key:string):string};
export function keepPending(error:any):boolean;
export function createPendingStore(storage:{get(key:string):any;set(key:string,value:any):void;remove(key:string):void}):PendingStore;
export function executeCommand<T>(store:PendingStore,key:string,draft:PendingCommand,send:(pending:PendingCommand)=>Promise<T>):Promise<{result:T;warning:string}>;

