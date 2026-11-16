const api = require('../../utils/api')

Page({
  data: { phone: '', password: '', busy: false, error: '', demo: false },
  onLoad() {
    let demo = false
    try { demo = wx.getAccountInfoSync().miniProgram.envVersion === 'develop' } catch (err) {}
    this.setData(demo ? { demo: true, phone: '13900000001', password: 'CampusDemo!2026' } : { demo: false })
  },
  onPhone(event) { this.setData({ phone: event.detail.value }) },
  onPassword(event) { this.setData({ password: event.detail.value }) },
  useDemo(event) {
    const phone = event.currentTarget.dataset.phone
    this.setData({ phone, password: 'CampusDemo!2026' })
  },
  submit() {
    if (this.data.busy) return
    this.setData({ busy: true, error: '' })
    api.login(this.data.phone.trim(), this.data.password)
      .then((session) => {
        const user = session.user
        wx.setStorageSync('campus_user', user)
        if (api.isCourier(user)) wx.reLaunch({ url: '/pages/delivery/tasks/tasks' })
        else wx.reLaunch({ url: '/pages/user/home/home' })
      })
      .catch((err) => this.setData({ error: err.message || '登录失败' }))
      .finally(() => this.setData({ busy: false }))
  },
})
