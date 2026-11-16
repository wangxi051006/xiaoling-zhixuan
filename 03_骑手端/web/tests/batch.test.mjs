import test from 'node:test'
import assert from 'node:assert/strict'
import { batchFinished, executeBatch, validSelection } from '../src/batch.js'
import { createPendingStore } from '../src/pending.js'
function setup(){const values=new Map();return createPendingStore({get:key=>values.get(key),set:(key,value)=>values.set(key,structuredClone(value)),remove:key=>values.delete(key)})}
const draft=()=>({requestId:'immutable-42',path:'/courier/tasks/batch-actions',payload:{action:'PICKUP',checked:true,items:[{taskId:'9',expectedVersion:3}]}})
const progress=(result='PENDING')=>({batchId:'b1',expectedCount:1,completedCount:result==='PENDING'?0:1,pendingCount:result==='PENDING'?1:0,items:[{taskId:'9',expectedVersion:3,result,code:result==='FAILED'?'VERSION_CONFLICT':'',version:4}]})
test('lost response recovers batch by original key then resumes only pending items',async()=>{
 const store=setup(),original=draft(),calls=[]
 await assert.rejects(executeBatch(store,'batch',original,async()=>{throw new TypeError('lost response')}))
 assert.equal(store.get('batch').requestId,'immutable-42')
 const result=await executeBatch(store,'batch',null,async(path,options)=>{calls.push({path,options});if(path.includes('?'))return{batchId:'b1'};if(path.endsWith('/resume'))return progress('SUCCESS');return progress()},true)
 assert.equal(calls[0].path,'/courier/tasks/batch-actions?requestId=immutable-42&action=PICKUP')
 assert.equal(calls[2].path,'/courier/tasks/batch-actions/b1/resume')
 assert.equal(result.items[0].result,'SUCCESS');assert.equal(store.get('batch'),null)
})
test('server 503 exposes durable partial progress and preserves immutable version',async()=>{
 const store=setup(),partial=progress()
 await assert.rejects(executeBatch(store,'batch',draft(),async()=>{throw Object.assign(Error('temporary'),{status:503,data:partial})}))
 assert.equal(store.get('batch').batchId,'b1');assert.equal(store.get('batch').payload.items[0].expectedVersion,3)
 let calls=0
 await executeBatch(store,'batch',null,async()=>{calls++;return progress('FAILED')},true)
 assert.equal(calls,1);assert.equal(store.get('batch'),null)
})
test('conflicting request id never clears pending or silently sends a new payload',async()=>{
 const store=setup(),saved=draft();store.put('batch',saved)
 await assert.rejects(executeBatch(store,'batch',{...saved,requestId:'new-key'},async()=>{throw Object.assign(Error('conflict'),{status:409,code:'IDEMPOTENCY_CONFLICT'})},true))
 assert.equal(store.get('batch').requestId,saved.requestId);assert.deepEqual(store.get('batch').payload,saved.payload)
})
test('not recorded batch replays the exact original payload instead of caller replacement',async()=>{
 const store=setup(),saved=draft();store.put('batch',saved);let body
 await executeBatch(store,'batch',{...draft(),payload:{action:'COMPLETE'}},async(path,options)=>{if(!options)throw Object.assign(Error('not found'),{status:404});body=options.body;return progress('SUCCESS')},true)
 assert.deepEqual(body,{...saved.payload,requestId:saved.requestId})
})
test('expired arrival refresh is limited to resume body; original command is unchanged',async()=>{
 const store=setup(),saved=draft();saved.payload.action='COMPLETE';saved.payload.arrivalToken='old';saved.batchId='b1';store.put('batch',saved)
 let resumed
 await executeBatch(store,'batch',null,async(path,options)=>{if(options){resumed=options.body;assert.equal(store.get('batch').payload.arrivalToken,'old');return progress('SUCCESS')}return progress()},true,()=>{},async()=>({arrivalToken:'fresh'}))
 assert.deepEqual(resumed,{arrivalToken:'fresh'})
})
test('incomplete or mismatched progress does not report a batch finished',()=>{
 assert.equal(batchFinished({...progress('SUCCESS'),expectedCount:2}),false)
 assert.equal(batchFinished(progress()),false);assert.equal(batchFinished(progress('FAILED')),true)
})
test('refresh removes stale selections without upgrading their versions or adding rows',()=>{
 const selected=[{id:'1',version:2,status:'OPEN'},{id:'2',version:4,status:'OPEN'}]
 const current=[{id:'1',version:3,status:'OPEN'},{id:'2',version:4,status:'OPEN'},{id:'3',version:1,status:'OPEN'}]
 assert.deepEqual(validSelection(selected,current,row=>row.status==='OPEN'),[selected[1]])
 assert.equal(selected[0].version,2)
})
