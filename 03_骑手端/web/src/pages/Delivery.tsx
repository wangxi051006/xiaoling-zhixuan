import { BatchPanel } from '../BatchPanel'
import { arrivalFor } from '../location'
import { deliveryCommand, pendingStore } from '../commands'
import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, money, shortTime } from '../api'
import { Empty, ErrorBox, Loading, Notice, PageHeader, StatusBadge, useAsync } from '../components'
import type { DeliveryTask } from '../types'


function PendingTaskCommands({taskId,onDone}:{taskId:string;onDone:()=>void}){
 const [message,setMessage]=useState(''),[busy,setBusy]=useState(false)
 const paths=['claim','pickup','start','complete','exceptions','resume'].map(action=>'/courier/tasks/'+taskId+'/'+action).filter(path=>{try{return !!pendingStore.get('campus_command_'+path)}catch{return false}})
 if(!paths.length)return null
 return <div className="warn-box"><b>存在待确认配送操作</b><p>恢复会复用原请求及版本。历史验证码请求已停用，请刷新状态后联系商家核对，不能直接重发。</p>{paths.map(path=><button key={path} className="btn" disabled={busy} onClick={async()=>{setBusy(true);try{const {warning}=await deliveryCommand(path,{});setMessage('原操作已确认'+warning);onDone()}catch(err:any){setMessage(err.message||'仍待确认')}finally{setBusy(false)}}}>恢复 {path.split('/').pop()}</button>)}{message&&<p role="status">{message}</p>}</div>
}

export function DeliveryTasks() {
  const available = useAsync<DeliveryTask[]>(() => api('/courier/tasks/available'))
  const mine = useAsync<DeliveryTask[]>(() => api('/courier/tasks'))
  const capacity = useAsync<{activeTaskCount:number;taskLimit:number;online:boolean}>(()=>api('/courier/capacity'))
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<{ text: string; tone?: 'info' | 'danger' | 'good' } | null>(null)
  function refreshTasks() { available.reload(); mine.reload(); capacity.reload() }
  async function claim(task: DeliveryTask) {
    setBusy(task.id)
    try {
      await deliveryCommand(`/courier/tasks/${task.id}/claim`, { expectedVersion: task.version })
      setNotice({ text: `已领取 ${task.orderNo}`, tone: 'good' })
      refreshTasks()
    } catch (err: any) { setNotice({ text: err.message || '领取失败', tone: 'danger' }) } finally { setBusy('') }
  }
  return (
    <>
      {notice && <Notice text={notice.text} tone={notice.tone} onClose={() => setNotice(null)} />}
      <PageHeader title="配送任务" subtitle="勾选任务批量领取、核货取货或同点送达；取货后请在详情开始配送" actions={<button className="btn btn-ghost" onClick={refreshTasks}>刷新</button>} />
      {capacity.data && <p className="capacity-line">当前 {capacity.data.online ? '在线' : '离线'} · 配送负载 {capacity.data.activeTaskCount}/{capacity.data.taskLimit} 单</p>}
      {capacity.error && <p className="form-error">容量信息暂不可用：{capacity.error}</p>}
      <BatchPanel rows={[...(available.data || []), ...(mine.data || [])]} kind="courier" onDone={refreshTasks} />
      <section className="panel">
        <h3>可接任务</h3>
        {available.loading ? <Loading /> : available.error ? <ErrorBox message={available.error} onRetry={available.reload} /> : (
          <div className="task-grid">
            {(available.data || []).map((task) => (
              <article className="task-card" key={task.id}>
                <div className="order-top"><b>#{task.orderNo}</b><StatusBadge status={task.status} /></div>
                <p><b>{task.buildingName || '楼栋待定'}</b> · {task.itemCount} 件</p>
                <p className="muted">{shortTime(task.createdAt)} · 订单状态 {task.orderStatus}</p>
                <button className="btn btn-primary" disabled={!!busy} onClick={() => claim(task)}>{busy === task.id ? '领取中…' : '领取任务'}</button>
              </article>
            ))}
            {!available.data?.length && <Empty text="暂无待领取任务" />}
          </div>
        )}
      </section>
      <section className="panel">
        <h3>我的任务</h3>{[...(available.data||[]),...(mine.data||[])].filter((task,index,all)=>all.findIndex(item=>item.id===task.id)===index).map(task=><PendingTaskCommands key={task.id} taskId={task.id} onDone={refreshTasks}/>)}
        {mine.loading ? <Loading /> : mine.error ? <ErrorBox message={mine.error} onRetry={mine.reload} /> : (
          <div className="task-grid">
            {(mine.data || []).map((task) => (
              <Link to={`/delivery/tasks/${task.id}`} className="task-card" key={task.id}>
                <div className="order-top"><b>#{task.orderNo}</b><StatusBadge status={task.status} /></div>
                <p><b>{task.buildingName}</b> · {task.itemCount} 件</p>
                <p className="muted">{task.contactName || '完成后撤去联系人'} {task.phone || ''}</p>
                <span className="link-text">查看与操作 →</span>
              </Link>
            ))}
            {!mine.data?.length && <Empty text="还没有领取任务" />}
          </div>
        )}
      </section>
    </>
  )
}

export function DeliveryTaskDetail() {
  const { id } = useParams()
  const { data, error, loading, reload } = useAsync<DeliveryTask>(() => api(`/courier/tasks/${id}`), [id])
  const [busy, setBusy] = useState('')
  const [code, setCode] = useState('')
  const [note, setNote] = useState('')
  const [resolveNote, setResolveNote] = useState('')
  const [type, setType] = useState('CUSTOMER_UNREACHABLE')
  const [notice, setNotice] = useState<{ text: string; tone?: 'info' | 'danger' | 'good' } | null>(null)
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} onRetry={reload} />
  if (!data) return <Empty text="任务不存在" />
  const task = data
  async function action(path: string, body: Record<string, unknown>, success: string) {
    if (path === 'pickup' && !window.confirm('请确认已逐件核对商品及数量，并已经取货。')) return
    setBusy(path)
    try {
      const arrivalToken = path === 'complete' ? (await arrivalFor([task.id])).arrivalToken : undefined
      await deliveryCommand(`/courier/tasks/${task.id}/${path}`, { expectedVersion: task.version, ...body, ...(arrivalToken ? {arrivalToken} : {}) })
      setNotice({ text: success, tone: 'good' })
      reload()
    } catch (err: any) { setNotice({ text: err.message || '操作失败', tone: 'danger' }) } finally { setBusy('') }
  }
  return (
    <>
      {notice && <Notice text={notice.text} tone={notice.tone} onClose={() => setNotice(null)} />}
      <PageHeader title={`任务 #${task.orderNo}`} subtitle={`${task.buildingName} · 版本 v${task.version} · 状态 ${task.status}`} actions={<Link className="btn btn-ghost" to="/delivery/tasks">返回任务</Link>} />
      <div className="detail-grid">
        <section className="panel">
          <h3>取送信息</h3>
          <dl className="kv">
            <dt>楼栋</dt><dd><b>{task.buildingName}</b></dd><dt>配送点</dt><dd>{task.deliveryPointName || '未配置，请联系商家'}{task.radiusMeters ? ` · 校验范围 ${task.radiusMeters} 米` : ''}</dd>
            <dt>地址</dt><dd>{task.addressText}</dd>
            <dt>联系人</dt><dd>{task.contactName || '已完成/待领取任务不展示完整信息'}</dd>
            <dt>电话</dt><dd>{task.phone || '—'}</dd>
            <dt>订单状态</dt><dd>{task.orderStatus}</dd>
          </dl>
          <h3>取货清单</h3>
          <div className="list">{(task.items || []).map((item) => <div className="list-row" key={item.id}><div><b>{item.productName}</b><small>{item.specName}</small></div><span>× {item.quantity}</span></div>)}</div>
          {!!task.urges?.length && (
            <div className="urge-admin">
              <h3>用户催单</h3>
              <p className="fine">仅当前指派配送员可见；改派或完成后不再展示。催单不改变取货、签收和完成核验。</p>
              <div className="urge-records">
                {task.urges.map((record) => (
                  <div className="urge-record" key={record.id}>
                    <div><b>第 {record.urgeNumber} 次</b><span className="muted"> {shortTime(record.createdAt)}</span><span className={`badge ${record.status === 'HANDLED' ? 'badge-good' : 'badge-warn'}`}>{record.status === 'HANDLED' ? '商家已处理' : '待商家处理'}</span></div>
                    <div className="small">{record.reason}</div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>
        <section className="panel">
          <h3>下一步</h3><PendingTaskCommands taskId={task.id} onDone={reload}/>{task.status === 'ACCEPTED' && task.orderStatus !== 'READY' && <p>商家尚未备齐，请等待订单进入待出餐后核货取货。</p>}
          {task.status === 'ACCEPTED' && task.orderStatus === 'READY' && <button className="btn btn-primary btn-block" disabled={!!busy} onClick={() => action('pickup', { checked: true }, '已确认取货')}>我已按清单取货</button>}
          {task.status === 'PICKED_UP' && <button className="btn btn-primary btn-block" disabled={!!busy} onClick={() => action('start', {}, '开始配送')}>开始配送</button>}
          {task.status === 'DELIVERING' && (
            <div className="deliver-box">
              <h4>到达配送点</h4><p className="muted">到达 {task.deliveryPointName || '配送点（请先联系商家配置）'} 后，获取真实位置并确认送达。定位需授权且页面需使用 HTTPS。</p><button className="btn btn-primary" disabled={!!busy || !task.deliveryPointId} onClick={() => { if (window.confirm('确认已将商品送达配送点？')) void action('complete', {}, '配送完成') }}>{busy ? '定位核验中…' : '定位并确认送达'}</button>
            </div>
          )}
          {task.status === 'EXCEPTION' && (
            <div className="deliver-box">
              <h4>任务已登记异常</h4>
              <p className="muted">{task.orderStatus === 'DELIVERING' ? '商品已在您手上：填写说明后可继续配送，送达时需要重新定位。' : '商品已退回待取货：商家备齐后即可重新取货。'}</p>
              <textarea value={resolveNote} onChange={(e) => setResolveNote(e.target.value)} placeholder="恢复说明，便于人工跟进" rows={3} />
              <button className="btn btn-primary" disabled={!resolveNote.trim() || !!busy} onClick={() => action('resume', { note: resolveNote }, '已恢复配送')}>恢复配送</button>
            </div>
          )}
          {['ACCEPTED', 'PICKED_UP', 'DELIVERING'].includes(task.status) && <><hr />
          <h4>异常处理</h4>
          <p className="muted">异常不等于送达；不会自动退款或回补库存。</p>
          <select value={type} onChange={(e) => setType(e.target.value)}><option value="CUSTOMER_UNREACHABLE">联系不上买家</option><option value="ADDRESS_ISSUE">地址/门禁问题</option><option value="DAMAGED">商品破损</option><option value="OTHER">其他</option></select>
          <textarea value={note} onChange={(e) => setNote(e.target.value)} placeholder="说明情况，便于人工跟进" rows={3} />
          <button className="btn btn-danger-ghost" disabled={!note.trim() || !!busy} onClick={() => action('exceptions', { type, note }, '异常已登记')}>登记异常</button></>}
          {task.status === 'COMPLETED' && <p className="notice">配送已完成，联系信息已隐藏。</p>}
          <p className="fine">离线只保留草稿；服务器确认前不显示“已送达”。</p>
        </section>
      </div>
    </>
  )
}

