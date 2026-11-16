const {command,store}=require('../../../utils/pending')
const {executeBatch,batchFinished,validSelection}=require('../../../utils/batch')
const {arrivalFor}=require('../../../utils/location')
const api=require('../../../utils/api')
const labels={CLAIM:'领取任务',PICKUP:'核对并取货',COMPLETE:'同点送达'}
const key='campus_batch_courier'
const request=(path,options={})=>api.request(path,options.method||'GET',options.body)
Page({
 data:{available:[],mine:[],loading:true,error:'',busy:false,action:'CLAIM',actions:['领取任务','核对并取货','同点送达'],candidates:[],selectedCount:0,pending:null,result:null,capacity:null},
 onShow(){
  if(!api.token()){wx.reLaunch({url:'/pages/login/login'});return}
  if(!api.isCourier(wx.getStorageSync('campus_user'))){this.setData({error:'需要配送员角色'});return}
  this.visible=true;this.selection=this.selection||[];this.load();this.readPending()
 },
 onHide(){this.visible=false;clearTimeout(this.timer)},onUnload(){this.onHide()},
 async load(){
  if(this.loadingRequest)return
  this.loadingRequest=true;clearTimeout(this.timer)
  try{const [available,mine,capacity]=await Promise.all([api.request('/courier/tasks/available'),api.request('/courier/tasks'),api.request('/courier/capacity')]);this.setData({available:available||[],mine:mine||[],capacity,error:''});this.failures=0;this.decorate()}
  catch(error){this.failures=(this.failures||0)+1;this.setData({error:error.message})}
  finally{this.loadingRequest=false;this.setData({loading:false});if(this.visible)this.timer=setTimeout(()=>this.load(),Math.min(10000*2**(this.failures||0),60000))}
 },
 eligible(task){return this.data.action==='CLAIM'?task.status==='OPEN':this.data.action==='PICKUP'?task.status==='ACCEPTED'&&task.orderStatus==='READY':task.status==='DELIVERING'&&!!task.deliveryPointId},
 decorate(){const rows=[...this.data.available,...this.data.mine];this.selection=validSelection(this.selection||[],rows,row=>this.eligible(row));this.setData({selectedCount:this.selection.length,candidates:rows.filter(row=>this.eligible(row)).map(row=>({...row,selected:this.selection.some(saved=>saved.id===row.id)}))})},
 selectAction(event){this.selection=[];this.setData({action:['CLAIM','PICKUP','COMPLETE'][Number(event.detail.value)]});this.decorate()},
 toggle(event){if(this.data.busy||this.data.pending)return;const id=event.currentTarget.dataset.id,row=this.data.candidates.find(item=>item.id===id);if(!row)return;if(this.selection.some(item=>item.id===id))this.selection=this.selection.filter(item=>item.id!==id);else if(this.selection.length<20)this.selection.push({...row});else wx.showToast({title:'每批最多 20 单',icon:'none'});this.decorate()},
 selectAll(){if(this.data.busy||this.data.pending)return;this.selection=this.data.candidates.slice(0,20).map(row=>({...row}));this.decorate()},
 clearSelected(){this.selection=[];this.decorate()},
 readPending(){try{const pending=store.get(key);this.setData({pending,result:pending?.progress||this.data.result});if(pending&&!this.batchRunning)this.runBatch(null,false)}catch(error){this.setData({error:error.message})}},
 async submitBatch(){
  if(this.batchRunning||!this.selection.length)return
  try{if(store.get(key)){this.setData({error:'请先恢复原批次'});return}}catch(error){this.setData({error:error.message});return}
  const action=this.data.action,snapshot=this.selection.map(row=>({...row}))
  if(action==='COMPLETE'&&new Set(snapshot.map(row=>row.deliveryPointId)).size!==1){this.setData({error:'批量送达仅能选择同配送点的配送中任务'});return}
  const confirmed=await new Promise(resolve=>wx.showModal({title:labels[action]+' '+snapshot.length+' 单',content:action==='PICKUP'?'请逐单核对商品及数量，确认已经取货。':action==='COMPLETE'?'确认商品已交付，随后将获取真实位置核验。':'请确认要领取勾选的任务。',success:res=>resolve(res.confirm),fail:()=>resolve(false)}))
  if(!confirmed)return
  const payload={action,items:snapshot.map(row=>({taskId:row.id,expectedVersion:row.version}))}
  if(action==='PICKUP')payload.checked=true
  if(action==='COMPLETE'){
   this.batchRunning=true;this.setData({busy:true})
   try{payload.arrivalToken=(await arrivalFor(snapshot.map(row=>row.id))).arrivalToken}catch(error){this.setData({error:error.message});return}finally{this.batchRunning=false;this.setData({busy:false})}
  }
  this.runBatch({path:'/courier/tasks/batch-actions',requestId:'batch_'+Date.now()+'_'+Math.random().toString(36).slice(2,10),payload},false)
 },
 async runBatch(draft,resume){
  if(this.batchRunning)return;this.batchRunning=true;this.setData({busy:true,error:''})
  try{const result=await executeBatch(store,key,draft,request,resume,result=>this.setData({result}),async(saved,progress)=>saved.payload.action==='COMPLETE'?{arrivalToken:(await arrivalFor(progress.items.filter(item=>item.result==='PENDING').map(item=>String(item.taskId)))).arrivalToken}:{});if(batchFinished(result)){this.selection=[];this.load();wx.showToast({title:'批次已处理，请查看逐单结果',icon:'none'})}}
  catch(error){this.setData({error:(error.message||'结果待确认')+'；原请求编号已保留，请恢复原批次。'+(error.traceId?' 追踪号 '+error.traceId:'')})}
  finally{try{this.setData({pending:store.get(key)})}catch(error){this.setData({error:error.message})}this.batchRunning=false;this.setData({busy:false})}
 },
 resumeBatch(){this.runBatch(null,true)},
 open(event){wx.navigateTo({url:'/pages/delivery/task-detail/task-detail?id='+event.currentTarget.dataset.id})},
 refresh(){this.load()}
})
