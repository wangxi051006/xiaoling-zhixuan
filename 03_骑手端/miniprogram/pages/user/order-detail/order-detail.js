const notifications=require('../../../utils/notifications')
const {store,executeCommand,keepPending}=require('../../../utils/pending')
const api = require('../../../utils/api')

const STATUS = {
  PENDING_PAYMENT: '待支付', PAID: '已付款，待接单', PICKING: '备货中', READY: '待出餐',
  DELIVERING: '配送中', COMPLETED: '已完成', CANCELLED: '已取消', CLOSED: '已关闭', PAYMENT_EXCEPTION: '支付异常',
}

function timeText(value) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { hour12: false })
}

Page({
  onHide(){this.unsubscribeNotifications?.()},onUnload(){this.unsubscribeNotifications?.()},
  goMessages(){wx.navigateTo({url:'/pages/user/messages/messages'})},
  data: { id: '', order: null, statusText: '', totalText: '', busy: false, attempt: null, error: '',
    urgeOpen: false, urgeReason: '请尽快处理，谢谢', urgeBusy: false, urgePending: null },
  onLoad(options) {
    this.setData({ id: options.id })
    try { this.setData({ urgePending: store.get('campus_pending_urge_' + options.id) || null }) } catch (err) {}
  },
  onShow() {
    this.unsubscribeNotifications?.();this.unsubscribeNotifications=notifications.subscribe(state=>this.setData({notificationUnread:state.notificationUnread}));notifications.start(); this.load() },
  load() {
    if (!this.data.id) return
    api.request('/orders/' + this.data.id)
      .then((order) => {
        const urge = order.urge ? {
          ...order.urge,
          lastText: timeText(order.urge.lastAt),
          nextText: timeText(order.urge.nextAt),
          records: (order.urge.records || []).map((record) => ({
            ...record,
            timeText: timeText(record.createdAt),
            notificationText: record.notificationStatus === 'SENT' ? '已送达站内提醒'
              : record.notificationStatus === 'PENDING' ? '通知待发送'
                : record.notificationStatus === 'CANCELLED' ? '订单状态变化，取消通知' : '通知失败，商家仍可在工作台看到',
          })),
        } : null
        this.setData({
          order: {
            ...order,
            urge,
            items: (order.items || []).map((item) => ({ ...item, goodsText: api.money(item.goodsCents) })),
            events: (order.events || []).map((event) => ({ ...event, timeText: timeText(event.createdAt) })),
            createdText: timeText(order.createdAt),
            goodsText: api.money(order.goodsCents),
            deliveryText: api.money(order.deliveryCents),
            totalText: api.money(order.totalCents),
          },
          statusText: STATUS[order.status] || order.status,
          totalText: api.money(order.totalCents),
          error: '',
        })
      })
      .catch((err) => this.setData({ error: err.message || '加载失败' }))
  },
  startPay() {
    if (this.data.busy) return
    let channel = 'SIMULATED'
    try {
      const env = wx.getAccountInfoSync().miniProgram.envVersion
      if (env === 'release' || env === 'trial') channel = 'WECHAT_MINI'
    } catch (err) {}
    this.setData({ busy: true, error: '' })
    api.request('/orders/' + this.data.id + '/payment-attempt', 'POST', { channel })
      .then((attempt) => {
        this.setData({ attempt })
        if (attempt.channel === 'WECHAT_MINI' && attempt.prepayParams && attempt.prepayParams.package) {
          wx.requestPayment({
            timeStamp: attempt.prepayParams.timeStamp,
            nonceStr: attempt.prepayParams.nonceStr,
            package: attempt.prepayParams.package,
            signType: attempt.prepayParams.signType || 'RSA',
            paySign: attempt.prepayParams.paySign,
            success: () => wx.showToast({ title: '支付结果确认中', icon: 'none' }),
            fail: () => wx.showToast({ title: '支付未完成，以服务端为准', icon: 'none' }),
          })
        }
      })
      .catch((err) => this.setData({ error: err.message || '创建支付失败' }))
      .finally(() => this.setData({ busy: false }))
  },
  confirmSim() {
    this.setData({ busy: true, error: '' })
    api.request('/test/payments/' + this.data.id + '/succeed', 'POST', { confirm: 'SIMULATED_PAYMENT' }, { 'X-Simulated-Payment': 'confirm' })
      .then(() => {
        wx.showToast({ title: '模拟支付成功', icon: 'none' })
        this.load()
      })
      .catch((err) => this.setData({ error: err.message || '确认失败' }))
      .finally(() => this.setData({ busy: false }))
  },
  cancel() {
    wx.showModal({ title: '取消订单', content: '仅待支付订单可取消；已付款请走售后。', success: (res) => {
      if (!res.confirm) return
      this.setData({ busy: true })
      api.request('/orders/' + this.data.id + '/cancel', 'POST', {})
        .then(() => this.load())
        .catch((err) => this.setData({ error: err.message }))
        .finally(() => this.setData({ busy: false }))
    } })
  },
  onUrgeReason(event) { this.setData({ urgeReason: event.detail.value }) },
  toggleUrge() { this.setData({ urgeOpen: !this.data.urgeOpen }) },
  retryUrge() { this.submitUrge(true) },
  async submitUrge(){
    if(this.data.urgeBusy)return
    const key='campus_pending_urge_'+this.data.id;this.setData({urgeBusy:true})
    try{const saved=store.get(key),requestId=saved?saved.requestId:'urge_'+Date.now()+'_'+Math.random().toString(36).slice(2,12),pending=saved||{requestId,body:{requestId,reason:(this.data.urgeReason||'').trim()||'请尽快处理，谢谢'}};this.setData({urgePending:pending});const {warning}=await executeCommand(store,key,pending,p=>api.request('/orders/'+this.data.id+'/urges','POST',p.body,{'Idempotency-Key':p.requestId}));this.setData({urgePending:null,urgeOpen:false});wx.showToast({title:warning||'催单已提交',icon:'none'});this.load()}
    catch(err){try{this.setData({urgePending:store.get(key)})}catch(ignore){}this.setData({error:(err.message||'催单失败')+(keepPending(err)?'；请用原请求恢复，勿重复提交':'')})}finally{this.setData({urgeBusy:false})}
  },
  goOrders() { wx.redirectTo({ url: '/pages/user/orders/orders' }) },
})



