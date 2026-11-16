const notifications=require('../../../utils/notifications')
const api = require('../../../utils/api')

Page({
  onHide(){this.unsubscribeNotifications?.()},onUnload(){this.unsubscribeNotifications?.()},
  goMessages(){wx.navigateTo({url:'/pages/user/messages/messages'})},
  data: {
    user: null,
    categories: [],
    products: [],
    filtered: [],
    categoryId: '',
    keyword: '',
    cart: [],
    cartCount: 0,
    cartTotal: '¥0.00',
    loading: true,
    error: '',
  },
  onShow() {
    this.unsubscribeNotifications?.();this.unsubscribeNotifications=notifications.subscribe(state=>this.setData({notificationUnread:state.notificationUnread}));notifications.start();
    if (!api.token()) {
      wx.reLaunch({ url: '/pages/login/login' })
      return
    }
    const user = wx.getStorageSync('campus_user')
    this.setData({ user })
    this.loadCart()
    this.load()
  },
  load() {
    this.setData({ loading: true, error: '' })
    Promise.all([api.request('/categories'), api.request('/products')])
      .then(([categories, products]) => {
        const decorated = (products || []).map((product) => {
          const sku = (product.skus || []).find((item) => item.active) || (product.skus || [])[0] || {}
          return {
            ...product,
            skuId: sku.id,
            specName: sku.specName || '标准',
            priceCents: sku.priceCents || 0,
            priceText: api.money(sku.priceCents || 0),
            available: sku.available || 0,
            soldOut: !sku.active || !sku.available,
            emoji: product.name.indexOf('茶') >= 0 || product.name.indexOf('奶') >= 0 ? '🧋' : product.name.indexOf('曲奇') >= 0 ? '🍪' : '🍟',
          }
        })
        this.setData({ categories: categories || [], products: decorated })
        this.filter()
      })
      .catch((err) => this.setData({ error: err.message || '加载失败' }))
      .finally(() => this.setData({ loading: false }))
  },
  loadCart() {
    const cart = wx.getStorageSync('campus_cart') || []
    const count = cart.reduce((sum, line) => sum + line.quantity, 0)
    const cents = cart.reduce((sum, line) => sum + line.priceCents * line.quantity, 0)
    this.setData({ cart, cartCount: count, cartTotal: api.money(cents) })
  },
  filter() {
    const { products, categoryId, keyword } = this.data
    const filtered = products.filter((product) => {
      if (categoryId && product.categoryId !== categoryId) return false
      if (keyword && (product.name + product.description).indexOf(keyword) < 0) return false
      return true
    })
    this.setData({ filtered })
  },
  chooseCategory(event) {
    this.setData({ categoryId: event.currentTarget.dataset.id || '' }, () => this.filter())
  },
  onKeyword(event) {
    this.setData({ keyword: event.detail.value || '' }, () => this.filter())
  },
  add(event) {
    const id = event.currentTarget.dataset.id
    const product = this.data.products.find((item) => item.id === id)
    if (!product || product.soldOut) return
    const cart = this.data.cart.slice()
    const existing = cart.find((line) => line.skuId === product.skuId)
    if (existing) {
      if (existing.quantity >= Math.min(product.available, 99)) {
        wx.showToast({ title: '已达可购上限', icon: 'none' })
        return
      }
      existing.quantity += 1
    } else {
      cart.push({ skuId: product.skuId, productId: product.id, name: product.name, spec: product.specName, priceCents: product.priceCents, quantity: 1, available: product.available })
    }
    wx.setStorageSync('campus_cart', cart)
    this.loadCart()
    wx.showToast({ title: '已加入购物车', icon: 'none' })
  },
  goCheckout() {
    if (!this.data.cartCount) {
      wx.showToast({ title: '购物车为空', icon: 'none' })
      return
    }
    wx.navigateTo({ url: '/pages/user/checkout/checkout' })
  },
  goOrders() { wx.navigateTo({ url: '/pages/user/orders/orders' }) },
  goProfile() { wx.navigateTo({ url: '/pages/user/profile/profile' }) },
})

