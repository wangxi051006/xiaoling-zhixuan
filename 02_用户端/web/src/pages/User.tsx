import { pendingStore } from '../commands'
import { executeCommand, keepPending } from '../pending.js'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, money, newRequestId, shortTime } from '../api'
import { Empty, ErrorBox, FirstOrderBanner, Loading, Notice, PageHeader, StatusBadge, useAsync } from '../components'
import type { Address, Building, Category, OrderDetail, OrderSummary, Product, Quote } from '../types'

type CartLine = { skuId: string; productId: string; name: string; spec: string; priceCents: number; quantity: number; available: number }
const CART_KEY = 'campus_cart'
const PENDING_KEY = 'campus_pending_checkout'

function loadCart(): CartLine[] {
  try { return JSON.parse(localStorage.getItem(CART_KEY) || '[]') } catch { return [] }
}

function useCart() {
  const [cart, setCart] = useState<CartLine[]>(loadCart)
  useEffect(() => { try { localStorage.setItem(CART_KEY, JSON.stringify(cart)) } catch { /* Keep current cart in memory; checkout separately requires a recoverable command. */ } }, [cart])
  function add(product: Product, skuId: string) {
    const sku = product.skus.find((item) => item.id === skuId)
    if (!sku || !sku.active || sku.available <= 0) return false
    setCart((current) => {
      const existing = current.find((line) => line.skuId === sku.id)
      const nextQty = Math.min((existing?.quantity || 0) + 1, sku.available, 99)
      if (existing) return current.map((line) => line.skuId === sku.id ? { ...line, quantity: nextQty, available: sku.available } : line)
      return [...current, { skuId: sku.id, productId: product.id, name: product.name, spec: sku.specName, priceCents: sku.priceCents, quantity: 1, available: sku.available }]
    })
    return true
  }
  function change(skuId: string, delta: number) {
    setCart((current) => current.map((line) => line.skuId === skuId ? { ...line, quantity: Math.max(0, Math.min(line.quantity + delta, line.available, 99)) } : line).filter((line) => line.quantity > 0))
  }
  function remove(skuId: string) { setCart((current) => current.filter((line) => line.skuId !== skuId)) }
  function clear() { setCart([]) }
  const count = cart.reduce((sum, line) => sum + line.quantity, 0)
  const totalCents = cart.reduce((sum, line) => sum + line.priceCents * line.quantity, 0)
  return { cart, add, change, remove, clear, count, totalCents }
}

export function UserHome() {
  const products = useAsync<Product[]>(() => api('/products'))
  const categories = useAsync<Category[]>(() => api('/categories'))
  const buildings = useAsync<Building[]>(() => api('/stores/1/buildings'))
  const addresses = useAsync<Address[]>(() => api('/addresses'))
  const cart = useCart()
  const navigate = useNavigate()
  const [category, setCategory] = useState('')
  const [search, setSearch] = useState('')
  const [addressId, setAddressId] = useState('')
  const [quote, setQuote] = useState<Quote | null>(null)
  const [checkoutOpen, setCheckoutOpen] = useState(false)
  const [addressOpen, setAddressOpen] = useState(false)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<{ text: string; tone?: 'info' | 'danger' | 'good' } | null>(null)
  const [addressForm, setAddressForm] = useState({ buildingId: '', contactName: '', phone: '', detail: '' })
  const [pending, setPending] = useState<any>(() => {
    try { return pendingStore.get(PENDING_KEY) } catch { return null }
  })
  useEffect(() => {
    if (!addressId && addresses.data?.length) {
      const preferred = addresses.data.find((address) => address.isDefault) || addresses.data[0]
      setAddressId(preferred.id)
    }
  }, [addresses.data, addressId])
  const filtered = useMemo(() => (products.data || []).filter((product) => {
    if (category && product.categoryId !== category) return false
    if (search && !`${product.name}${product.description}`.includes(search)) return false
    return true
  }), [products.data, category, search])
  function notify(text: string, tone: 'info' | 'danger' | 'good' = 'info') { setNotice({ text, tone }) }
  async function createAddress() {
    setBusy('address')
    try {
      const created = await api<Address>('/addresses', { method: 'POST', body: addressForm })
      setAddressId(created.id)
      setAddressOpen(false)
      addresses.reload()
      notify('地址已保存', 'good')
    } catch (err: any) { notify(err.message || '保存地址失败', 'danger') } finally { setBusy('') }
  }
  async function makeQuote(): Promise<Quote | null> {
    if (!cart.cart.length) { notify('购物车为空', 'danger'); return null }
    if (!addressId) { notify('请先选择本人地址', 'danger'); return null }
    setBusy('quote')
    try {
      const result = await api<Quote>('/checkout/quotes', {
        method: 'POST',
        body: { storeId: '1', addressId, fulfillmentType: 'DELIVERY', items: cart.cart.map((line) => ({ skuId: line.skuId, quantity: line.quantity })) },
      })
      if (result.lines.some((line) => !line.inStock || !line.active)) {
        notify('部分商品已售罄或下架，请调整后再结算', 'danger')
        setQuote(result)
        return result
      }
      setQuote(result)
      setCheckoutOpen(true)
      return result
    } catch (err: any) { notify(err.message || '报价失败', 'danger'); return null } finally { setBusy('') }
  }
  async function submitCheckout(retry = false) {
    if (busy) return
    setBusy('order')
    try {
      const saved=pendingStore.get(PENDING_KEY)
      if (!retry && saved) {setPending(saved);notify('有一笔结果待确认，请用原请求恢复，不要重新下单','danger');return}
      if (retry && !saved) {setPending(null);notify('没有待恢复请求，请重新获取报价');return}
      if (!saved && !quote) return
      const requestId = saved?.requestId || newRequestId('checkout')
      const payload = saved?.payload || {requestId,storeId:'1',addressId,fulfillmentType:'DELIVERY',quoteId:quote!.quoteId,expectedTotalCents:quote!.totalCents,remark:'',items:cart.cart.map(line=>({skuId:line.skuId,quantity:line.quantity}))}
      const draft={requestId,payload,createdAt:new Date().toISOString()}
      setPending(saved||draft)
      const {result:created,warning}=await executeCommand<any>(pendingStore,PENDING_KEY,draft,command=>api('/orders',{method:'POST',body:command.payload,headers:{'Idempotency-Key':command.requestId}}))
      setPending(null);cart.clear();setCheckoutOpen(false)
      notify('订单已确认'+(warning?'；'+warning:''),'good')
      navigate('/user/orders/'+created.orderId)
    } catch(err:any) {
      try {setPending(pendingStore.get(PENDING_KEY))} catch {}
      notify(keepPending(err)?(err.message||'结果未知')+'；请用原请求恢复，勿重新下单':err.message||'提交失败','danger')
      if(!keepPending(err)){setQuote(null);setCheckoutOpen(false)}
    } finally {setBusy('')}
  }
  async function placeOrder(){return submitCheckout(false)}
  async function retryPending(){return submitCheckout(true)}
  return (
    <>
      {notice && <Notice text={notice.text} tone={notice.tone} onClose={() => setNotice(null)} />}
      {pending && (
        <div className="pending-banner">
          <div><b>有一笔结算结果未知</b><span>先恢复原 requestId 查询/重试，不要重新下单。</span></div>
          <button className="btn btn-ghost" disabled={busy === 'pending'} onClick={retryPending}>{busy === 'pending' ? '恢复中…' : '用原请求恢复'}</button>
        </div>
      )}
      <section className="user-hero">
        <div>
          <span className="eyebrow">CAMPUS RETAIL · 紫荆店</span>
          <h1>今天想吃什么？</h1>
          <p>楼栋地址优先、服务端报价，付款确认后才开始履约。</p>
        </div>
        <div className="address-card">
          <span className="muted">送到</span>
          {addresses.data?.length ? (
            <select value={addressId} onChange={(e) => setAddressId(e.target.value)}>
              {addresses.data.map((address) => <option key={address.id} value={address.id}>{address.buildingName} {address.detail} · {address.contactName}</option>)}
            </select>
          ) : <span>还没有地址</span>}
          <button className="link-btn" onClick={() => setAddressOpen(!addressOpen)}>{addresses.data?.length ? '新增地址' : '添加本人地址'}</button>
        </div>
      </section>
      {addressOpen && (
        <section className="panel inline-panel">
          <h3>新增服务地址</h3>
          <div className="form-grid">
            <label>楼栋<select value={addressForm.buildingId} onChange={(e) => setAddressForm({ ...addressForm, buildingId: e.target.value })}><option value="">选择楼栋</option>{(buildings.data || []).map((building) => <option value={building.id} key={building.id}>{building.name} · 配送 {money(building.deliveryFeeCents)}</option>)}</select></label>
            <label>联系人<input value={addressForm.contactName} onChange={(e) => setAddressForm({ ...addressForm, contactName: e.target.value })} /></label>
            <label>手机号<input value={addressForm.phone} onChange={(e) => setAddressForm({ ...addressForm, phone: e.target.value })} /></label>
            <label>门牌/交付点<input value={addressForm.detail} onChange={(e) => setAddressForm({ ...addressForm, detail: e.target.value })} placeholder="如 6号楼512" /></label>
          </div>
          <button className="btn btn-primary" disabled={busy === 'address'} onClick={createAddress}>保存地址</button>
        </section>
      )}
      <div className="scene-row">
        <button className="scene-chip active">快捷货架</button>
        <button className="scene-chip">晚自习补给</button>
        <button className="scene-chip">宿舍分享</button>
        <span className="scene-note">场景为人工选品入口，不是算法推荐</span>
      </div>
      <div className="toolbar">
        <div className="chip-row">
          <button className={`chip ${!category ? 'active' : ''}`} onClick={() => setCategory('')}>全部</button>
          {(categories.data || []).map((item) => <button className={`chip ${category === item.id ? 'active' : ''}`} key={item.id} onClick={() => setCategory(item.id)}>{item.name}</button>)}
        </div>
        <input className="search" placeholder="搜索零食、饮料…" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      {products.loading ? <Loading /> : products.error ? <ErrorBox message={products.error} onRetry={products.reload} /> : (
        <div className="product-grid">
          {filtered.map((product) => {
            const sku = product.skus.find((item) => item.active) || product.skus[0]
            if (!sku) return null
            return (
              <article className="product-card" key={product.id}>
                <div className="product-emoji">{product.name.includes('茶') || product.name.includes('奶') ? '🧋' : product.name.includes('曲奇') ? '🍪' : '🍟'}</div>
                <div className="product-body">
                  <h3>{product.name}</h3>
                  <p>{product.description || '校园日常补给'}</p>
                  <div className="product-meta"><span>{sku.specName}</span><span>{sku.available > 0 ? `库存 ${sku.available}` : '售罄'}</span></div>
                  <div className="product-foot"><b>{money(sku.priceCents)}</b><button className="btn btn-primary btn-small" disabled={!sku.active || sku.available <= 0} onClick={() => { if (cart.add(product, sku.id)) notify(`已加入 ${product.name}`, 'good') }}>加入</button></div>
                </div>
              </article>
            )
          })}
          {!filtered.length && <Empty text="没有匹配的商品，试试切换分类或清空搜索" />}
        </div>
      )}
      <div className={`cart-bar ${cart.count ? 'has-items' : ''}`}>
        <div><b>购物车</b><span>{cart.count} 件 · {money(cart.totalCents)}</span></div>
        <button className="btn btn-primary" disabled={!cart.count || busy === 'quote'} onClick={makeQuote}>{busy === 'quote' ? '正在核价…' : '去结算'}</button>
      </div>
      {cart.count > 0 && (
        <section className="panel cart-panel">
          <h3>购物车（未占库存）</h3>
          {cart.cart.map((line) => (
            <div className="cart-line" key={line.skuId}>
              <div><b>{line.name}</b><small>{line.spec} · {money(line.priceCents)}</small></div>
              <div className="qty"><button onClick={() => cart.change(line.skuId, -1)}>−</button><span>{line.quantity}</span><button onClick={() => cart.change(line.skuId, 1)}>+</button></div>
              <button className="link-btn danger-text" onClick={() => cart.remove(line.skuId)}>移除</button>
            </div>
          ))}
        </section>
      )}
      {checkoutOpen && quote && (
        <div className="shade" onClick={() => setCheckoutOpen(false)}>
          <div className="modal" onClick={(event) => event.stopPropagation()}>
            <h2>确认订单</h2>
            <p className="muted">服务端报价有效期至 {shortTime(quote.expiresAt)}</p>
            <div className="quote-lines">
              {quote.lines.map((line) => <div className="quote-line" key={line.skuId}><span>{line.productName} {line.specName} × {line.quantity}</span><b>{money(line.goodsCents)}</b></div>)}
            </div>
            <dl className="kv">
              <dt>商品</dt><dd>{money(quote.goodsCents)}</dd>
              <dt>配送费</dt><dd>{money(quote.deliveryCents)}</dd>
              <dt>合计</dt><dd><b>{money(quote.totalCents)}</b></dd>
            </dl>
            <p className="fine">提交时服务端会重新核价、校验地址/楼栋/效期/库存并整单预留；价格或库存变化会要求重新确认。</p>
            <div className="modal-actions">
              <button className="btn btn-ghost" onClick={() => setCheckoutOpen(false)}>再想想</button>
              <button className="btn btn-primary" disabled={busy === 'order'} onClick={placeOrder}>{busy === 'order' ? '提交中…' : '提交订单'}</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

export function UserOrders() {
  const { data, error, loading, reload } = useAsync<any>(() => api('/orders?limit=50'))
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  return (
    <>
      <PageHeader title="我的订单" subtitle="支付弹窗结果不等于已付；以服务端确认和订单时间线为准" actions={<Link className="btn btn-primary" to="/user">继续选购</Link>} />
      <div className="list">
        {(data?.items || []).map((order: OrderSummary) => (
          <Link to={`/user/orders/${order.id}`} className={`order-row ${order.isFirstOrder ? 'is-first' : ''}`} key={order.id}>
            <div><b>#{order.orderNo}</b><StatusBadge status={order.status} /><small>{shortTime(order.createdAt)} · {order.itemCount} 件</small></div>
            <div className="order-row-right"><b>{money(order.totalCents)}</b><span>查看进度 →</span></div>
          </Link>
        ))}
        {!data?.items?.length && <Empty text="还没有订单，去首页逛逛" />}
      </div>
    </>
  )
}

export function UserOrderDetail() {
  const { id } = useParams()
  const { data, error, loading, reload } = useAsync<OrderDetail>(() => api(`/orders/${id}`), [id])
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<{ text: string; tone?: 'info' | 'danger' | 'good' } | null>(null)
  const [attempt, setAttempt] = useState<any>(null)
  const [afterOpen, setAfterOpen] = useState(false)
  const [afterItem, setAfterItem] = useState('')
  const [afterQty, setAfterQty] = useState('1')
  const [afterReason, setAfterReason] = useState('商品破损/少件')
  const [urgeOpen, setUrgeOpen] = useState(false)
  const [urgeReason, setUrgeReason] = useState('请尽快处理，谢谢')
  const [urgeBusy, setUrgeBusy] = useState(false)
  const [urgePending, setUrgePending] = useState<any>(() => {
    try { return pendingStore.get('campus_pending_urge_' + id) } catch { return null }
  })
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  if (!data) return <Empty />
  const order = data
  function notify(text: string, tone: 'info' | 'danger' | 'good' = 'info') { setNotice({ text, tone }) }
  async function startPayment() {
    setBusy('attempt')
    try {
      const channel = (import.meta.env.VITE_PAYMENT_MODE as string) === 'wechat' ? 'WECHAT_H5' : 'SIMULATED'
      const result = await api<any>(`/orders/${order.id}/payment-attempt`, { method: 'POST', body: { channel } })
      setAttempt(result)
      if (result.prepayParams?.h5_url) window.location.href = result.prepayParams.h5_url
      notify(channel === 'SIMULATED' ? '已创建测试支付记录；请明确确认模拟支付' : '已创建支付记录，请在微信中完成', 'info')
    } catch (err: any) { notify(err.message || '创建支付失败', 'danger') } finally { setBusy('') }
  }
  async function confirmSimulated() {
    setBusy('pay')
    try {
      await api(`/test/payments/${order.id}/succeed`, { method: 'POST', body: { confirm: 'SIMULATED_PAYMENT' }, headers: { 'X-Simulated-Payment': 'confirm' } })
      notify('模拟支付确认成功（仅测试环境）', 'good')
      reload()
    } catch (err: any) { notify(err.message || '支付确认失败', 'danger') } finally { setBusy('') }
  }
  async function cancel() {
    if (!window.confirm('确认取消该待支付订单？已付款订单请走售后。')) return
    setBusy('cancel')
    try {
      await api(`/orders/${order.id}/cancel`, { method: 'POST' })
      notify('订单已取消', 'good')
      reload()
    } catch (err: any) { notify(err.message || '取消失败', 'danger') } finally { setBusy('') }
  }
  async function requestAfterSale() {
    if (!afterItem) { notify('请选择售后商品', 'danger'); return }
    setBusy('after')
    try {
      await api(`/orders/${order.id}/after-sales`, {
        method: 'POST',
        body: { requestId: newRequestId('after'), reason: afterReason, items: [{ orderItemId: afterItem, quantity: Number(afterQty) }] },
      })
      notify('售后申请已提交，等待商家处理', 'good')
      setAfterOpen(false)
      reload()
    } catch (err: any) { notify(err.message || '提交售后失败', 'danger') } finally { setBusy('') }
  }
  async function submitUrge(_usePending = false) {
    if(urgeBusy)return
    const key='campus_pending_urge_'+order.id
    setUrgeBusy(true)
    try {
      const saved=pendingStore.get(key)
      const requestId=saved?.requestId||newRequestId('urge')
      const draft=saved||{requestId,body:{requestId,reason:urgeReason.trim()||'请尽快处理，谢谢'}}
      setUrgePending(draft)
      const {warning}=await executeCommand(pendingStore,key,draft,command=>api('/orders/'+order.id+'/urges',{method:'POST',body:command.body,headers:{'Idempotency-Key':command.requestId}}))
      setUrgePending(null);setUrgeOpen(false);notify('催单已提交'+(warning?'；'+warning:''),'good');reload()
    }catch(err:any){try{setUrgePending(pendingStore.get(key))}catch{}notify((err.message||'催单失败')+(keepPending(err)?'；请用原请求恢复，勿重复计次':''),'danger')}finally{setUrgeBusy(false)}
  }
  return (
    <>
      {notice && <Notice text={notice.text} tone={notice.tone} onClose={() => setNotice(null)} />}
      <PageHeader title={`订单 #${order.orderNo}`} subtitle={`创建 ${shortTime(order.createdAt)} · 支付状态 ${order.paymentStatus}`} actions={<Link className="btn btn-ghost" to="/user/orders">返回订单</Link>} />
      {order.isFirstOrder && <FirstOrderBanner count={order.customerOrderCount} ordinal={order.customerOrderOrdinal} />}
      <div className="detail-grid">
        <section className="panel">
          <h3>进度</h3>{order.deliveryTask && <p>当前配送员：{order.deliveryTask.assigneeName || '尚未指派'} · <StatusBadge status={order.deliveryTask.status} /></p>}
          <div className="progress"><StatusBadge status={order.status} /><span>{order.status === 'PENDING_PAYMENT' ? '等待支付' : order.status === 'PAID' ? '商家待接单' : order.status === 'PICKING' ? '商家备货中' : order.status === 'READY' ? '等待配送/取货' : order.status === 'DELIVERING' ? '配送中' : order.status === 'COMPLETED' ? '已完成' : '请查看时间线'}</span></div>
          <ol className="timeline">{(order.events || []).map((event) => <li key={event.id}><b>{event.type}</b><span>{shortTime(event.createdAt)}</span></li>)}</ol>
          {order.status === 'PENDING_PAYMENT' && (
            <div className="card-actions">
              <button className="btn btn-primary" disabled={busy === 'attempt'} onClick={startPayment}>{busy === 'attempt' ? '处理中…' : '去支付'}</button>
              <button className="btn btn-ghost" disabled={busy === 'cancel'} onClick={cancel}>取消订单</button>
            </div>
          )}
          {attempt?.simulated && order.status === 'PENDING_PAYMENT' && <div className="warn-box"><b>仅测试环境模拟支付</b><p>不会产生真实扣款。前端点击“确认模拟支付”后仍需后端 inbox 消费才变已付。</p><button className="btn btn-danger-ghost" disabled={busy === 'pay'} onClick={confirmSimulated}>确认模拟支付（测试）</button></div>}
          {order.urge && (
            <div className="urge-box">
              <div className="section-head"><h3>催单提醒</h3><span className="muted">已催 {order.urge.count}/{order.urge.maxPerOrder} 次 · 待处理 {order.urge.pendingCount}</span></div>
              <p className="fine">最近催单 {shortTime(order.urge.lastAt)} · 下次可催 {shortTime(order.urge.nextAt)}</p>
              <p className={order.urge.allowed ? 'urge-ok' : 'urge-reason'}>{order.urge.reason}</p>
              {urgePending && <button className="btn btn-ghost" disabled={urgeBusy} onClick={() => submitUrge(true)}>结果待确认，用原请求重试</button>}
              {order.urge.allowed && !urgeOpen && <button className="btn btn-primary" disabled={urgeBusy} onClick={() => setUrgeOpen(true)}>催一下</button>}
              {urgeOpen && (
                <div className="urge-form">
                  <input value={urgeReason} maxLength={200} onChange={(event) => setUrgeReason(event.target.value)} placeholder="可补充原因，如：已等 30 分钟" />
                  <button className="btn btn-primary" disabled={urgeBusy} onClick={() => submitUrge(false)}>{urgeBusy ? '提交中…' : '提交催单'}</button>
                </div>
              )}
              {!!order.urge.records.length && (
                <div className="urge-records">
                  {order.urge.records.map((record) => (
                    <div className="urge-record" key={record.id}>
                      <div><b>第 {record.urgeNumber} 次</b><span className="muted"> {shortTime(record.createdAt)}</span><span className={`badge ${record.status === 'HANDLED' ? 'badge-good' : 'badge-warn'}`}>{record.status === 'HANDLED' ? '商家已处理' : '待商家处理'}</span></div>
                      <div className="muted">通知：{record.notificationStatus === 'SENT' ? '已送达站内提醒' : record.notificationStatus === 'PENDING' ? '通知待发送' : record.notificationStatus === 'CANCELLED' ? '订单状态已变化，取消通知' : '通知失败，商家仍可在工作台看到'}</div>
                      {record.reason && <div className="small">原因：{record.reason}</div>}
                      {record.handlerName && <div className="muted small">处理人：{record.handlerName} · {record.handleNote}</div>}
                    </div>
                  ))}
                </div>
              )}
              <p className="fine">催单只提醒商家/当前配送员，不改变金额、库存、支付、下单次数或履约承诺。</p>
            </div>
          )}
        </section>
        <section className="panel">
          <h3>金额与地址</h3>
          <dl className="kv"><dt>商品</dt><dd>{money(order.goodsCents)}</dd><dt>配送</dt><dd>{money(order.deliveryCents)}</dd><dt>合计</dt><dd><b>{money(order.totalCents)}</b></dd><dt>地址</dt><dd>{order.addressText}</dd><dt>备注</dt><dd>{order.remark || '—'}</dd></dl>
          <h3>商品</h3>
          <div className="list">{(order.items || []).map((item) => <div className="list-row" key={item.id}><div><b>{item.productName}</b><small>{item.specName} × {item.quantity}</small></div><span>{money(item.goodsCents)}</span></div>)}</div>
          {['PAID', 'PARTIAL_REFUNDED'].includes(order.paymentStatus) && (
            <div className="after-sale">
              <button className="btn btn-ghost" onClick={() => setAfterOpen(!afterOpen)}>申请售后</button>
              {afterOpen && (
                <div className="after-form">
                  <select value={afterItem} onChange={(event) => setAfterItem(event.target.value)}>
                    <option value="">选择商品</option>
                    {order.items.filter((item) => item.refundedQuantity < item.quantity).map((item) => <option key={item.id} value={item.id}>{item.productName} {item.specName}（可退 {item.quantity - item.refundedQuantity}）</option>)}
                  </select>
                  <input type="number" min={1} value={afterQty} onChange={(event) => setAfterQty(event.target.value)} />
                  <input value={afterReason} onChange={(event) => setAfterReason(event.target.value)} placeholder="售后原因" />
                  <button className="btn btn-primary" disabled={busy === 'after'} onClick={requestAfterSale}>提交</button>
                </div>
              )}
              {!!order.refunds?.length && <div className="refund-list">{order.refunds.map((refund) => <div className="refund-row" key={refund.id}><span>{money(refund.amountCents)} · <StatusBadge status={refund.status} /></span><span className="muted">{refund.reason}</span></div>)}</div>}
            </div>
          )}
        </section>
      </div>
    </>
  )
}

