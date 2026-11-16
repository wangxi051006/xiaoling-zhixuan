const notifications=require('../../../utils/notifications')
const api = require('../../../utils/api')

const STATUS = {
  PENDING_PAYMENT: '待支付', PAID: '已付款', PICKING: '备货中', READY: '待出餐',
  DELIVERING: '配送中', COMPLETED: '已完成', CANCELLED: '已取消', CLOSED: '已关闭', PAYMENT_EXCEPTION: '支付异常',
}

Page({
  onHide(){this.unsubscribeNotifications?.()},onUnload(){this.unsubscribeNotifications?.()},
  goMessages(){wx.navigateTo({url:'/pages/user/messages/messages'})},
  data: { orders: [], loading: true, error: '' },
  onShow() {
    this.unsubscribeNotifications?.();this.unsubscribeNotifications=notifications.subscribe(state=>this.setData({notificationUnread:state.notificationUnread}));notifications.start();
    if (!api.token()) { wx.reLaunch({ url: '/pages/login/login' }); return }
    this.load()
  },
  load() {
    this.setData({ loading: true, error: '' })
    api.request('/orders?limit=50')
      .then((result) => {
        const orders = (result.items || []).map((order) => ({
          ...order,
          statusText: STATUS[order.status] || order.status,
          totalText: api.money(order.totalCents),
          timeText: new Date(order.createdAt).toLocaleString('zh-CN', { hour12: false }),
        }))
        this.setData({ orders })
      })
      .catch((err) => this.setData({ error: err.message || '加载失败' }))
      .finally(() => this.setData({ loading: false }))
  },
  detail(event) {
    wx.navigateTo({ url: '/pages/user/order-detail/order-detail?id=' + event.currentTarget.dataset.id })
  },
  goHome() { wx.reLaunch({ url: '/pages/user/home/home' }) },
})

