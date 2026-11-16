import { api, newRequestId } from './api'
import { createPendingStore, executeCommand } from './pending.js'
export const pendingStore=createPendingStore({get:key=>JSON.parse(localStorage.getItem(key)||'null'),set:(key,value)=>localStorage.setItem(key,JSON.stringify(value)),remove:key=>localStorage.removeItem(key)})
const busy=new Set<string>()
export async function deliveryCommand(path:string,body:Record<string,unknown>){
 const key='campus_command_'+path
 if(busy.has(key))throw new Error('原请求正在提交，请稍候')
 busy.add(key)
 try { const saved=pendingStore.get(key);if(saved?.payload?.code)throw new Error('历史验证码请求已停止使用。请刷新订单状态并联系商家核对，原请求已保留，不能盲目重发。'); const draft={requestId:newRequestId('delivery'),payload:body};return await executeCommand(pendingStore,key,draft,command=>api(path,{method:'POST',body:{...command.payload,requestId:command.requestId},headers:{'Idempotency-Key':command.requestId}})) } finally {busy.delete(key)}
}

