export function batchFinished(result: any): boolean;
export function validSelection<T extends { id:string; version:number }>(selected:T[], rows:T[], eligible:(row:T)=>boolean):T[];
export function executeBatch(store:any,key:string,draft:any,request:(path:string,options?:any)=>Promise<any>,resume?:boolean,onProgress?:(result:any)=>void,resumeBody?:(pending:any,result:any)=>Promise<any>):Promise<any>;

