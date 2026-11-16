import { useState } from 'react'
import { api } from '../api'
import { Empty, ErrorBox, Loading, PageHeader, useAsync } from '../components'
import type { Building, DeliveryPoint } from '../types'
const blank = { buildingId:'',name:'',latitude:'',longitude:'',radiusMeters:'80',active:true }
export function DeliveryPoints() {
  const points=useAsync<DeliveryPoint[]>(()=>api('/admin/delivery-points'))
  const buildings=useAsync<Building[]>(()=>api('/stores/1/buildings'))
  const [draft,setDraft]=useState(blank),[editing,setEditing]=useState<DeliveryPoint|null>(null),[busy,setBusy]=useState(false),[message,setMessage]=useState('')
  function edit(point:DeliveryPoint) { setEditing(point); setDraft({buildingId:point.buildingId,name:point.name,latitude:String(point.latitude),longitude:String(point.longitude),radiusMeters:String(point.radiusMeters),active:point.active});setMessage('') }
  async function save(event:React.FormEvent) {
    event.preventDefault()
    const latitude=Number(draft.latitude),longitude=Number(draft.longitude),radiusMeters=Number(draft.radiusMeters)
    if(!draft.buildingId || !draft.name.trim() || !draft.latitude.trim() || !draft.longitude.trim() || !Number.isFinite(latitude) || latitude < -90 || latitude > 90 || !Number.isFinite(longitude) || longitude < -180 || longitude > 180 || !Number.isInteger(radiusMeters) || radiusMeters < 20 || radiusMeters > 300) {setMessage('请填写楼栋、配送点名称及有效 WGS84 经纬度，半径应为 20–300 整数米');return}
    setBusy(true);setMessage('')
    try { await api('/admin/delivery-points'+(editing?'/'+editing.id:''),{method:editing?'PUT':'POST',body:{buildingId:draft.buildingId,name:draft.name.trim(),latitude,longitude,radiusMeters,active:draft.active,...(editing?{expectedVersion:editing.version}:{})}});setDraft(blank);setEditing(null);setMessage('配送点已保存');points.reload() }
    catch(error:any){setMessage(error.message || '保存失败，请刷新后核对版本')}
    finally{setBusy(false)}
  }
  return <><PageHeader title="配送点设置" subtitle="每个楼栋配置明确的交付点。订单保存下单时的配送点，修改用于后续订单。坐标必须使用 WGS84。" />
    <section className="panel"><h3>{editing?'编辑配送点':'新建配送点'}</h3><form onSubmit={save}><div className="point-form"><label>所属楼栋<select value={draft.buildingId} onChange={e=>setDraft({...draft,buildingId:e.target.value})} required><option value="">选择楼栋</option>{(buildings.data||[]).map(row=><option key={row.id} value={row.id}>{row.name}</option>)}</select></label><label>配送点名称<input value={draft.name} onChange={e=>setDraft({...draft,name:e.target.value})} maxLength={100} required placeholder="如：1 号楼东门收货台" /></label><label>纬度（WGS84）<input value={draft.latitude} onChange={e=>setDraft({...draft,latitude:e.target.value})} inputMode="decimal" required /></label><label>经度（WGS84）<input value={draft.longitude} onChange={e=>setDraft({...draft,longitude:e.target.value})} inputMode="decimal" required /></label><label>允许到达半径（米）<input type="number" min="20" max="300" step="1" value={draft.radiusMeters} onChange={e=>setDraft({...draft,radiusMeters:e.target.value})} required /></label><label>状态<select value={draft.active?'on':'off'} onChange={e=>setDraft({...draft,active:e.target.value==='on'})}><option value="on">启用</option><option value="off">停用</option></select></label></div><div className="card-actions"><button className="btn btn-primary" disabled={busy}>{busy?'保存中…':'保存配送点'}</button>{editing&&<button type="button" className="btn btn-ghost" onClick={()=>{setEditing(null);setDraft(blank)}}>取消编辑</button>}</div></form>{message&&<p role="status">{message}</p>}{buildings.error&&<ErrorBox message={buildings.error} onRetry={buildings.reload}/>}</section>
    <section className="panel">{points.loading?<Loading/>:points.error?<ErrorBox message={points.error} onRetry={points.reload}/>:<div className="table-wrap"><table><thead><tr><th>配送点</th><th>楼栋</th><th>纬度 / 经度</th><th>半径</th><th>状态</th><th>操作</th></tr></thead><tbody>{(points.data||[]).map(point=><tr key={point.id}><td>{point.name}</td><td>{buildings.data?.find(row=>row.id===point.buildingId)?.name||point.buildingId}</td><td>{point.latitude} / {point.longitude}</td><td>{point.radiusMeters} 米</td><td>{point.active?'启用':'停用'}</td><td><button className="btn btn-ghost" onClick={()=>edit(point)}>编辑</button></td></tr>)}</tbody></table>{!points.data?.length&&<Empty text="尚未配置配送点，请先填写楼栋的实际交付位置"/>}</div>}</section></>
}
