const { apiBase: BASE } = require('../config')
const TOKEN_KEY = 'campus_token'

function token() {
  return wx.getStorageSync(TOKEN_KEY) || ''
}

function request(path, method = 'GET', data, extraHeaders) {
  return new Promise((resolve, reject) => {
    wx.request({
      url: BASE + path,
      method,
      data,
      header: {
        'content-type': 'application/json',
        Authorization: token() ? 'Bearer ' + token() : '',
        ...(extraHeaders || {}),
      },
      success(res) {
        const body = res.data || {}
        if (res.statusCode >= 200 && res.statusCode < 300) {
          resolve(body.data)
          return
        }
        if (res.statusCode === 401) {
          wx.removeStorageSync(TOKEN_KEY)
          wx.removeStorageSync('campus_user')
        }
        const error = new Error(body.message || '请求失败')
        error.code = body.code
        error.status = res.statusCode
        error.data = body.data
        error.traceId = body.traceId
        reject(error)
      },
      fail(err) {
        const error = new Error('网络不可用，请稍后重试')
        error.code = 'NETWORK'
        reject(error)
      },
    })
  })
}

function login(phone, password) {
  return request('/auth/login', 'POST', { phone, password }).then((session) => {
    wx.setStorageSync(TOKEN_KEY, session.accessToken)
    wx.setStorageSync('campus_user', session.user)
    return session
  })
}

function isStaff(user) {
  const roles = (user && user.roles) || []
  return roles.some((role) => ['store_admin', 'system_admin', 'warehouse_operator', 'dispatcher', 'support', 'finance', 'catalog_operator'].includes(role))
}

function isCourier(user) {
  return (((user && user.roles) || []).indexOf('courier') >= 0)
}

function money(cents) {
  return '¥' + ((Number(cents) || 0) / 100).toFixed(2)
}

module.exports = { request, login, token, isStaff, isCourier, money }

