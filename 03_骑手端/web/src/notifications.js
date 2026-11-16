export function parseSSE(buffer) {
  const events = [];
  let rest = buffer;
  for (;;) {
    const end = /\r?\n\r?\n/.exec(rest);
    if (!end) break;
    const block = rest.slice(0, end.index);
    rest = rest.slice(end.index + end[0].length);
    let event = 'message', id = '', data = [];
    for (const line of block.split(/\r?\n/)) {
      if (!line || line.startsWith(':')) continue;
      const colon = line.indexOf(':');
      const key = colon < 0 ? line : line.slice(0, colon);
      const value = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '');
      if (key === 'event') event = value;
      if (key === 'id') id = value;
      if (key === 'data') data.push(value);
    }
    if (data.length) { try { events.push({ event, id, data: JSON.parse(data.join('\n')) }); } catch { /* Malformed event must not drop later persisted notifications. */ } }
  }
  return { events, rest };
}
export function mergeNotifications(previous, incoming) {
  const byId = new Map(previous.map(item => [String(item.id), item]));
  for (const item of incoming) if (item && item.id != null) byId.set(String(item.id), { ...byId.get(String(item.id)), ...item });
  return [...byId.values()].sort((a,b) => BigInt(a.id) < BigInt(b.id) ? -1 : BigInt(a.id) > BigInt(b.id) ? 1 : 0);
}
