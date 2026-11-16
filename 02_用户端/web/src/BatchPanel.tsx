import { useEffect, useRef, useState } from 'react'
import { api, newRequestId } from './api'
import { pendingStore } from './commands'
import { batchFinished, executeBatch, validSelection } from './batch.js'
import { arrivalFor } from './location'
import type { DeliveryTask, OrderSummary } from './types'

type Row = (OrderSummary | DeliveryTask)
const labels: Record<string,string> = { ACCEPT: '接单备货', READY: '确认出餐', CLAIM: '领取任务', PICKUP: '核对并取货', COMPLETE: '同配送点送达' }
export function BatchPanel({ rows, kind, onDone }: { rows: Row[]; kind: 'admin'|'courier'; onDone:()=>void }) {
  const key = 'campus_batch_' + kind
  const [action, setAction] = useState(kind === 'admin' ? 'ACCEPT' : 'CLAIM')
  const [selected, setSelected] = useState<Row[]>([])
  const [pending, setPending] = useState<any>(null)
  const [result, setResult] = useState<any>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const running = useRef(false)
  const onDoneRef = useRef(onDone); onDoneRef.current = onDone
  function eligible(row: Row) {
    if (!Number.isInteger(row.version)) return false
    if (kind === 'admin') return row.status === (action === 'ACCEPT' ? 'PAID' : 'PICKING')
    const task = row as DeliveryTask
    return action === 'CLAIM' ? task.status === 'OPEN' : action === 'PICKUP' ? task.status === 'ACCEPTED' && task.orderStatus === 'READY' : task.status === 'DELIVERING' && !!task.deliveryPointId
  }
  useEffect(() => {
    setSelected(previous => {
      const next = validSelection(previous, rows, eligible)
      return next.length === previous.length ? previous : next
    })
  }, [rows, action])
  useEffect(() => {
    try {
      const saved = pendingStore.get(key)
      setPending(saved)
      if (saved) { setResult(saved.progress || null); void run(null, false) }
    } catch (error: any) { setMessage(error.message) }
  }, [key])
  async function run(draft: any, resume: boolean) {
    if (running.current) return
    running.current = true; setBusy(true); setMessage('')
    try {
      const value = await executeBatch(pendingStore, key, draft, api, resume, setResult, async (saved, progress) => {
        if (saved.payload.action !== 'COMPLETE') return {}
        const ids = progress.items.filter((item:any)=>item.result==='PENDING').map((item:any)=>String(item.taskId))
        return {arrivalToken:(await arrivalFor(ids)).arrivalToken}
      })
      if (value) {
        setResult(value)
        if (batchFinished(value)) { setSelected([]); setMessage('批次已处理完毕，请查看每单结果'); onDoneRef.current() }
        else setMessage('仍有待确认订单，请继续恢复原批次')
      }
    } catch (error: any) {
      setMessage((error.message || '批次结果待确认') + (error.traceId ? ' · 追踪号 ' + error.traceId : '') + '。原请求编号已保留，请恢复原批次。')
    } finally {
      try { setPending(pendingStore.get(key)) } catch (error:any) { setMessage(error.message) }
      running.current = false; setBusy(false)
    }
  }
  async function submit() {
    if (running.current || !selected.length) return
    try { if (pendingStore.get(key)) { setMessage('请先恢复原批次，不能创建新请求'); return } } catch(error:any) { setMessage(error.message); return }
    const snapshot = selected.map(row => ({...row}))
    if (action === 'COMPLETE' && new Set(snapshot.map(row => (row as DeliveryTask).deliveryPointId)).size !== 1) { setMessage('批量送达仅可选择同一个配送点的配送中任务'); return }
    const detail = action === 'PICKUP' ? '请逐单核对订单、商品和数量，确认已经全部取货。' : action === 'COMPLETE' ? '确认商品已交付到同一配送点，接下来将获取真实定位。' : '请核对订单后提交。'
    if (!window.confirm(`${labels[action]}：共 ${snapshot.length} 单。${detail}`)) return
    const payload: any = { action, items: snapshot.map(row => ({ [kind === 'admin' ? 'orderId' : 'taskId']: row.id, expectedVersion: row.version })) }
    if (action === 'PICKUP') payload.checked = true
    if (action === 'COMPLETE') {
      running.current = true; setBusy(true)
      try { payload.arrivalToken = (await arrivalFor(snapshot.map(row => row.id))).arrivalToken }
      catch(error:any) { setMessage(error.message); return }
      finally { running.current = false; setBusy(false) }
    }
    await run({ requestId: newRequestId('batch'), path: kind === 'admin' ? '/admin/orders/batch-actions' : '/courier/tasks/batch-actions', payload }, false)
  }
  const limit = kind === 'admin' ? 50 : 20
  const candidates = rows.filter(eligible)
  const pageCandidates = candidates.slice(0,limit)
  const all = candidates.length > 0 && pageCandidates.every(row => selected.some(item => item.id === row.id))
  return <section className="panel batch-panel">
    <div className="section-head"><h3>批量处理</h3><span className="muted">勾选后版本发生变化的订单会自动取消选择</span></div>
    {pending && <div className="warn-box" role="status"><b>存在待确认批次</b><p>请求编号：{pending.requestId} · {labels[pending.payload?.action] || pending.payload?.action}</p><button className="btn" disabled={busy} onClick={() => run(null, true)}>{busy ? '核对中…' : '恢复原批次并继续处理'}</button></div>}
    <div className="toolbar"><select aria-label="批量操作" value={action} disabled={busy || !!pending} onChange={e => { setAction(e.target.value); setSelected([]) }}>{(kind === 'admin' ? ['ACCEPT','READY'] : ['CLAIM','PICKUP','COMPLETE']).map(value=><option key={value} value={value}>{labels[value]}</option>)}</select><label className="check-label"><input type="checkbox" checked={all} disabled={busy || !!pending || !candidates.length} onChange={e => setSelected(e.target.checked ? [...pageCandidates] : [])} />当前页全选（最多 {limit} 单）</label><span>已选 {selected.length} 单</span><button className="btn btn-primary" disabled={!selected.length || busy || !!pending} onClick={submit}>{busy ? '处理中…' : labels[action]}</button></div>
    <div className="batch-options">{candidates.map(row=><label key={row.id} className="batch-option"><input type="checkbox" checked={selected.some(item=>item.id===row.id)} disabled={busy || !!pending} onChange={e=>setSelected(previous=>e.target.checked ? previous.length < limit ? [...previous,row] : previous : previous.filter(item=>item.id!==row.id))} /><span><b>#{row.orderNo}</b> · v{row.version}{kind === 'courier' && <small>{(row as DeliveryTask).deliveryPointName || (row as DeliveryTask).buildingName || '未配置配送点'}</small>}</span></label>)}</div>
    {!candidates.length && <p className="muted">当前页没有可执行此操作的订单</p>}
    {message && <p className="form-error" role="status">{message}</p>}
    {result && <div aria-live="polite"><p>批次 {result.batchId}：已处理 {result.completedCount || 0}/{result.expectedCount || 0}，待确认 {result.pendingCount || 0}</p><div className="table-wrap"><table><thead><tr><th>订单 / 任务</th><th>结果</th><th>说明</th></tr></thead><tbody>{(result.items || []).map((item:any)=><tr key={item.orderId || item.taskId}><td>{item.orderId || item.taskId}</td><td>{({SUCCESS:'成功',FAILED:'失败',PENDING:'待确认'} as any)[item.result] || item.result}</td><td>{item.message || item.code || '—'}</td></tr>)}</tbody></table></div></div>}
  </section>
}

