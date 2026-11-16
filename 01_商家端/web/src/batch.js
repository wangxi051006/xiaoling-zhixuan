// A batch keeps its immutable selection and request key until every item is authoritative.
export function batchFinished(result) {
  return !!result && Array.isArray(result.items) && result.items.length === Number(result.expectedCount) && result.items.every(item => ['SUCCESS', 'FAILED'].includes(item.result)) && Number(result.pendingCount) === 0;
}
export function validSelection(selected, rows, eligible) {
  return selected.filter(saved => rows.some(row => row.id === saved.id && row.version === saved.version && eligible(row)));
}
export async function executeBatch(store, key, draft, request, resume = false, onProgress = () => {}, resumeBody = async () => ({})) {
  store = store.bound ? store.bound() : store;
  const existing = store.get(key);
  const pending = existing || draft;
  if (!pending) return null;
  store.put(key, pending);
  const path = pending.path;
  function record(result) {
    if (!result?.batchId) return result;
    pending.batchId = result.batchId;
    pending.progress = result;
    store.put(key, pending);
    onProgress(result);
    if (batchFinished(result)) store.clear(key);
    return result;
  }
  try {
    if (existing || resume) {
      if (!pending.batchId) {
        try {
          const found = await request(path + '?requestId=' + encodeURIComponent(pending.requestId) + '&action=' + encodeURIComponent(pending.payload.action));
          pending.batchId = found.batchId;
          store.put(key, pending);
        } catch (error) {
          if (error.status !== 404) throw error;
          // Not recorded on the server: replay the same immutable request, never a new key.
          return record(await request(path, { method: 'POST', body: { ...pending.payload, requestId: pending.requestId } }));
        }
      }
      const result = record(await request(path + '/' + pending.batchId));
      if (batchFinished(result) || !resume) return result;
      return record(await request(path + '/' + pending.batchId + '/resume', { method: 'POST', body: await resumeBody(pending, result) }));
    }
    return record(await request(path, { method: 'POST', body: { ...pending.payload, requestId: pending.requestId } }));
  } catch (error) {
    if (error.data?.batchId) record(error.data);
    // Includes 409 request-key conflicts: only server item results may release a pending batch.
    throw error;
  }
}

