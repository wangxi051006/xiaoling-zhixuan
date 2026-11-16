const notifications=require('../../../utils/notifications')
const api = require('../../../utils/api')

Page({
  onHide(){this.unsubscribeNotifications?.()},onUnload(){this.unsubscribeNotifications?.()},
  goMessages(){wx.navigateTo({url:'/pages/user/messages/messages'})},
  data: { user: null, isCourier: false },
  onShow() {
    this.unsubscribeNotifications?.();this.unsubscribeNotifications=notifications.subscribe(state=>this.setData({notificationUnread:state.notificationUnread}));notifications.start();
    if (!api.token()) { wx.reLaunch({ url: '/pages/login/login' }); return }
    const user = wx.getStorageSync('campus_user')
    this.setData({ user, isCourier: api.isCourier(user), roleText: (user.roles || []).length ? user.roles.join(' / ') : '顾客' })
  },
  goOrders() { wx.navigateTo({ url: '/pages/user/orders/orders' }) },
  goDelivery() { wx.reLaunch({ url: '/pages/delivery/tasks/tasks' }) },
  logout() {
    api.request('/auth/logout', 'POST', {}).catch(() => {})
    wx.removeStorageSync('campus_token')
    wx.removeStorageSync('campus_user')
    wx.reLaunch({ url: '/pages/login/login' })
  },
})

