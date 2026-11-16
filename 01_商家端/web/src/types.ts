export type SessionUser = {
  id: string
  nickname: string
  phone: string
  roles: string[]
  storeId: string
  isDemo: boolean
}

export type Session = { accessToken: string; user: SessionUser }

export type ProductSKU = {
	lowStockThreshold: number
  id: string
  specName: string
  priceCents: number
  active: boolean
  available: number
  expiryDate?: string
}

export type Product = {
  id: string
  categoryId: string
  name: string
  description: string
  active: boolean
  version: number
  skus: ProductSKU[]
}

export type Category = { id: string; name: string; sortOrder: number; productCount: number }
export type Building = { id: string; name: string; deliveryFeeCents: number }
export type Address = {
  id: string
  buildingId: string
  buildingName: string
  contactName: string
  phone: string
  detail: string
  isDefault: boolean
  version: number
  deliveryFeeCents: number
}

export type OrderSummary = {
  id: string
  version: number
  orderNo: string
  buyerId: string
  buyerName: string
  status: string
  paymentStatus: string
  deliveryStatus: string
  goodsCents: number
  deliveryCents: number
  totalCents: number
  currency: string
  itemCount: number
  createdAt: string
  paidAt?: string
  customerOrderCount: number
  customerOrderOrdinal: number
  isFirstOrder: boolean
  isDemo: boolean
  pendingUrgeCount: number
  urgeCount: number
  lastUrgeAt?: string
  addressText?: string
  allowedActions: string[]
}

export type OrderItem = {
  id: string
  skuId: string
  productName: string
  specName: string
  unitPriceCents: number
  quantity: number
  goodsCents: number
  refundedQuantity: number
}

export type OrderDetail = OrderSummary & {
  address: Record<string, unknown>
  remark: string
  items: OrderItem[]
  payment?: {
    id: string
    channel: string
    outTradeNo: string
    amountCents: number
    status: string
    simulated: boolean
    prepayParams?: Record<string, unknown>
  }
  events: Array<{ id: string; type: string; actorType: string; createdAt: string }>
  deliveryTask?: DeliveryTask
  expiresAt: string
  version: number
  paymentException: boolean
  refunds?: Refund[]
  urge?: UrgeSummary
}

export type DeliveryTask = {
  id: string
  orderId: string
  orderNo: string
  status: string
  assigneeId?: string
  assigneeName?: string
  version: number
  assignmentGeneration: number
  buildingName?: string
  addressText?: string
  contactName?: string
  phone?: string
  itemCount: number
  items?: OrderItem[]
  urges?: UrgeRecord[]
  createdAt: string
  orderStatus: string
  deliveryPointId?: string
  deliveryPointName?: string
  latitude?: number
  longitude?: number
  radiusMeters?: number
}

export type DeliveryPoint = { id: string; buildingId: string; name: string; latitude: number; longitude: number; radiusMeters: number; active: boolean; version: number }

export type UrgeRecord = {
  id: string
  orderId: string
  orderNo?: string
  requesterId?: string
  requesterName?: string
  reason: string
  status: 'RECEIVED' | 'HANDLED' | string
  notificationStatus: 'PENDING' | 'SENT' | 'FAILED' | 'CANCELLED' | string
  notificationTarget: 'STORE' | 'STORE_AND_COURIER' | string
  urgeNumber: number
  version: number
  createdAt: string
  handledAt?: string
  handledBy?: string
  handlerName?: string
  handleNote?: string
}

export type UrgeSummary = {
  count: number
  pendingCount: number
  maxPerOrder: number
  cooldownSeconds: number
  allowed: boolean
  reason: string
  lastAt?: string
  nextAt?: string
  records: UrgeRecord[]
}

export type Refund = {  id: string
  orderId: string
  orderNo: string
  outRefundNo: string
  amountCents: number
  status: string
  reason: string
  requestedBy: string
  succeededAt?: string
  createdAt: string
  items?: Array<{ id:string;skuId:string;restockedQuantity:number;orderItemId: string; productName?: string; specName?: string; quantity: number; amountCents: number }>
}

export type DailySKURow = {
  skuId: string
  productId: string
  productName: string
  specName: string
  paidQuantity: number
  successfulRefundQuantity: number
  netQuantity: number
  goodsSalesCents: number
  goodsRefundCents: number
  goodsNetCents: number
  paidOrders: number
}

export type Dashboard = {
  date: string
  timezone: string
  asOf: string
  paidOrders: number
  newUserOrders: number
  returningOrders: number
  goodsSalesCents: number
  goodsRefundCents: number
  goodsNetCents: number
  deliveryCents: number
  refundCashCents: number
  lowStockCount: number
  hourlySales: Array<{ hour: number; orders: number; salesCents: number; future: boolean }>
  dailySku: {
    date: string
    timezone: string
    asOf: string
    rows: DailySKURow[]
    totals: DailySKURow
    deliveryCents: number
    demoIncluded: boolean
    cashRefunds: Array<{ refundId: string; orderNo: string; amountCents: number; occurredAt: string }>
  }
  replenishment: Array<{
    skuId: string
    productName: string
    specName: string
    availableQty: number
    sold7Days: number
    suggestedQty: number
    reason: string
  }>
  demoIncluded: boolean
}

export type Quote = {
  quoteId: string
  goodsCents: number
  deliveryCents: number
  discountCents: number
  totalCents: number
  currency: string
  expiresAt: string
  lines: Array<{
    skuId: string
    productName: string
    specName: string
    unitPriceCents: number
    quantity: number
    goodsCents: number
    availableQty: number
    inStock: boolean
    active: boolean
  }>
}

export type CourierCandidate = {id:string;nickname:string;online:boolean;activeTaskCount:number;taskLimit:number}
