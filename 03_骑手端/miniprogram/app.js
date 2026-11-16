const notifications=require('./utils/notifications')
const api = require('./utils/api')

App({
  globalData: {
    user: null,
    baseUrl: 'http://127.0.0.1:8080/api/v1',
  },
  onLaunch() {
    const user = wx.getStorageSync('campus_user')
    if (user) this.globalData.user = user
  },
  onShow(){notifications.start()},
  onHide(){notifications.stop()},
  ensureLogin() {
    if (!api.token()) {
      wx.reLaunch({ url: '/pages/login/login' })
      return false
    }
    return true
  },
})

