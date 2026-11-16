// Invoked by the Go/MySQL integration test with a short-lived httptest server.
// No handwritten successful responses: every result below comes from Go + MySQL.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';

let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const { batchFinished, executeBatch } = await import(pathToFileURL(process.argv[2]).href);
const { createPendingStore } = await import(new URL('./pending.js', pathToFileURL(process.argv[2])));
const values = new Map();
const store = createPendingStore({
  get: key => values.get(key),
  set: (key, value) => values.set(key, structuredClone(value)),
  remove: key => values.delete(key),
}, () => 'contract-admin');
const path = '/admin/orders/batch-actions';
async function request(endpoint, options = {}) {
  const response = await fetch(config.baseURL + '/api/v1' + endpoint, {
    method: options.method || 'GET',
    headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
    ...(options.body === undefined ? {} : { body: JSON.stringify(options.body) }),
    signal: AbortSignal.timeout(10000),
  });
  const body = await response.json();
  if (!response.ok) throw Object.assign(Error(body.message), { status: response.status, code: body.code, data: body.data });
  assert.equal(body.code, 'OK');
  return body.data;
}
function done(result, count) {
  assert.equal(result.status, 'DONE');
  assert.equal(result.expectedCount, count);
  assert.equal(result.completedCount, count);
  assert.equal(result.pendingCount, 0, 'actual DONE response must explicitly expose pendingCount=0');
  assert.equal(result.items.length, count);
  assert.equal(batchFinished(result), true);
}
const accept = { path, requestId: 'contract_accept_request_0001', payload: {
  action: 'ACCEPT', items: [config.paid, config.unpaid],
} };
const accepted = await executeBatch(store, 'merchant', accept, request);
done(accepted, 2);
assert.deepEqual(accepted.items.map(item => item.result).sort(), ['FAILED', 'SUCCESS']);
assert.equal(store.get('merchant'), null);
console.log('PASS first ACCEPT partial-success HTTP response clears pending');

const paidResult = accepted.items.find(item => item.orderId === config.paid.orderId);
const ready = { path, requestId: 'contract_ready_request_0001', payload: {
  action: 'READY', items: [{ orderId: config.paid.orderId, expectedVersion: paidResult.version }],
} };
const prepared = await executeBatch(store, 'merchant', ready, request);
done(prepared, 1);
assert.equal(prepared.items[0].result, 'SUCCESS');
assert.equal(store.get('merchant'), null);
console.log('PASS next READY is submitted immediately and clears pending');

for (const original of [accept, ready]) {
  const replay = await request(path, { method: 'POST', body: { ...original.payload, requestId: original.requestId } });
  const detail = await request(path + '/' + replay.batchId);
  const resume = await request(path + '/' + replay.batchId + '/resume', { method: 'POST', body: {} });
  for (const response of [replay, detail, resume]) done(response, original.payload.items.length);
  assert.deepEqual(replay.items, resume.items);
}
console.log('PASS ACCEPT/READY replay, query and resume expose consistent completion counts');

const failed = await executeBatch(store, 'merchant', { ...accept, requestId: 'contract_all_failed_request_01' }, request);
done(failed, 2);
assert.ok(failed.items.every(item => item.result === 'FAILED'));
assert.equal(store.get('merchant'), null);
console.log('PASS fully failed terminal batch clears pending');

await assert.rejects(executeBatch(store, 'lost', accept, async (endpoint, options) => {
  await request(endpoint, options);
  throw new TypeError('intentional response loss after a real HTTP success');
}));
assert.equal(store.get('lost').requestId, accept.requestId);
const recovered = await executeBatch(store, 'lost', null, request, true);
done(recovered, 2);
assert.equal(store.get('lost'), null);
console.log('PASS lost response retains original request and recovers by real lookup/query');

const conflicting = structuredClone(accept);
conflicting.payload.items[0].expectedVersion += 100;
await assert.rejects(executeBatch(store, 'conflict', conflicting, request), error => error.status === 409);
assert.equal(store.get('conflict').requestId, accept.requestId);
assert.deepEqual(store.get('conflict').payload, conflicting.payload);
console.log('PASS real 409 preserves original requestId and immutable payload');

// The fixture caused a real MySQL audit failure before launching this process.
// Replay that actual 503 body, then use real GET/resume after removing the fault.
assert.equal(config.partialData.pendingCount, 1);
await assert.rejects(executeBatch(store, 'partial', config.partialDraft, async () => {
  throw Object.assign(Error('recorded real database failure'), { status: 503, code: 'BATCH_INCOMPLETE', data: config.partialData });
}), error => error.status === 503);
assert.equal(store.get('partial').batchId, config.partialData.batchId);
assert.deepEqual(store.get('partial').payload, config.partialDraft.payload);
const completed = await executeBatch(store, 'partial', null, request, true);
done(completed, 2);
assert.ok(completed.items.every(item => item.result === 'SUCCESS'));
assert.equal(store.get('partial'), null);
console.log('PASS real MySQL 503 progress stays pending; HTTP resume finishes and clears');
