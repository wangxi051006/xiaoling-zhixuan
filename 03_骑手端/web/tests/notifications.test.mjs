import test from 'node:test'
import assert from 'node:assert/strict'
import { parseSSE, mergeNotifications } from '../src/notifications.js'
test('SSE parser handles fragmented CRLF frames, comments, and multiple persisted events',()=>{
 const text=': heartbeat\r\n\r\nid: 7\r\nevent: delivery_notification\r\ndata: {"id":"7",\r\ndata: "body":"送达"}\r\n\r\nevent: delivery_notification\ndata: {"id":"8"}\n\n'
 let rest='',events=[]
 for(const character of text){const parsed=parseSSE(rest+character);rest=parsed.rest;events.push(...parsed.events)}
 assert.equal(rest,'');assert.deepEqual(events.map(event=>event.data.id),['7','8']);assert.equal(events[0].data.body,'送达')
})
test('malformed frame does not prevent later valid notification or consume incomplete data',()=>{
 const result=parseSSE('event: delivery_notification\ndata: bad\n\nevent: delivery_notification\ndata: {"id":"9"}\n\ndata: {"id":')
 assert.deepEqual(result.events.map(event=>event.data.id),['9']);assert.equal(result.rest,'data: {"id":')
})
test('reconnect backfill merges duplicate IDs and preserves read updates with large IDs',()=>{
 const merged=mergeNotifications([{id:'9007199254740993',body:'A',readAt:null}],[{id:'9007199254740992',body:'B'},{id:'9007199254740993',readAt:'now'}])
 assert.deepEqual(merged.map(item=>item.id),['9007199254740992','9007199254740993']);assert.equal(merged[1].body,'A');assert.equal(merged[1].readAt,'now')
})
