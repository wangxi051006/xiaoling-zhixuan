const {arrivalFor}=require('../../../utils/location')
const {command,store}=require('../../../utils/pending')
const api = require('../../../utils/api')

Page({
  data: { id: '', task: null, loading: true, error: '', busy: false, note: '', type: 'CUSTOMER_UNREACHABLE',
    types: [
      { value: 'CUSTOMER_UNREACHABLE', label: '联系不上买家' },
      { value: 'ADDRESS_ISSUE', label: '地址/门禁问题' },
      { value: 'DAMAGED', label: '商品破损' },
      { value: 'OTHER', label: '其他' },
    ] },
  onLoad(options) { this.setData({ id: options.id }) },
  onShow() { this.visible=true;this.load();this.refreshPending() },
  onHide(){this.visible=false;clearTimeout(this.timer)},onUnload(){this.onHide()},
  refreshPending(){const actions=['claim','pickup','start','complete','exceptions'].filter(action=>{try{return !!store.get('campus_command_/courier/tasks/'+this.data.id+'/'+action)}catch{return false}});this.setData({pendingActions:actions})},
  retryCommand(event){this.action(event.currentTarget.dataset.action,{},'原操作已确认')},
  load() {
    if(this.loadingRequest)return
    this.loadingRequest=true;clearTimeout(this.timer)
    api.request('/courier/tasks/' + this.data.id)
      .then((task) => this.setData({
        task: {
          ...task,
          urges: (task.urges || []).map((item) => ({ ...item, timeText: new Date(item.createdAt).toLocaleString('zh-CN', { hour12: false }) })),
        },
        error: '',
      }))
      .catch((err) => this.setData({ error: err.message || '加载失败' }))
      .finally(() => {this.loadingRequest=false;this.setData({loading:false});if(this.visible)this.timer=setTimeout(()=>this.load(),10000)})
  },
  onNote(event) { this.setData({ note: event.detail.value }) },
  onType(event) {
    const item = this.data.types[Number(event.detail.value)]
    if (item) this.setData({ type: item.value })
  },
  action(path, body, success) {
    if (this.data.busy) return
    this.setData({ busy: true, error: '' })
    command('/courier/tasks/' + this.data.id + '/' + path, { expectedVersion: this.data.task.version, ...body })
      .then(() => {
        wx.showToast({ title: success, icon: 'none' })
        this.load()
      })
      .catch((err) => this.setData({ error: err.message || '操作失败' }))
      .finally(() => {this.setData({ busy: false });this.refreshPending()})
  },
  pickup() {if(this.data.task.orderStatus!=='READY'){this.setData({error:'商家尚未备齐，请等待待出餐后取货'});return}wx.showModal({title:'确认核货',content:'请逐件核对商品及数量，确认已经取货。',success:res=>{if(res.confirm)this.action('pickup',{checked:true},'已取货')}}) },
  start() { this.action('start', {}, '开始配送') },
  async complete() {
    if(this.data.busy)return
    const task=this.data.task
    if(!task.deliveryPointId){this.setData({error:'请先联系商家配置配送点'});return}
    const yes=await new Promise(resolve=>wx.showModal({title:'确认送达',content:'商品已交付到 '+task.deliveryPointName+'，随后将获取真实位置核验。',success:res=>resolve(res.confirm),fail:()=>resolve(false)}))
    if(!yes)return
    this.setData({busy:true,error:''})
    try{const saved=store.get('campus_command_/courier/tasks/'+task.id+'/complete');if(saved?.payload?.code)throw new Error('历史验证码请求已停用，请刷新订单并联系商家核对，不能盲目重发');const arrival=await arrivalFor([task.id]);await command('/courier/tasks/'+task.id+'/complete',{expectedVersion:task.version,arrivalToken:arrival.arrivalToken});wx.showToast({title:'配送完成',icon:'none'});this.load()}
    catch(error){this.setData({error:error.message})}
    finally{this.setData({busy:false});this.refreshPending()}
  },
  exception() {
    if (!this.data.note.trim()) { wx.showToast({ title: '请填写异常说明', icon: 'none' }); return }
    this.action('exceptions', { type: this.data.type, note: this.data.note }, '异常已登记')
  },
  goTasks() { wx.redirectTo({ url: '/pages/delivery/tasks/tasks' }) },
})

