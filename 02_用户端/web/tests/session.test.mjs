import test from 'node:test';
import assert from 'node:assert/strict';
import {discardRejectedSession, TOKEN_KEY, SESSION_KEY} from '../src/session.js';

test('rejected credentials are removed without discarding the original pending order', () => {
  const pendingKey = 'campus_checkout_pending:owner:6';
  const original = JSON.stringify({requestId:'checkout_original_123', payload:{quantity:2}});
  const data = new Map([[TOKEN_KEY,'expired'],[SESSION_KEY,'old-session'],[pendingKey,original]]);
  const storage = {getItem:key=>data.get(key)||null,removeItem:key=>data.delete(key)};
  assert.equal(discardRejectedSession(storage,'expired'),true);
  assert.equal(data.has(TOKEN_KEY),false);
  assert.equal(data.has(SESSION_KEY),false);
  assert.equal(data.get(pendingKey),original);
});

test('late 401 for an old token cannot remove a newly signed-in account', () => {
  const data = new Map([[TOKEN_KEY,'new-token'],[SESSION_KEY,'new-session']]);
  const storage = {getItem:key=>data.get(key)||null,removeItem:key=>data.delete(key)};
  assert.equal(discardRejectedSession(storage,'old-token'),false);
  assert.equal(data.get(TOKEN_KEY),'new-token');
  assert.equal(data.get(SESSION_KEY),'new-session');
});
