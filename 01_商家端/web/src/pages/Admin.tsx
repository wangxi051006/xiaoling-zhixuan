import { BatchPanel } from '../BatchPanel'
import { deliveryCommand, pendingStore } from '../commands'
import type { CourierCandidate } from '../types'
import { useEffect, useMemo, useRef, useState } from 'react'
import { parseMoneyCents, parseStockThreshold } from '../admin-input'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, money, newRequestId, shortTime } from '../api'
import { useAuth } from '../auth'
import { CustomerCount, Empty, ErrorBox, FirstOrderBanner, Loading, Notice, PageHeader, StatusBadge, useAsync } from '../components'
import type { Dashboard, OrderDetail, OrderSummary, Product, ProductSKU, Refund } from '../types'

function useNotice() {
  const [notice, setNotice] = useState<{ text: string; tone?: 'info' | 'danger' | 'good' } | null>(null)
  const node = notice ? <Notice text={notice.text} tone={notice.tone} onClose={() => setNotice(null)} /> : null
  return { setNotice, node }
}

export function AdminWorkbench() {
  const { data, error, loading, reload } = useAsync<any>(() => api('/admin/workbench'))
  const [busy, setBusy] = useState('')
  const { setNotice, node } = useNotice()
  async function act(order: OrderSummary, action: string) {
    if (!window.confirm(`确认${action === 'ACCEPT' ? '接单备货' : '出餐'}：订单 ${order.orderNo}？`)) return
    setBusy(order.id + action)
    try {
      const detail = await api<OrderDetail>(`/admin/orders/${order.id}`)
      await api(`/admin/orders/${order.id}/actions`, { method: 'POST', body: { action, expectedVersion: order.version } })
      setNotice({ text: `订单 ${order.orderNo} 已更新`, tone: 'good' })
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '操作失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  const counts = data?.counts || {}
  return (
    <>
      {node}
      <PageHeader title="履约工作台" subtitle={`门店快照 ${shortTime(data?.snapshotAt)} · 队列仅展示已确认付款的下一动作`} actions={<Link className="btn" to="/admin/orders">全部订单</Link>} />
      <BatchPanel rows={data?.queue || []} kind="admin" onDone={reload} />
      <div className="kpi-grid">
        {[
          ['待接单', counts.PAID || 0, '已付款，等待商家接单'],
          ['备货中', counts.PICKING || 0, '需要取货、打包'],
          ['待出餐', counts.READY || 0, '等待派送或自取'],
          ['配送中', counts.DELIVERING || 0, '配送员正在路上'],
          ['待处理催单', data?.pendingUrges || 0, '站内提醒未处理的订单催单'],
          ['支付异常', counts.PAYMENT_EXCEPTION || 0, '需要人工核实，不自动释放库存'],
        ].map(([label, value, hint]) => (
          <div className="kpi" key={label as string}>
            <span>{label}</span><b>{value as number}</b><small>{hint as string}</small>
          </div>
        ))}
      </div>
      <div className="section-head"><h2>下一批要处理</h2><span className="muted">首单标签来自创建时不可变快照</span></div>
      <div className="order-grid">
        {(data?.queue || []).map((order: OrderSummary) => (
          <article className={`order-card ${order.isFirstOrder ? 'is-first' : ''}`} key={order.id}>
            <div className="order-top">
              <div><b className="order-no">#{order.orderNo}</b><StatusBadge status={order.status} />{order.pendingUrgeCount > 0 && <span className="badge badge-danger">催单 {order.pendingUrgeCount}</span>}</div>
              <span className="muted">#{order.id}</span>
            </div>
            {order.isFirstOrder && <FirstOrderBanner count={order.customerOrderCount} ordinal={order.customerOrderOrdinal} />}
            {order.urgeCount > 0 && <div className="urge-inline">累计催单 {order.urgeCount} 次{order.lastUrgeAt ? ` · 最近 ${shortTime(order.lastUrgeAt)}` : ''}</div>}
            <div className="order-main">
              <div><span className="muted">顾客</span><b>{order.buyerName}</b></div>
              <div><span className="muted">件数</span><b>{order.itemCount}</b></div>
              <div><span className="muted">应付</span><b>{money(order.totalCents)}</b></div>
              <div><span className="muted">下单</span><b>{shortTime(order.createdAt)}</b></div>
            </div>
            {!order.isFirstOrder && <CustomerCount count={order.customerOrderCount} ordinal={order.customerOrderOrdinal} first={false} />}
            <p className="address-line">{order.addressText || '自取 / 无地址'}</p>
            <div className="card-actions">
              <Link className="btn btn-ghost" to={`/admin/orders/${order.id}`}>详情</Link>
              {order.status === 'PAID' && <button className="btn btn-primary" disabled={!!busy} onClick={() => act(order, 'ACCEPT')}>{busy ? '处理中…' : '接单备货'}</button>}
              {order.status === 'PICKING' && <button className="btn btn-primary" disabled={!!busy} onClick={() => act(order, 'READY')}>标记待出餐</button>}
              {order.status === 'READY' && <Link className="btn" to={`/admin/orders/${order.id}`}>去派送</Link>}
              {order.status === 'PAYMENT_EXCEPTION' && <span className="danger-text">人工核实支付，不可自动释放</span>}
            </div>
          </article>
        ))}
        {!data?.queue?.length && <Empty text="当前没有待处理订单" />}
      </div>
    </>
  )
}

export function AdminOrders() {
  const [status, setStatus] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [buyer, setBuyer] = useState('')
  const [keyword, setKeyword] = useState('')
  const [query, setQuery] = useState({ status: '', from: '', to: '', buyer: '', keyword: '' })
  const [extra, setExtra] = useState<any[]>([])
  const [moreCursor, setMoreCursor] = useState<string | null>(null)
  const [moreBusy, setMoreBusy] = useState(false)
  const [moreError, setMoreError] = useState('')
  const pageRequest = useRef({ generation: 0, busy: false })
  useEffect(() => () => { pageRequest.current.generation++ }, [])
  const params = new URLSearchParams({ limit: '50' })
  Object.entries(query).forEach(([key, value]) => { if (value) params.set(key, String(value)) })
  const { data, error, loading, reload } = useAsync<any>(() => api(`/admin/orders?${params.toString()}`), [query])
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  const rows = Array.from(new Map<string, OrderSummary>([...(data?.items || []), ...extra].map(order => [order.id, order])).values())
  const nextCursor = moreCursor ?? data?.nextCursor ?? ''
  function applyQuery(next: typeof query) {
    pageRequest.current.generation++
    pageRequest.current.busy = false
    setMoreBusy(false)
    setMoreError('')
    setExtra([])
    setMoreCursor(null)
    setQuery(next)
  }
  async function loadMore() {
    if (!nextCursor || pageRequest.current.busy) return
    const generation = pageRequest.current.generation
    pageRequest.current.busy = true
    setMoreBusy(true)
    setMoreError('')
    const p = new URLSearchParams(params)
    p.set('cursor', nextCursor)
    try {
      const next = await api<any>(`/admin/orders?${p.toString()}`)
      if (pageRequest.current.generation !== generation) return
      setExtra(previous => [...previous, ...(next.items || [])])
      setMoreCursor(next.nextCursor || '')
    } catch (err: any) {
      if (pageRequest.current.generation === generation) setMoreError(err.message || '加载失败，请重试')
    } finally {
      if (pageRequest.current.generation === generation) {
        pageRequest.current.busy = false
        setMoreBusy(false)
      }
    }
  }
  return (
    <>
      <PageHeader title="订单工作区" subtitle="服务端组合筛选（状态/日期/顾客/商品关键词），游标分页；日期按 Asia/Shanghai 解释" />
      <div className="toolbar">
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">全部状态</option>
          <option value="PENDING_PAYMENT">待付款</option>
          <option value="PAID">已付款待接单</option>
          <option value="PICKING">备货中</option>
          <option value="READY">待出餐</option>
          <option value="DELIVERING">配送中</option>
          <option value="COMPLETED">已完成</option>
          <option value="CANCELLED">已取消</option>
          <option value="CLOSED">已关闭</option>
          <option value="PAYMENT_EXCEPTION">支付异常</option>
        </select>
        <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="开始日期" />
        <input type="date" value={to} onChange={(e) => setTo(e.target.value)} aria-label="结束日期" />
        <input value={buyer} onChange={(e) => setBuyer(e.target.value)} placeholder="顾客姓名" maxLength={40} />
        <input value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="商品关键词" maxLength={40} />
        <button className="btn btn-primary" onClick={() => applyQuery({ status, from, to, buyer, keyword })}>查询</button>
        <button className="btn btn-ghost" onClick={() => { setStatus(''); setFrom(''); setTo(''); setBuyer(''); setKeyword(''); applyQuery({ status: '', from: '', to: '', buyer: '', keyword: '' }) }}>清空</button>
      </div>
      <BatchPanel rows={rows} kind="admin" onDone={reload} />
      <div className="table-wrap">
        <table>
          <thead><tr><th>订单</th><th>顾客</th><th>累计 / 本单</th><th>催单</th><th>状态</th><th>件数</th><th>金额</th><th>下单时间</th><th /></tr></thead>
          <tbody>
            {rows.map((order: OrderSummary) => (
              <tr key={order.id} className={order.isFirstOrder ? 'row-first' : ''}>
                <td><b>#{order.orderNo}</b>{order.isDemo && <span className="tiny demo-tag">演示</span>}</td>
                <td>{order.buyerName}</td>
                <td>{order.customerOrderCount} 次 / 第 {order.customerOrderOrdinal} 次{order.isFirstOrder && <b className="first-text"> 首单</b>}</td>
                <td>{order.pendingUrgeCount > 0 ? <b className="danger-text">待处理 {order.pendingUrgeCount}</b> : order.urgeCount > 0 ? `已处理 ${order.urgeCount}` : '—'}</td>
                <td><StatusBadge status={order.status} /></td>
                <td>{order.itemCount}</td>
                <td>{money(order.totalCents)}</td>
                <td>{shortTime(order.createdAt)}</td>
                <td><Link to={`/admin/orders/${order.id}`}>查看</Link></td>
              </tr>
            ))}
          </tbody>
        </table>
        {!rows.length && <Empty text="没有符合条件的订单" />}
        <div className="toolbar">
          <span className="muted tiny">已显示 {rows.length} 条{nextCursor ? '（还有更多）' : ''}</span>
          {nextCursor && <button className="btn" disabled={moreBusy} onClick={loadMore}>{moreBusy ? '加载中…' : '加载更多'}</button>}
        </div>
        {moreError && <div role="alert" className="danger-text">{moreError}</div>}
      </div>
    </>
  )
}

export function AdminOrderDetail() {
  const { id } = useParams()
  const { session } = useAuth()
  const canHandleUrge = !!session?.user.roles.some((role) => ['store_admin', 'system_admin', 'dispatcher', 'support'].includes(role))
  const { data, error, loading, reload } = useAsync<OrderDetail>(() => api(`/admin/orders/${id}`), [id])
  const { data: staff } = useAsync<CourierCandidate[]>(() => api('/admin/courier-candidates'), [])
  const [busy, setBusy] = useState('')
  const [refundOpen, setRefundOpen] = useState(false)
  const [reason, setReason] = useState('顾客申请售后')
  const [refundQty, setRefundQty] = useState<Record<string, number>>({})
  const [urgeNotes, setUrgeNotes] = useState<Record<string, string>>({})
  const [assignee, setAssignee] = useState('')
  const [resumeNote, setResumeNote] = useState('')
  const { setNotice, node } = useNotice()
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  if (!data) return <Empty text="订单不存在" />
  const order = data
  async function action(name: string) {
    if (name === 'CLOSE' && !window.confirm(`确认关闭订单 ${order.orderNo}？仅在服务端确认可以关闭支付时释放预留库存。`)) return
    setBusy(name)
    try {
      await api(`/admin/orders/${order.id}/actions`, { method: 'POST', body: { action: name, expectedVersion: order.version } })
      setNotice({ text: '状态已更新', tone: 'good' })
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '操作失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  async function assign() {
    const task = order.deliveryTask
    if (task && ['assign','reassign'].some(action=>pendingStore.get('campus_command_/admin/delivery-tasks/'+task.id+'/'+action))) {setNotice({text:'有派单结果待确认，请先恢复原派单',tone:'danger'});return}
    if (!assignee || !task) return
    setBusy('assign')
    try {
      const reassign = task.status !== 'OPEN'
      await deliveryCommand(`/admin/delivery-tasks/${task.id}/${reassign ? 'reassign' : 'assign'}`, { assigneeId: assignee, expectedVersion: task.version, reason: reassign ? '管理端改派' : '' })
      setNotice({ text: '已指派配送员', tone: 'good' })
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '指派失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  async function resumeDelivery() {
    const task = order.deliveryTask
    if (!task || !resumeNote.trim()) return
    setBusy('resume')
    try {
      await deliveryCommand(`/admin/delivery-tasks/${task.id}/resume`, { expectedVersion: task.version, note: resumeNote.trim() })
      setNotice({ text: task.orderStatus === 'DELIVERING' ? '已恢复为配送中，可继续签收' : '已恢复为待取货', tone: 'good' })
      setResumeNote('')
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '恢复配送失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  async function createRefund() {
    const items = Object.entries(refundQty).filter(([, qty]) => Number(qty) > 0).map(([orderItemId, qty]) => ({ orderItemId, quantity: Number(qty) }))
    if (!items.length) { setNotice({ text: '请选择退款数量', tone: 'danger' }); return }
    setBusy('refund')
    try {
      await api('/admin/refunds', { method: 'POST', body: { requestId: newRequestId('refund'), orderId: order.id, reason, items } })
      setNotice({ text: '退款申请已创建，资金与回库分离', tone: 'good' })
      setRefundOpen(false)
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '创建退款失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  async function succeedRefund(refund: Refund) {
    setBusy('refund-' + refund.id)
    try {
      await api(`/test/refunds/${refund.id}/succeed`, { method: 'POST', body: { confirm: 'SIMULATED_REFUND' } })
      setNotice({ text: '模拟退款成功（仅测试环境）', tone: 'good' })
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '退款确认失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  async function handleUrge(record: any) {
    const note = urgeNotes[record.id]?.trim() || '已查看并安排处理'
    setBusy('urge-' + record.id)
    try {
      await api(`/admin/urges/${record.id}/handle`, { method: 'POST', body: { expectedVersion: record.version, note } })
      setNotice({ text: '催单已标记处理，未改变订单状态', tone: 'good' })
      reload()
    } catch (err: any) {
      setNotice({ text: err.message || '处理催单失败', tone: 'danger' })
    } finally {
      setBusy('')
    }
  }
  const couriers = staff || []
  return (
    <>
      {node}
      <PageHeader
        title={`订单 #${order.orderNo}`}
        subtitle={`创建 ${shortTime(order.createdAt)} · 版本 v${order.version} · ${order.isDemo ? '演示种子' : '真实数据'}`}
        actions={<><Link className="btn btn-ghost" to="/admin/orders">返回列表</Link>{order.status === 'PAID' && <button className="btn btn-primary" onClick={() => action('ACCEPT')} disabled={!!busy}>接单备货</button>}{order.status === 'PICKING' && <button className="btn btn-primary" onClick={() => action('READY')} disabled={!!busy}>待出餐</button>}</>}
      />
      {order.isFirstOrder && <FirstOrderBanner count={order.customerOrderCount} ordinal={order.customerOrderOrdinal} />}
      <div className="detail-grid">
        <section className="panel">
          <h3>顾客与交付</h3>
          <dl className="kv">
            <dt>顾客</dt><dd>{order.buyerName}</dd>
            <dt>累计下单</dt><dd><b>{order.customerOrderCount}</b> 次（含当前单，取消/关闭不减）</dd>
            <dt>本单序号</dt><dd><b>第 {order.customerOrderOrdinal} 次</b>（创建时快照，非实时回推）</dd>
            <dt>地址</dt><dd>{order.addressText || '自取'}</dd>
            <dt>备注</dt><dd>{order.remark || '—'}</dd>
            <dt>支付状态</dt><dd><StatusBadge status={order.paymentStatus} /> {order.paymentException && <b className="danger-text"> 资金异常</b>}</dd>
            <dt>配送状态</dt><dd><StatusBadge status={order.deliveryStatus} /></dd>
          </dl>
          {order.deliveryTask && (
            <div className="subpanel">
              <h4>配送任务</h4><PendingDispatch taskId={order.deliveryTask.id} onDone={reload}/>
              <p>状态 <StatusBadge status={order.deliveryTask.status} /> · 版本 v{order.deliveryTask.version}</p>
              <p className="muted">当前配送员：{order.deliveryTask.assigneeName || '未指派'}</p>
              {['OPEN', 'ACCEPTED'].includes(order.deliveryTask.status) && (
                <div className="inline-form">
                  <select value={assignee} onChange={(e) => setAssignee(e.target.value)}>
                    <option value="">选择配送员</option>
                    {couriers.map((member: CourierCandidate) => <option key={member.id} value={member.id}>{member.nickname}</option>)}
                  </select>
                  <button className="btn" disabled={!assignee || !!busy} onClick={assign}>指派</button>
                </div>
              )}
              {order.deliveryTask.status === 'EXCEPTION' && (
                <div className="inline-form">
                  <input value={resumeNote} onChange={(e) => setResumeNote(e.target.value)} placeholder="恢复说明（必填）" />
                  <button className="btn" disabled={!resumeNote.trim() || !!busy} onClick={resumeDelivery}>恢复配送</button>
                </div>
              )}
            </div>
          )}
        </section>
        <section className="panel">
          <h3>金额</h3>
          <dl className="kv">
            <dt>商品</dt><dd>{money(order.goodsCents)}</dd>
            <dt>配送费</dt><dd>{money(order.deliveryCents)}</dd>
            <dt>合计</dt><dd><b>{money(order.totalCents)}</b></dd>
            <dt>支付渠道</dt><dd>{order.payment?.channel || '—'} {order.payment?.simulated && <span className="warn-tag">模拟</span>}</dd>
          </dl>
          <h3>状态时间线</h3>
          <ol className="timeline">
            {order.events.map((event) => <li key={event.id}><b>{event.type}</b><span>{shortTime(event.createdAt)} · {event.actorType}</span></li>)}
          </ol>
        </section>
      </div>
      {order.urge && order.urge.count > 0 && (
        <section className="panel urge-admin">
          <div className="section-head">
            <h3>催单记录 <span className="badge badge-danger">待处理 {order.urge.pendingCount}</span></h3>
            <span className="muted">累计 {order.urge.count}/{order.urge.maxPerOrder} 次 · 最近 {shortTime(order.urge.lastAt)} · 冷却 {order.urge.cooldownSeconds} 秒</span>
          </div>
          <p className="fine">处理催单只记录处理人/时间/说明，不改变订单的备货、发货、完成状态，也不影响金额、库存、下单次数和销售报表。</p>
          <div className="urge-records">
            {order.urge.records.map((record) => (
              <div className="urge-record" key={record.id}>
                <div><b>第 {record.urgeNumber} 次</b><span className="muted"> {shortTime(record.createdAt)}</span><span className={`badge ${record.status === 'HANDLED' ? 'badge-good' : 'badge-warn'}`}>{record.status === 'HANDLED' ? '已处理' : '待处理'}</span></div>
                <div className="small">原因：{record.reason || '—'}</div>
                <div className="muted small">通知：{record.notificationStatus} · 范围：{record.notificationTarget}</div>
                {record.status === 'HANDLED'
                  ? <div className="muted small">处理人：{record.handlerName || record.handledBy} · {record.handleNote}</div>
                  : canHandleUrge && <div className="form-row"><label>处理说明<input maxLength={255} value={urgeNotes[record.id] || ''} placeholder="已查看并安排处理" onChange={event => setUrgeNotes({...urgeNotes, [record.id]: event.target.value})} /></label><button className="btn btn-small" disabled={!!busy} onClick={() => handleUrge(record)}>标记已处理</button></div>}
              </div>
            ))}
          </div>
        </section>
      )}
      <section className="panel">
        <div className="section-head"><h3>商品明细</h3><button className="btn btn-danger-ghost" onClick={() => setRefundOpen(!refundOpen)}>申请退款</button></div>
        <div className="table-wrap">
          <table>
            <thead><tr><th>商品</th><th>单价</th><th>数量</th><th>小计</th><th>已退</th>{refundOpen && <th>本次退款</th>}</tr></thead>
            <tbody>
              {order.items.map((item) => (
                <tr key={item.id}>
                  <td>{item.productName}<small className="muted"> {item.specName}</small></td>
                  <td>{money(item.unitPriceCents)}</td>
                  <td>{item.quantity}</td>
                  <td>{money(item.goodsCents)}</td>
                  <td>{item.refundedQuantity}</td>
                  {refundOpen && <td><input className="qty-input" type="number" min={0} max={item.quantity - item.refundedQuantity} value={refundQty[item.id] || 0} onChange={(e) => setRefundQty({ ...refundQty, [item.id]: Number(e.target.value) })} /></td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {refundOpen && (
          <div className="form-row">
            <label>退款原因<input value={reason} onChange={(e) => setReason(e.target.value)} /></label>
            <button className="btn btn-primary" disabled={!!busy} onClick={createRefund}>提交退款申请</button>
          </div>
        )}
        {!!order.refunds?.length && (
          <div className="refund-list">
            {order.refunds.map((refund) => (
              <div className="refund-row" key={refund.id}>
                <span><b>{money(refund.amountCents)}</b> · <StatusBadge status={refund.status} /></span>
                <span className="muted">{refund.reason}</span>
                {refund.status !== 'SUCCESS' && <button className="btn btn-ghost" disabled={!!busy} onClick={() => succeedRefund(refund)}>模拟退款成功</button>}
                {refund.status === 'SUCCESS' && <RefundRestock refundId={refund.id} />}
              </div>
            ))}
          </div>
        )}
      </section>
    </>
  )
}



function PendingDispatch({taskId,onDone}:{taskId:string;onDone:()=>void}){
 const [message,setMessage]=useState(''),[busy,setBusy]=useState(false)
 const paths=['assign','reassign','resume'].map(action=>'/admin/delivery-tasks/'+taskId+'/'+action).filter(path=>{try{return !!pendingStore.get('campus_command_'+path)}catch{return false}})
 if(!paths.length)return null
 return <div className="warn-box"><p>派单结果待确认，请先恢复原请求（使用原骑手与版本）。</p>{paths.map(path=><button key={path} className="btn" disabled={busy} onClick={async()=>{setBusy(true);try{const {warning}=await deliveryCommand(path,{});setMessage('原派单已确认'+warning);onDone()}catch(err:any){setMessage(err.message)}finally{setBusy(false)}}}>恢复原派单</button>)}{message&&<p>{message}</p>}</div>
}

function RefundRestock({refundId}:{refundId:string}) {
 const refund=useAsync<Refund>(()=>api('/admin/refunds/'+refundId),[refundId])
 const inventory=useAsync<Array<{id:string;skuId:string;batchNo:string;expiryDate:string;productName:string;specName:string}>>(()=>api('/admin/inventory'),[])
 const [itemId,setItemId]=useState(''),[lotId,setLotId]=useState(''),[quantity,setQuantity]=useState('1'),[busy,setBusy]=useState(false),[message,setMessage]=useState('')
 const item=refund.data?.items?.find(x=>x.id===itemId)
 async function submit(){if(busy)return;setBusy(true);setMessage('');try{const {warning}=await deliveryCommand('/admin/refunds/'+refundId+'/restock',{refundItemId:itemId,lotId,quantity:Number(quantity)});setMessage('本次回库已确认，余量以刷新后的累计数为准。'+warning);refund.reload();inventory.reload()}catch(err:any){setMessage((err.message||'回库失败')+'；结果未知时再次点击将使用原请求，不改变数量。')}finally{setBusy(false)}}
 return <div className="subpanel"><h4>实物质检与分次回库</h4><p className="fine">资金退款不自动回库。每次确认使用独立请求键；结果未知保留原数量和批次，先恢复后再进行下一次回库。</p>{refund.error||inventory.error?<p className="danger-text">{refund.error||inventory.error}</p>:<><label>退款商品<select value={itemId} onChange={e=>{setItemId(e.target.value);setLotId('')}}><option value="">选择退款商品</option>{refund.data?.items?.map(row=><option key={row.id} value={row.id}>{row.productName} / {row.specName} · 可回 {row.quantity-row.restockedQuantity} 件（已回 {row.restockedQuantity}）</option>)}</select></label><label>同SKU批次<select value={lotId} onChange={e=>setLotId(e.target.value)}><option value="">选择质检通过的未过期批次</option>{inventory.data?.filter(lot=>lot.skuId===item?.skuId).map(lot=><option key={lot.id} value={lot.id}>{lot.batchNo} · 效期 {lot.expiryDate}</option>)}</select></label><label>本次数量<input type="number" min="1" max={item?item.quantity-item.restockedQuantity:1} value={quantity} onChange={e=>setQuantity(e.target.value)}/></label><button className="btn" disabled={busy||!itemId||!lotId||!Number.isInteger(Number(quantity))||Number(quantity)<1} onClick={submit}>{busy?'提交中…':'确认本次回库 / 恢复原请求'}</button></>}{message&&<p role="status">{message}</p>}</div>
}

export function Reports() {
  const [date, setDate] = useState(new Date().toLocaleDateString('sv-SE'))
  const { data, error, loading, reload } = useAsync<Dashboard>(() => api(`/admin/reports/dashboard?date=${date}`), [date])
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  if (!data) return <Empty />
  const maxHour = Math.max(1, ...data.hourlySales.map((hour) => hour.salesCents))
  return (
    <>
      <PageHeader title="经营分析驾驶舱" subtitle={`Asia/Shanghai 自然日 · 数据更新 ${shortTime(data.asOf)} · ${data.demoIncluded ? '包含演示种子（已标识）' : '仅真实数据'}`} actions={<input type="date" value={date} onChange={(e) => setDate(e.target.value)} />} />

      <div className="kpi-grid">
        <div className="kpi"><span>已付订单</span><b>{data.paidOrders}</b><small>新用户 {data.newUserOrders} / 老用户 {data.returningOrders}</small></div>
        <div className="kpi"><span>商品销售额</span><b>{money(data.goodsSalesCents)}</b><small>不含配送费</small></div>
        <div className="kpi"><span>商品退款额</span><b>{money(data.goodsRefundCents)}</b><small>仅成功退款回溯原销售日</small></div>
        <div className="kpi"><span>商品净额</span><b>{money(data.goodsNetCents)}</b><small>销售减成功退款</small></div>
        <div className="kpi"><span>缺货预警</span><b>{data.lowStockCount}</b><small>在售 SKU 可用库存 ≤ 各自预警阈值</small></div>
      </div>
      <div className="two-col">
        <section className="panel">
          <h3>新老用户订单结构</h3>
          <div className="bar-stack">
            <div className="bar-row"><span>新用户</span><div className="bar-track"><i style={{ width: `${data.paidOrders ? (data.newUserOrders / data.paidOrders) * 100 : 0}%` }} /></div><b>{data.newUserOrders}</b></div>
            <div className="bar-row"><span>老用户</span><div className="bar-track"><i className="warm" style={{ width: `${data.paidOrders ? (data.returningOrders / data.paidOrders) * 100 : 0}%` }} /></div><b>{data.returningOrders}</b></div>
          </div>
          <p className="fine">新用户 = 本单是创建时首单；不是注册时间。历史首单标签不会因后续下单消失。</p>
          <h3>时段销售趋势</h3>
          <div className="hour-chart">
            {data.hourlySales.map((hour) => (
              <div className={`hour-col ${hour.future ? 'future' : ''}`} key={hour.hour} title={`${hour.hour}:00 · ${money(hour.salesCents)}`}>
                <i style={{ height: `${Math.max(2, (hour.salesCents / maxHour) * 100)}%` }} />
                <span>{hour.hour}</span>
              </div>
            ))}
          </div>
          <p className="fine">未到时段标记为“未到时段”，不写成零销量。时段商品金额合计与下方日报销售额一致。</p>
        </section>
        <section className="panel">
          <h3>补货关注</h3>
          <div className="list">
            {data.replenishment.map((item) => (
              <div className="list-row" key={item.skuId}>
                <div><b>{item.productName}</b><small>{item.specName} · 可用 {item.availableQty} · 近7天 {item.sold7Days}</small></div>
                <span className="pill">建议补 {item.suggestedQty}</span>
              </div>
            ))}
            {!data.replenishment.length && <Empty text="暂无补货建议" />}
          </div>
          <p className="fine">规则建议，必须人工复核；确认建议不等于实际入库。</p>
          <Link className="btn btn-ghost" to="/admin/products">去库存与入库</Link>
        </section>
      </div>
      <section className="panel">
        <div className="section-head"><h3>每日每种零食销售</h3><span className="muted">支付成功归属 Shanghai 日；成功退款回溯原销售日 · asOf {shortTime(data.dailySku.asOf)}</span></div>
        <div className="table-wrap">
          <table>
            <thead><tr><th>商品 / 规格</th><th>已付数量</th><th>退款成功</th><th>净销量</th><th>商品销售额</th><th>商品退款额</th><th>商品净额</th><th>已付订单</th></tr></thead>
            <tbody>
              {data.dailySku.rows.map((row) => (
                <tr key={row.skuId}>
                  <td><b>{row.productName}</b><small className="muted"> {row.specName}</small></td>
                  <td>{row.paidQuantity}</td><td>{row.successfulRefundQuantity}</td><td><b>{row.netQuantity}</b></td>
                  <td>{money(row.goodsSalesCents)}</td><td>{money(row.goodsRefundCents)}</td><td><b>{money(row.goodsNetCents)}</b></td><td>{row.paidOrders}</td>
                </tr>
              ))}
              <tr className="total-row">
                <td>合计</td><td>{data.dailySku.totals.paidQuantity}</td><td>{data.dailySku.totals.successfulRefundQuantity}</td><td>{data.dailySku.totals.netQuantity}</td>
                <td>{money(data.dailySku.totals.goodsSalesCents)}</td><td>{money(data.dailySku.totals.goodsRefundCents)}</td><td>{money(data.dailySku.totals.goodsNetCents)}</td><td>{data.dailySku.totals.paidOrders}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p className="fine">配送费另列：{money(data.dailySku.deliveryCents)}，不计入商品销售额/净额。退款发生日现金流水如下（与销售归属日分开）。</p>
        <div className="cash-refunds">
          {data.dailySku.cashRefunds.map((cash) => <span key={cash.refundId} className="pill">#{cash.orderNo} · {money(cash.amountCents)} · {shortTime(cash.occurredAt)}</span>)}
          {!data.dailySku.cashRefunds.length && <span className="muted">当日无现金退款流水</span>}
        </div>
      </section>
    </>
  )
}

export function AdminProducts() {
  const products = useAsync<Product[]>(() => api('/admin/products'))
  const inventory = useAsync<any[]>(() => api('/admin/inventory'))
  const categories = useAsync<any[]>(() => api('/categories'))
  const [busy, setBusy] = useState('')
  const [form, setForm] = useState({ name: '', categoryId: '', specName: '标准', price: '', threshold: '5', active: true })
  const [receipt, setReceipt] = useState({ skuId: '', batchNo: 'B' + new Date().toISOString().slice(0, 10).replace(/-/g, ''), expiryDate: '', quantity: '', unitCost: '' })
  const [waste, setWaste] = useState({ lotId: '', quantity: '', reason: '到期/破损' })
  const { setNotice, node } = useNotice()
  async function createProduct() {
    setBusy('create')
    try {
      await api('/admin/products', { method: 'POST', body: { name: form.name, categoryId: form.categoryId, description: '', active: form.active, skus: [{ specName: form.specName, priceCents: parseMoneyCents(form.price), lowStockThreshold: parseStockThreshold(form.threshold), active: true }] } })
      setNotice({ text: '商品已创建', tone: 'good' })
      products.reload()
      inventory.reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  async function updateSKU(product: Product, sku: ProductSKU, price: string, active: boolean, threshold: string) {
    setBusy(sku.id)
    try {
      const priceCents = parseMoneyCents(price)
      const lowStockThreshold = parseStockThreshold(threshold)
      if (!window.confirm(`确认修改 ${product.name} / ${sku.specName}？\n价格：${money(sku.priceCents)} → ${money(priceCents)}\n预警阈值：${sku.lowStockThreshold} → ${lowStockThreshold}\n状态：${active ? '在售' : '下架'}`)) return
      await api(`/admin/products/${product.id}`, { method: 'PATCH', body: { name: product.name, categoryId: product.categoryId, description: product.description, active: product.active, expectedVersion: product.version, skus: product.skus.map((item) => ({ id: item.id, specName: item.specName, priceCents: item.id === sku.id ? priceCents : item.priceCents, lowStockThreshold: item.id === sku.id ? lowStockThreshold : item.lowStockThreshold, active: item.id === sku.id ? active : item.active })) } })
      setNotice({ text: '规格已更新', tone: 'good' })
      products.reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  async function submitReceipt() {
    setBusy('receipt')
    try {
      const quantity = Number(receipt.quantity)
      if (!Number.isSafeInteger(quantity) || quantity <= 0) throw new Error('入库数量须为正整数')
      await api('/admin/inventory/receipts', { method: 'POST', body: { skuId: receipt.skuId, batchNo: receipt.batchNo, expiryDate: receipt.expiryDate, quantity, unitCostCents: parseMoneyCents(receipt.unitCost) } })
      setNotice({ text: '入库完成', tone: 'good' })
      inventory.reload()
      products.reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  async function submitWaste() {
    setBusy('waste')
    try {
      const quantity = Number(waste.quantity)
      if (!Number.isSafeInteger(quantity) || quantity <= 0) throw new Error('报损数量须为正整数')
      if (!window.confirm(`确认登记报损 ${quantity} 件？这会扣减该批次库存并记录操作。`)) return
      await api('/admin/inventory/waste', { method: 'POST', body: { lotId: waste.lotId, quantity, reason: waste.reason } })
      setNotice({ text: '报损已登记', tone: 'good' })
      inventory.reload()
      products.reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  return (
    <>
      {node}
      <PageHeader title="商品、规格与库存" subtitle="商品上下架与价格走版本校验；库存只通过入库/报损流水改动" />
      <div className="two-col">
        <section className="panel">
          <h3>新建商品</h3>
          <div className="form-grid">
            <label>名称<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></label>
            <label>分类<select value={form.categoryId} onChange={(e) => setForm({ ...form, categoryId: e.target.value })}><option value="">选择分类</option>{(categories.data || []).map((category) => <option value={category.id} key={category.id}>{category.name}</option>)}</select></label>
            <label>规格<input value={form.specName} onChange={(e) => setForm({ ...form, specName: e.target.value })} /></label>
            <label>价格（元）<input type="number" step="0.01" value={form.price} onChange={(e) => setForm({ ...form, price: e.target.value })} /></label>
            <label>库存预警阈值<input type="number" min="0" step="1" value={form.threshold} onChange={(e) => setForm({ ...form, threshold: e.target.value })} /></label>
          </div>
          <button className="btn btn-primary" disabled={!!busy || !form.name || !form.categoryId || !form.price} onClick={createProduct}>创建商品</button>
        </section>
        <section className="panel">
          <h3>入库 / 报损</h3>
          <div className="form-grid">
            <label>SKU<select value={receipt.skuId} onChange={(e) => setReceipt({ ...receipt, skuId: e.target.value })}><option value="">选择 SKU</option>{(products.data || []).flatMap((product) => product.skus.map((sku) => <option value={sku.id} key={sku.id}>{product.name} / {sku.specName}</option>))}</select></label>
            <label>批次<input value={receipt.batchNo} onChange={(e) => setReceipt({ ...receipt, batchNo: e.target.value })} /></label>
            <label>到期日<input type="date" value={receipt.expiryDate} onChange={(e) => setReceipt({ ...receipt, expiryDate: e.target.value })} /></label>
            <label>数量<input type="number" value={receipt.quantity} onChange={(e) => setReceipt({ ...receipt, quantity: e.target.value })} /></label>
            <label>单位成本（元）<input type="number" step="0.01" value={receipt.unitCost} onChange={(e) => setReceipt({ ...receipt, unitCost: e.target.value })} /></label>
          </div>
          <button className="btn" disabled={!!busy || !receipt.skuId || !receipt.quantity} onClick={submitReceipt}>确认入库</button>
          <hr />
          <div className="form-grid">
            <label>批次<select value={waste.lotId} onChange={(e) => setWaste({ ...waste, lotId: e.target.value })}><option value="">选择批次</option>{(inventory.data || []).map((lot) => <option value={lot.id} key={lot.id}>{lot.productName} / {lot.specName} / {lot.batchNo} · 可用 {lot.available}</option>)}</select></label>
            <label>数量<input type="number" value={waste.quantity} onChange={(e) => setWaste({ ...waste, quantity: e.target.value })} /></label>
            <label>原因<input value={waste.reason} onChange={(e) => setWaste({ ...waste, reason: e.target.value })} /></label>
          </div>
          <button className="btn btn-danger-ghost" disabled={!!busy || !waste.lotId || !waste.quantity} onClick={submitWaste}>登记报损</button>
        </section>
      </div>
      <section className="panel">
        <h3>商品列表</h3>
        {products.loading ? <Loading /> : products.error ? <ErrorBox message={products.error} onRetry={products.reload} /> : (
          <div className="product-list">
            {(products.data || []).map((product) => (
              <div className={`product-row ${!product.active ? 'inactive' : ''}`} key={product.id}>
                <div><b>{product.name}</b><span className="muted"> v{product.version} · {product.active ? '在售' : '下架'}</span></div>
                <div className="sku-list">
                  {product.skus.map((sku) => <SKUEditor key={sku.id} product={product} sku={sku} busy={!!busy} onSave={updateSKU} />)}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
      <section className="panel">
        <h3>批次库存</h3>
        {inventory.loading ? <Loading /> : <div className="table-wrap"><table><thead><tr><th>商品/规格</th><th>批次</th><th>到期</th><th>在手</th><th>预留</th><th>可用</th><th>成本</th></tr></thead><tbody>{(inventory.data || []).map((lot) => <tr key={lot.id}><td>{lot.productName}<small className="muted"> {lot.specName}</small></td><td>{lot.batchNo}</td><td>{lot.expiryDate}</td><td>{lot.qtyOnHand}</td><td>{lot.qtyReserved}</td><td><b>{lot.available}</b></td><td>{money(lot.unitCostCents)}</td></tr>)}</tbody></table></div>}
      </section>
    </>
  )
}

function SKUEditor({ product, sku, busy, onSave }: { product: Product; sku: ProductSKU; busy: boolean; onSave: (product: Product, sku: ProductSKU, price: string, active: boolean, threshold: string) => void }) {
  const [price, setPrice] = useState((sku.priceCents / 100).toFixed(2))
  const [active, setActive] = useState(sku.active)
  const [threshold, setThreshold] = useState(String(sku.lowStockThreshold))
  useEffect(() => {
    setPrice((sku.priceCents / 100).toFixed(2))
    setActive(sku.active)
    setThreshold(String(sku.lowStockThreshold))
  }, [sku.priceCents, sku.active, sku.lowStockThreshold])
  return (
    <span className="sku-edit">
      <span>{sku.specName}</span>
      <span className={product.active && sku.active && sku.available <= sku.lowStockThreshold ? 'danger-text' : 'muted'}>可用 {sku.available}{product.active && sku.active && sku.available <= sku.lowStockThreshold ? ' · 库存预警' : ''}</span>
      <label>价格（元）<input aria-label={`${sku.specName}价格`} type="number" min="0" step="0.01" value={price} onChange={(e) => setPrice(e.target.value)} /></label>
      <label>预警阈值<input aria-label={`${sku.specName}预警阈值`} type="number" min="0" step="1" value={threshold} onChange={(e) => setThreshold(e.target.value)} /></label>
      <label className="check"><input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />在售</label>
      <button className="btn btn-small" disabled={busy} onClick={() => onSave(product, sku, price, active, threshold)}>保存</button>
    </span>
  )
}

export function AdminRefunds() {
  const { data, error, loading, reload } = useAsync<Refund[]>(() => api('/admin/refunds'))
  const [busy, setBusy] = useState('')
  const { setNotice, node } = useNotice()
  async function succeed(refund: Refund) {
    setBusy(refund.id)
    try {
      await api(`/test/refunds/${refund.id}/succeed`, { method: 'POST', body: { confirm: 'SIMULATED_REFUND' } })
      setNotice({ text: '模拟退款成功（仅测试环境）', tone: 'good' })
      reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  return (
    <>
      {node}
      <PageHeader title="售后退款" subtitle="资金退款与实物回库分开；待处理/失败退款不计入销售日报" />
      <div className="table-wrap">
        <table><thead><tr><th>退款单</th><th>订单</th><th>金额</th><th>状态</th><th>原因</th><th>创建</th><th /></tr></thead>
          <tbody>{(data || []).map((refund) => <tr key={refund.id}><td>{refund.outRefundNo}</td><td>#{refund.orderNo}</td><td>{money(refund.amountCents)}</td><td><StatusBadge status={refund.status} /></td><td>{refund.reason}</td><td>{shortTime(refund.createdAt)}</td><td>{refund.status !== 'SUCCESS' && <button className="btn btn-small" disabled={!!busy} onClick={() => succeed(refund)}>模拟退款成功</button>}</td></tr>)}</tbody>
        </table>
        {!data?.length && <Empty text="暂无退款" />}
      </div>
    </>
  )
}

export function AdminStaff() {
  const staff = useAsync<any[]>(() => api('/admin/staff'))
  const [busy, setBusy] = useState('')
  const { setNotice, node } = useNotice()
  async function edit(member: any) {
    const value = window.prompt('输入角色，逗号分隔：store_admin,catalog_operator,warehouse_operator,dispatcher,courier,support,finance', member.roles.join(','))
    if (value === null) return
    setBusy(member.id)
    try {
      await api(`/admin/staff/${member.id}/roles`, { method: 'PATCH', body: { roles: value.split(',').map((role) => role.trim()).filter(Boolean), expectedVersion: member.version } })
      setNotice({ text: '角色已更新', tone: 'good' })
      staff.reload()
    } catch (err: any) { setNotice({ text: err.message, tone: 'danger' }) } finally { setBusy('') }
  }
  return (
    <>
      {node}
      <PageHeader title="员工权限与审计" subtitle="角色、门店范围与对象归属同时校验；变更留审计" />
      <div className="two-col">
        <section className="panel"><h3>员工</h3>{staff.loading ? <Loading /> : <div className="list">{(staff.data || []).map((member) => <div className="list-row" key={member.id}><div><b>{member.nickname}</b><small>{member.phone} · {member.roles.join(' / ')}</small></div><button className="btn btn-small" disabled={!!busy} onClick={() => edit(member)}>改角色</button></div>)}</div>}</section>
        <AuditLogPanel />
      </div>
      <UrgePolicyCard />
    </>
  )
}

export function AuditLogPanel() {
  const emptyFilters = { action: '', objectType: '', actorId: '', from: '', to: '' }
  const [filters, setFilters] = useState(emptyFilters)
  const [query, setQuery] = useState(emptyFilters)
  const [page, setPage] = useState(1)
  const params = new URLSearchParams({ page: String(page), pageSize: '20' })
  Object.entries(query).forEach(([key, value]) => { if (value.trim()) params.set(key, value.trim()) })
  const audit = useAsync<any>(() => api(`/admin/audit-logs?${params.toString()}`), [query, page])
  const pages = Math.max(1, Math.ceil((audit.data?.total || 0) / 20))
  return (
    <section className="panel"><h3>操作审计</h3>
      <form className="form-grid" onSubmit={event => { event.preventDefault(); setPage(1); setQuery({ ...filters }) }}>
        <label>动作<input value={filters.action} placeholder="如 UPDATE_PRODUCT" onChange={e => setFilters({ ...filters, action: e.target.value })} /></label>
        <label>对象类型<input value={filters.objectType} placeholder="如 PRODUCT" onChange={e => setFilters({ ...filters, objectType: e.target.value })} /></label>
        <label>操作人 ID<input value={filters.actorId} onChange={e => setFilters({ ...filters, actorId: e.target.value })} /></label>
        <label>开始日期<input type="date" value={filters.from} onChange={e => setFilters({ ...filters, from: e.target.value })} /></label>
        <label>结束日期<input type="date" value={filters.to} onChange={e => setFilters({ ...filters, to: e.target.value })} /></label>
        <button type="submit" className="btn">筛选审计</button>
        <button type="button" className="btn btn-ghost" onClick={() => { setFilters(emptyFilters); setQuery({ ...emptyFilters }); setPage(1) }}>清空筛选</button>
      </form>
      {audit.loading ? <Loading /> : audit.error ? <ErrorBox message={audit.error} onRetry={audit.reload} /> : (
        <>
          <div className="list compact">{(audit.data?.items || []).map((log: any) => (
            <div className="list-row" key={log.id}>
              <div style={{ minWidth: 0, width: '100%' }}>
                <b>{log.action}</b>
                <small>{log.objectType} {log.objectId} · {shortTime(log.createdAt)} · 操作人 {log.actorId}</small>
                <details><summary>查看修改前后详情</summary>
                  <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify({ before: log.before, after: log.after }, null, 2)}</pre>
                  <small>追踪号：{log.traceId}</small>
                </details>
              </div>
            </div>
          ))}</div>
          {!audit.data?.items?.length && <Empty text="没有符合条件的审计记录" />}
        </>
      )}
      <div className="toolbar">
        <button className="btn btn-small" disabled={audit.loading || !!audit.error || page <= 1} onClick={() => setPage(previous => previous - 1)}>上一页</button>
        <span>第 {page} / {pages} 页 · 共 {audit.data?.total || 0} 条</span>
        <button className="btn btn-small" disabled={audit.loading || !!audit.error || page >= pages} onClick={() => setPage(previous => previous + 1)}>下一页</button>
      </div>
    </section>
  )
}

function UrgePolicyCard() {
  const { data, reload } = useAsync<any>(() => api('/admin/urge-policy'))
  const [cooldown, setCooldown] = useState('300')
  const [max, setMax] = useState('3')
  const [enabled, setEnabled] = useState(true)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState('')
  useEffect(() => {
    if (data) {
      setCooldown(String(data.cooldownSeconds))
      setMax(String(data.maxPerOrder))
      setEnabled(!!data.enabled)
    }
  }, [data])
  async function save() {
    setBusy(true)
    setMsg('')
    try {
      await api('/admin/urge-policy', { method: 'PUT', body: { cooldownSeconds: Number(cooldown), maxPerOrder: Number(max), enabled, expectedVersion: data?.version || 0 } })
      setMsg('已保存；新规则只影响之后的催单')
      reload()
    } catch (err: any) {
      setMsg(err.message || '保存失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="panel" style={{ marginTop: 16 }}>
      <h3>催单策略</h3>
      <p className="fine">默认同一订单 5 分钟一次、累计最多 3 次。策略由数据库保存，前后端统一读取；催单不会改变订单状态。</p>
      <div className="form-grid">
        <label>冷却秒数<input type="number" min={0} max={86400} value={cooldown} onChange={(e) => setCooldown(e.target.value)} /></label>
        <label>每单上限<input type="number" min={1} max={20} value={max} onChange={(e) => setMax(e.target.value)} /></label>
        <label className="check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />开启催单</label>
      </div>
      <button className="btn btn-primary" disabled={busy} onClick={save}>{busy ? '保存中…' : '保存策略'}</button>
      {msg && <p className="fine">{msg}</p>}
    </section>
  )
}


