import assert from 'node:assert/strict';
import { writeFileSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const base = 'http://127.0.0.1:19090/api/v1';
const output = new URL('./', import.meta.url);
const evidence = [];
let token;
async function call(path, method = 'GET', body) {
  const response = await fetch(base + path, {
    method, headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: 'Bearer ' + token } : {}) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(10000),
  });
  const reply = await response.json();
  if (!path.startsWith('/auth/')) evidence.push({ path, method, httpStatus: response.status, reply });
  if (!response.ok) throw Object.assign(Error(reply.message), { status: response.status, code: reply.code, data: reply.data });
  assert.equal(reply.code, 'OK');
  return reply.data;
}
function store() {
  const entries = new Map();
  return { get: key => entries.get(key) || null, put: (key, value) => entries.set(key, structuredClone(value)), clear: key => entries.delete(key) };
}
const contracts = [
  { batchId: '1', action: 'ACCEPT', requestId: 'http_smoke_a97a07e905974a37be0c356736ed2d9c', version: 2 },
  { batchId: '3', action: 'READY', requestId: 'http_smoke_44a50fa9e0614945ad6bfc19a1abe71f', version: 3 },
];
const path = '/admin/orders/batch-actions';
const draft = contract => ({ path, requestId: contract.requestId, payload: { action: contract.action, items: ['6', '7'].map(orderId => ({ orderId, expectedVersion: contract.version })) } });
const check = result => {
  assert.equal(result.status, 'DONE');
  assert.equal(result.pendingCount, 0);
  assert.equal(result.expectedCount, 2);
  assert.equal(result.completedCount, 2);
  assert.equal(result.items.length, 2);
  assert.ok(result.items.every(item => item.result === 'SUCCESS'));
};
try {
  const login = await call('/auth/staff/login', 'POST', { phone: '13800000001', password: '123123123' });
  token = login.accessToken;
  assert.ok(token);
  for (const contract of contracts) {
    const original = draft(contract);
    const post = await call(path, 'POST', { ...original.payload, requestId: original.requestId });
    const detail = await call(path + '/' + contract.batchId);
    const resume = await call(path + '/' + contract.batchId + '/resume', 'POST', {});
    for (const result of [post, detail, resume]) check(result);
    assert.deepEqual(post.items, resume.items);
    console.log(`PASS real ${contract.action} POST replay / GET / resume: DONE 2/2 pendingCount=0`);
  }
  for (const directory of ['01_商家端', '02_用户端', '03_骑手端']) {
    const { executeBatch, batchFinished } = await import(new URL(`../../${directory}/web/src/batch.js`, output));
    const pending = store();
    let calls = 0;
    const request = async (endpoint, options) => { calls++; return call(endpoint, options?.method || 'GET', options?.body); };
    const accepted = await executeBatch(pending, 'batch', draft(contracts[0]), request);
    assert.equal(batchFinished(accepted), true);
    assert.equal(pending.get('batch'), null);
    const ready = await executeBatch(pending, 'batch', draft(contracts[1]), request);
    assert.equal(batchFinished(ready), true);
    assert.equal(pending.get('batch'), null);
    assert.equal(calls, 2);
    console.log(`PASS ${directory} actual executeBatch + real HTTP: ACCEPT clears pending; next READY sends immediately and clears pending`);
  }
  const { executeBatch, batchFinished } = await import(new URL('../../01_商家端/web/src/batch.js', output));
  const lost = store();
  await assert.rejects(executeBatch(lost, 'lost', draft(contracts[0]), async (endpoint, options) => {
    await call(endpoint, options?.method || 'GET', options?.body);
    throw new TypeError('intentional response loss after real HTTP success');
  }));
  assert.ok(lost.get('lost'));
  const restored = await executeBatch(lost, 'lost', null, (endpoint, options) => call(endpoint, options?.method || 'GET', options?.body), true);
  assert.equal(batchFinished(restored), true);
  assert.equal(lost.get('lost'), null);
  console.log('PASS actual response-loss simulation retains original draft then real lookup/detail recovers and clears');
  const conflict = store();
  const changed = draft(contracts[0]);
  changed.payload.items[0].expectedVersion = 999;
  await assert.rejects(executeBatch(conflict, 'conflict', changed, (endpoint, options) => call(endpoint, options?.method || 'GET', options?.body)), error => error.status === 409);
  assert.equal(conflict.get('conflict').requestId, contracts[0].requestId);
  assert.equal(conflict.get('conflict').payload.items[0].expectedVersion, 999);
  console.log('PASS real HTTP 409 preserves original pending payload and requestId');
  const old = JSON.parse(readFileSync(new URL('../compatibility-20261114-215700/merchant-post-replay.json', output), 'utf8').replace(/^\uFEFF/, ''));
  assert.equal(batchFinished(old.data), false);
  console.log('PASS old captured response still reproduces the defect; current response passes without frontend weakening');
  console.log(`PASS total ${evidence.length} authenticated business HTTP responses checked; only existing DONE batches replayed, no new orders created`);
} finally {
  writeFileSync(new URL('live-contract-responses.json', output), JSON.stringify(evidence, null, 2));
}
