import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

function compile(file) {
  return ts.transpileModule(fs.readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
  }).outputText
}
const input = {}
vm.runInNewContext(compile('../src/admin-input.ts'), { exports: input })
const componentSource = compile('../src/pages/Admin.tsx')
const row = id => ({ id: String(id), orderNo: String(id), status: 'PAID', totalCents: 100 })
const page1 = () => ({ items: Array.from({ length: 50 }, (_, i) => row(i + 1)), nextCursor: 'page2' })
const nodes = node => !node ? [] : Array.isArray(node) ? node.flatMap(nodes) : typeof node === 'object' ? [node, ...nodes(node.props?.children)] : []
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }

// Run real transpiled components with controlled hooks and asynchronous API
// responses. These verify handlers/state, not browser layout or React DOM.
function fixture() {
  let index = 0, states = [], tree = [], data = page1(), handler = async () => ({ items: [row(51)], nextCursor: '' }), notice
  const calls = [], loaders = [], confirmations = []
  const jsx = (type, props) => ({ type, props: props || {} })
  const useState = initial => { const i = index++; if (!(i in states)) states[i] = initial; return [states[i], value => { states[i] = typeof value === 'function' ? value(states[i]) : value }] }
  const react = { useState, useRef(initial) { return useState({ current: initial })[0] }, useEffect() {}, useMemo: fn => fn() }
  const deps = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx, Fragment: 'fragment' },
    'react-router-dom': { Link: 'link', useParams: () => ({}), useNavigate: () => () => {} },
    '../api': { money: String, shortTime: String, api: async (path, options) => { calls.push({ path, options }); return handler(path, options) } },
    '../components': { useAsync(loader) { loaders.push(loader); return { data, loading: false, error: null, reload() {} } } },
    '../BatchPanel': {BatchPanel: 'batch-panel'}, '../admin-input': input, '../commands': {}, '../auth': {},
  }
  const exported = {}
  const browserWindow = { confirm: message => { confirmations.push(message); return true } }
  vm.runInNewContext(componentSource, { exports: exported, require: name => { if (!(name in deps)) throw Error(name); return deps[name] }, URLSearchParams, window: browserWindow })
  return {
    calls, loaders, confirmations, window: browserWindow,
    setData(value) { data = value }, setHandler(value) { handler = value },
    render(name = 'AdminOrders') { index = 0; loaders.length = 0; tree = nodes(exported[name]()); return tree },
    button(label) { const node = tree.find(n => n.type === 'button' && n.props.children === label); assert.ok(node, `missing button: ${label}`); return node.props },
    find(predicate) { const node = tree.find(predicate); assert.ok(node, 'missing field'); return node.props },
  }
}

test('money text becomes exact integer cents and rejects rounding/exponents/invalid values', () => {
  for (const [text, cents] of [['0', 0], ['0.01', 1], ['0.29', 29], ['6.50', 650], ['999999.99', 99999999]]) assert.equal(input.parseMoneyCents(text), cents)
  for (const text of ['', ' ', '-1', '1.005', '1e2', 'Infinity', 'NaN', '.5', '01', '1000000', '1.']) assert.throws(() => input.parseMoneyCents(text), /金额/)
})

test('threshold accepts zero and rejects fractional/negative/overflow values', () => {
  assert.equal(input.parseStockThreshold('0'), 0)
  assert.equal(input.parseStockThreshold('2147483647'), 2147483647)
  for (const text of ['', '-1', '1.1', '1e3', '2147483648']) assert.throws(() => input.parseStockThreshold(text), /阈值/)
})

test('terminal order page removes load-more and never appends duplicate IDs', async () => {
  const f = fixture(); f.setHandler(async () => ({ items: [row(50), row(51)], nextCursor: '' }))
  f.render(); await f.button('加载更多').onClick()
  const tree = f.render()
  assert.equal(tree.filter(n => n.type === 'link').length, 51)
  assert.equal(tree.some(n => n.type === 'button' && n.props.children === '加载更多'), false)
  assert.equal(f.calls.length, 1)
})

test('rapid clicks send one order-page request, including before next render', async () => {
  const pending = deferred(), f = fixture(); f.setHandler(() => pending.promise)
  f.render(); const button = f.button('加载更多')
  const first = button.onClick(); await button.onClick()
  assert.equal(f.calls.length, 1)
  f.render(); assert.equal(f.button('加载中…').disabled, true)
  pending.resolve({ items: [row(51)], nextCursor: '' }); await first
})

test('page failure keeps cursor and displays an error; retry reaches terminal page', async () => {
  const f = fixture(); f.setHandler(async () => { throw Error('network down') })
  f.render(); await f.button('加载更多').onClick()
  let tree = f.render(); assert.ok(tree.some(n => n.props.role === 'alert' && n.props.children === 'network down'))
  f.setHandler(async () => ({ items: [row(51)], nextCursor: '' }))
  await f.button('加载更多').onClick(); tree = f.render()
  assert.equal(new URL(f.calls[0].path, 'http://test').searchParams.get('cursor'), 'page2')
  assert.equal(f.calls[0].path, f.calls[1].path)
  assert.equal(tree.filter(n => n.type === 'link').length, 51)
})

test('changing order filters discards old in-flight results and resets pagination', async () => {
  const f = fixture(), old = deferred(); f.setHandler(() => old.promise)
  f.render(); const work = f.button('加载更多').onClick()
  f.find(n => n.type === 'input' && n.props.placeholder === '顾客姓名').onChange({ target: { value: '新顾客' } })
  f.render(); f.button('查询').onClick()
  f.setData({ items: [row(80)], nextCursor: '' })
  old.resolve({ items: [row(51)], nextCursor: 'old-page3' }); await work
  const tree = f.render()
  assert.deepEqual(tree.filter(n => n.type === 'link').map(n => n.props.to), ['/admin/orders/80'])
  assert.equal(tree.some(n => n.type === 'button' && n.props.children === '加载更多'), false)
})

test('audit controls request next page and reset to page one with all applied filters', async () => {
  const f = fixture(); f.setData({ items: [], total: 21 }); f.render('AuditLogPanel')
  assert.equal(f.button('上一页').disabled, true)
  f.button('下一页').onClick(); f.render('AuditLogPanel')
  await f.loaders[0](); assert.match(f.calls.at(-1).path, /page=2/)
  assert.equal(f.button('下一页').disabled, true)
  f.find(n => n.type === 'input' && n.props.placeholder === '如 UPDATE_PRODUCT').onChange({ target: { value: 'UPDATE_PRODUCT' } })
  f.render('AuditLogPanel'); f.find(n => n.type === 'form').onSubmit({ preventDefault() {} })
  f.render('AuditLogPanel'); await f.loaders[0]()
  const p = new URL(f.calls.at(-1).path, 'http://test').searchParams
  assert.equal(p.get('page'), '1'); assert.equal(p.get('action'), 'UPDATE_PRODUCT')
})

test('actual SKU save rejects invalid money, honors cancel and submits exact cents/threshold on confirm', async () => {
  const f = fixture(), product = { id: '1', name: '薯片', categoryId: '1', description: '', active: true, version: 2, skus: [{ id: '1', specName: '70g', priceCents: 650, active: true, available: 1, lowStockThreshold: 5 }] }
  f.setData([product]); f.render('AdminProducts')
  const save = f.find(n => typeof n.type === 'function' && n.type.name === 'SKUEditor').onSave
  await save(product, product.skus[0], '1.005', true, '3')
  assert.equal(f.calls.length, 0); assert.equal(f.confirmations.length, 0)
  f.window.confirm = () => false
  await save(product, product.skus[0], '6.51', true, '3'); assert.equal(f.calls.length, 0)
  f.window.confirm = message => { assert.match(message, /650 → 651/); return true }
  await save(product, product.skus[0], '6.51', true, '3')
  assert.equal(f.calls.length, 1)
  const body = f.calls[0].options.body
  assert.equal(body.expectedVersion, 2); assert.equal(body.skus[0].priceCents, 651); assert.equal(body.skus[0].lowStockThreshold, 3)
})

