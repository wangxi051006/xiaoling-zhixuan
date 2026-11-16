const {store,executeCommand,keepPending}=require('../../../utils/pending')
const api = require('../../../utils/api')

Page({
  data: {
    addresses: [],
    addressId: '',
    selectedAddress: null,
    buildings: [],
    cart: [],
    quote: null,
    busy: false,
    error: '',
    addressOpen: false,
    pending: null,
    form: { buildingId: '', contactName: '', phone: '', detail: '' },
  },
  onShow() {
    if (!api.token()) { wx.reLaunch({ url: '/pages/login/login' }); return }
    const cart = wx.getStorageSync('campus_cart') || []
    try {this.setData({pending:store.get('campus_checkout_pending')})}catch(err){this.setData({error:err.message});return}
    if (!cart.length && !this.data.pending) {
      wx.showToast({ title: '购物车为空', icon: 'none' })
      setTimeout(() => wx.navigateBack(), 500)
      return
    }
    this.setData({ cart: cart.map((line) => ({ ...line, lineTotalText: api.money(line.priceCents * line.quantity) })) })
    this.loadAddresses()
    api.request('/stores/1/buildings').then((buildings) => this.setData({ buildings })).catch(() => {})

  },
  loadAddresses() {
    api.request('/addresses').then((addresses) => {
      const list = (addresses || []).map((item) => ({ ...item, display: item.buildingName + ' ' + item.detail }))
      const chosen = this.data.addressId || (list.find((item) => item.isDefault) || list[0] || {}).id || ''
      const selected = list.find((item) => item.id === chosen) || list[0] || null
      this.setData({ addresses: list, addressId: chosen, selectedAddress: selected })
    }).catch((err) => this.setData({ error: err.message }))
  },
  onBuilding(event) {
    const building = this.data.buildings[Number(event.detail.value)]
    if (building) this.setData({ 'form.buildingId': building.id })
  },
  onAddress(event) {
    const address = this.data.addresses[Number(event.detail.value)]
    if (address) this.setData({ addressId: address.id, selectedAddress: address })
  },
  toggleAddress() { this.setData({ addressOpen: !this.data.addressOpen }) },
  onField(event) {
    const field = event.currentTarget.dataset.field
    this.setData({ ['form.' + field]: event.detail.value })
  },
  createAddress() {
    this.setData({ busy: true, error: '' })
    api.request('/addresses', 'POST', this.data.form)
      .then((address) => {
        this.setData({ addressOpen: false, addressId: address.id })
        this.loadAddresses()
      })
      .catch((err) => this.setData({ error: err.message }))
      .finally(() => this.setData({ busy: false }))
  },
  quote() {
    if (!this.data.addressId) { this.setData({ error: '请先选择或添加本人地址' }); return }
    this.setData({ busy: true, error: '' })
    api.request('/checkout/quotes', 'POST', {
      storeId: '1', addressId: this.data.addressId, fulfillmentType: 'DELIVERY',
      items: this.data.cart.map((line) => ({ skuId: line.skuId, quantity: line.quantity })),
    })
      .then((quote) => {
        const decorated = {
          ...quote,
          goodsText: api.money(quote.goodsCents),
          deliveryText: api.money(quote.deliveryCents),
          totalText: api.money(quote.totalCents),
          expiresText: quote.expiresAt ? new Date(quote.expiresAt).toLocaleString('zh-CN', { hour12: false }) : '—',
        }
        if ((quote.lines || []).some((line) => !line.inStock || !line.active)) {
          this.setData({ quote: decorated, error: '部分商品不可售或库存不足' })
        } else {
          this.setData({ quote: decorated })
        }
      })
      .catch((err) => this.setData({ error: err.message || '报价失败' }))
      .finally(() => this.setData({ busy: false }))
  },
  submit(){return this.submitCheckout(false)},
  retryPending(){return this.submitCheckout(true)},
  async submitCheckout(retry){
    if(this.data.busy)return
    this.setData({busy:true,error:''})
    try{
      const saved=store.get('campus_checkout_pending')
      if(!retry&&saved){this.setData({pending:saved,error:'请用原请求恢复，勿重新下单'});return}
      if(retry&&!saved){this.setData({pending:null,error:'没有待确认请求，请重新报价'});return}
      if(!saved&&!this.data.quote)return
      const requestId=saved?saved.requestId:'checkout_'+Date.now()+'_'+Math.random().toString(36).slice(2,12)
      const payload=saved?saved.payload:{requestId,storeId:'1',addressId:this.data.addressId,fulfillmentType:'DELIVERY',quoteId:this.data.quote.quoteId,expectedTotalCents:this.data.quote.totalCents,remark:'',items:this.data.cart.map(line=>({skuId:line.skuId,quantity:line.quantity}))}
      const draft=saved||{requestId,payload,createdAt:new Date().toISOString()};this.setData({pending:draft})
      const {result:order,warning}=await executeCommand(store,'campus_checkout_pending',draft,p=>api.request('/orders','POST',p.payload,{'Idempotency-Key':p.requestId}))
      this.setData({pending:null});try{wx.removeStorageSync('campus_cart')}catch(ignore){}
      if(warning)wx.showToast({title:warning,icon:'none'})
      wx.redirectTo({url:'/pages/user/order-detail/order-detail?id='+order.orderId})
    }catch(err){try{this.setData({pending:store.get('campus_checkout_pending')})}catch(ignore){}this.setData({error:(err.message||'提交失败')+(keepPending(err)?'；请用原请求恢复，勿换键重下':'')});if(!keepPending(err))this.setData({quote:null})}finally{this.setData({busy:false})}
  },
})
