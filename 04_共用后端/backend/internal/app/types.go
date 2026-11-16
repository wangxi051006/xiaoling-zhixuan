package app

import (
	"encoding/json"
	"time"
)

type APIResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"traceId"`
}

type ItemInput struct {
	SkuID    string `json:"skuId"`
	Quantity int    `json:"quantity"`
}

type QuoteRequest struct {
	StoreID         string      `json:"storeId"`
	AddressID       string      `json:"addressId"`
	FulfillmentType string      `json:"fulfillmentType"`
	Items           []ItemInput `json:"items"`
}

type QuoteLine struct {
	SkuID          string `json:"skuId"`
	ProductID      string `json:"productId"`
	ProductName    string `json:"productName"`
	SpecName       string `json:"specName"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	Quantity       int    `json:"quantity"`
	GoodsCents     int64  `json:"goodsCents"`
	AvailableQty   int64  `json:"availableQty"`
	InStock        bool   `json:"inStock"`
	Active         bool   `json:"active"`
	ExpiryDate     string `json:"expiryDate,omitempty"`
}

type QuoteResponse struct {
	QuoteID         string      `json:"quoteId"`
	StoreID         string      `json:"storeId"`
	AddressID       string      `json:"addressId"`
	FulfillmentType string      `json:"fulfillmentType"`
	Lines           []QuoteLine `json:"lines"`
	GoodsCents      int64       `json:"goodsCents"`
	DeliveryCents   int64       `json:"deliveryCents"`
	DiscountCents   int64       `json:"discountCents"`
	TotalCents      int64       `json:"totalCents"`
	Currency        string      `json:"currency"`
	ExpiresAt       time.Time   `json:"expiresAt"`
}

type CreateOrderRequest struct {
	RequestID          string      `json:"requestId"`
	StoreID            string      `json:"storeId"`
	Items              []ItemInput `json:"items"`
	AddressID          string      `json:"addressId"`
	FulfillmentType    string      `json:"fulfillmentType"`
	QuoteID            string      `json:"quoteId"`
	ExpectedTotalCents int64       `json:"expectedTotalCents"`
	Remark             string      `json:"remark"`
}

type CreateOrderResponse struct {
	OrderID              string         `json:"orderId"`
	OrderNo              string         `json:"orderNo"`
	TotalCents           int64          `json:"totalCents"`
	Currency             string         `json:"currency"`
	ExpiresAt            time.Time      `json:"expiresAt"`
	ItemsSnapshot        []ItemSnapshot `json:"itemsSnapshot"`
	Replayed             bool           `json:"replayed"`
	CustomerOrderOrdinal int            `json:"customerOrderOrdinal"`
	IsFirstOrder         bool           `json:"isFirstOrder"`
}

type ItemSnapshot struct {
	SkuID          string `json:"skuId"`
	ProductName    string `json:"productName"`
	SpecName       string `json:"specName"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	Quantity       int    `json:"quantity"`
	GoodsCents     int64  `json:"goodsCents"`
}

type Address struct {
	ID           string `json:"id"`
	BuildingID   string `json:"buildingId"`
	BuildingName string `json:"buildingName"`
	ContactName  string `json:"contactName"`
	Phone        string `json:"phone"`
	Detail       string `json:"detail"`
	IsDefault    bool   `json:"isDefault"`
	Version      int64  `json:"version"`
}

type Product struct {
	ID          string `json:"id"`
	CategoryID  string `json:"categoryId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Version     int64  `json:"version"`
	Skus        []SKU  `json:"skus"`
}

type SKU struct {
	LowStockThreshold int64  `json:"lowStockThreshold"`
	ID                string `json:"id"`
	SpecName          string `json:"specName"`
	PriceCents        int64  `json:"priceCents"`
	Active            bool   `json:"active"`
	Available         int64  `json:"available"`
	ExpiryDate        string `json:"expiryDate,omitempty"`
}

type OrderSummary struct {
	Version              int64      `json:"version"`
	ID                   string     `json:"id"`
	OrderNo              string     `json:"orderNo"`
	BuyerID              string     `json:"buyerId"`
	BuyerName            string     `json:"buyerName"`
	Status               string     `json:"status"`
	PaymentStatus        string     `json:"paymentStatus"`
	DeliveryStatus       string     `json:"deliveryStatus"`
	TotalCents           int64      `json:"totalCents"`
	GoodsCents           int64      `json:"goodsCents"`
	DeliveryCents        int64      `json:"deliveryCents"`
	Currency             string     `json:"currency"`
	ItemCount            int        `json:"itemCount"`
	CreatedAt            time.Time  `json:"createdAt"`
	PaidAt               *time.Time `json:"paidAt,omitempty"`
	CustomerOrderCount   int64      `json:"customerOrderCount"`
	CustomerOrderOrdinal int        `json:"customerOrderOrdinal"`
	IsFirstOrder         bool       `json:"isFirstOrder"`
	IsDemo               bool       `json:"isDemo"`
	PendingUrgeCount     int64      `json:"pendingUrgeCount"`
	UrgeCount            int64      `json:"urgeCount"`
	LastUrgeAt           *time.Time `json:"lastUrgeAt,omitempty"`
	AddressText          string     `json:"addressText,omitempty"`
	AllowedActions       []string   `json:"allowedActions"`
}

type OrderItem struct {
	ID               string `json:"id"`
	SkuID            string `json:"skuId"`
	ProductName      string `json:"productName"`
	SpecName         string `json:"specName"`
	UnitPriceCents   int64  `json:"unitPriceCents"`
	Quantity         int    `json:"quantity"`
	GoodsCents       int64  `json:"goodsCents"`
	RefundedQuantity int    `json:"refundedQuantity"`
}

type PaymentAttempt struct {
	ID              string          `json:"id"`
	Channel         string          `json:"channel"`
	OutTradeNo      string          `json:"outTradeNo"`
	AmountCents     int64           `json:"amountCents"`
	Status          string          `json:"status"`
	PrepayParams    json.RawMessage `json:"prepayParams,omitempty"`
	PrepayExpiresAt *time.Time      `json:"prepayExpiresAt,omitempty"`
	Simulated       bool            `json:"simulated"`
}

type OrderDetail struct {
	OrderSummary
	Address          json.RawMessage `json:"address"`
	Remark           string          `json:"remark"`
	Items            []OrderItem     `json:"items"`
	Payment          *PaymentAttempt `json:"payment,omitempty"`
	Events           []OrderEvent    `json:"events"`
	DeliveryTask     *DeliveryTask   `json:"deliveryTask,omitempty"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	PaymentException bool            `json:"paymentException"`
	Refunds          []Refund        `json:"refunds,omitempty"`
	Urge             *UrgeSummary    `json:"urge,omitempty"`
}

type OrderEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	ActorType string          `json:"actorType"`
	ActorID   string          `json:"actorId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type DeliveryTask struct {
	DeliveryPointID      string       `json:"deliveryPointId,omitempty"`
	DeliveryPointName    string       `json:"deliveryPointName,omitempty"`
	Latitude             *float64     `json:"latitude,omitempty"`
	Longitude            *float64     `json:"longitude,omitempty"`
	RadiusMeters         *int         `json:"radiusMeters,omitempty"`
	ID                   string       `json:"id"`
	OrderID              string       `json:"orderId"`
	OrderNo              string       `json:"orderNo"`
	Status               string       `json:"status"`
	AssigneeID           string       `json:"assigneeId,omitempty"`
	AssigneeName         string       `json:"assigneeName,omitempty"`
	Version              int64        `json:"version"`
	AssignmentGeneration int          `json:"assignmentGeneration"`
	BuildingName         string       `json:"buildingName,omitempty"`
	AddressText          string       `json:"addressText,omitempty"`
	ContactName          string       `json:"contactName,omitempty"`
	Phone                string       `json:"phone,omitempty"`
	ItemCount            int          `json:"itemCount"`
	Items                []OrderItem  `json:"items,omitempty"`
	Urges                []UrgeRecord `json:"urges,omitempty"`
	CreatedAt            time.Time    `json:"createdAt"`
	AssignedAt           *time.Time   `json:"assignedAt,omitempty"`
	StartedAt            *time.Time   `json:"startedAt,omitempty"`
	CompletedAt          *time.Time   `json:"completedAt,omitempty"`
	OrderStatus          string       `json:"orderStatus"`
}

type Refund struct {
	ID          string       `json:"id"`
	OrderID     string       `json:"orderId"`
	OrderNo     string       `json:"orderNo"`
	OutRefundNo string       `json:"outRefundNo"`
	AmountCents int64        `json:"amountCents"`
	Status      string       `json:"status"`
	Reason      string       `json:"reason"`
	RequestedBy string       `json:"requestedBy"`
	SucceededAt *time.Time   `json:"succeededAt,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	Items       []RefundItem `json:"items,omitempty"`
}

type RefundItem struct {
	ID                string `json:"id"`
	SKUID             string `json:"skuId"`
	RestockedQuantity int64  `json:"restockedQuantity"`
	OrderItemID       string `json:"orderItemId"`
	ProductName       string `json:"productName,omitempty"`
	SpecName          string `json:"specName,omitempty"`
	Quantity          int    `json:"quantity"`
	AmountCents       int64  `json:"amountCents"`
}

type DailySKURow struct {
	SkuID                    string `json:"skuId"`
	ProductID                string `json:"productId"`
	ProductName              string `json:"productName"`
	SpecName                 string `json:"specName"`
	PaidQuantity             int64  `json:"paidQuantity"`
	SuccessfulRefundQuantity int64  `json:"successfulRefundQuantity"`
	NetQuantity              int64  `json:"netQuantity"`
	GoodsSalesCents          int64  `json:"goodsSalesCents"`
	GoodsRefundCents         int64  `json:"goodsRefundCents"`
	GoodsNetCents            int64  `json:"goodsNetCents"`
	PaidOrders               int64  `json:"paidOrders"`
}

type DailySKUReport struct {
	Date          string        `json:"date"`
	Timezone      string        `json:"timezone"`
	AsOf          time.Time     `json:"asOf"`
	Rows          []DailySKURow `json:"rows"`
	Totals        DailySKURow   `json:"totals"`
	DeliveryCents int64         `json:"deliveryCents"`
	DemoIncluded  bool          `json:"demoIncluded"`
	CashRefunds   []CashRefund  `json:"cashRefunds"`
}

type CashRefund struct {
	RefundID    string    `json:"refundId"`
	OrderNo     string    `json:"orderNo"`
	AmountCents int64     `json:"amountCents"`
	OccurredAt  time.Time `json:"occurredAt"`
}

type Dashboard struct {
	Date             string          `json:"date"`
	Timezone         string          `json:"timezone"`
	AsOf             time.Time       `json:"asOf"`
	PaidOrders       int64           `json:"paidOrders"`
	NewUserOrders    int64           `json:"newUserOrders"`
	ReturningOrders  int64           `json:"returningOrders"`
	GoodsSalesCents  int64           `json:"goodsSalesCents"`
	GoodsRefundCents int64           `json:"goodsRefundCents"`
	GoodsNetCents    int64           `json:"goodsNetCents"`
	DeliveryCents    int64           `json:"deliveryCents"`
	RefundCashCents  int64           `json:"refundCashCents"`
	LowStockCount    int64           `json:"lowStockCount"`
	HourlySales      []HourlySales   `json:"hourlySales"`
	DailySKU         DailySKUReport  `json:"dailySku"`
	Replenishment    []Replenishment `json:"replenishment"`
	DemoIncluded     bool            `json:"demoIncluded"`
}

type HourlySales struct {
	Hour       int   `json:"hour"`
	Orders     int64 `json:"orders"`
	SalesCents int64 `json:"salesCents"`
	Future     bool  `json:"future"`
}

type Replenishment struct {
	SkuID        string `json:"skuId"`
	ProductName  string `json:"productName"`
	SpecName     string `json:"specName"`
	AvailableQty int64  `json:"availableQty"`
	Sold7Days    int64  `json:"sold7Days"`
	SuggestedQty int64  `json:"suggestedQty"`
	Reason       string `json:"reason"`
}

type UrgePolicy struct {
	CooldownSeconds int   `json:"cooldownSeconds"`
	MaxPerOrder     int   `json:"maxPerOrder"`
	Enabled         bool  `json:"enabled"`
	Version         int64 `json:"version"`
}

type UrgeRecord struct {
	ID                 string     `json:"id"`
	OrderID            string     `json:"orderId"`
	OrderNo            string     `json:"orderNo,omitempty"`
	RequesterID        string     `json:"requesterId,omitempty"`
	RequesterName      string     `json:"requesterName,omitempty"`
	Reason             string     `json:"reason"`
	Status             string     `json:"status"`
	NotificationStatus string     `json:"notificationStatus"`
	NotificationTarget string     `json:"notificationTarget"`
	UrgeNumber         int        `json:"urgeNumber"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"createdAt"`
	HandledAt          *time.Time `json:"handledAt,omitempty"`
	HandledBy          string     `json:"handledBy,omitempty"`
	HandlerName        string     `json:"handlerName,omitempty"`
	HandleNote         string     `json:"handleNote,omitempty"`
}

type UrgeSummary struct {
	Count           int          `json:"count"`
	PendingCount    int          `json:"pendingCount"`
	MaxPerOrder     int          `json:"maxPerOrder"`
	CooldownSeconds int          `json:"cooldownSeconds"`
	Allowed         bool         `json:"allowed"`
	Reason          string       `json:"reason"`
	LastAt          *time.Time   `json:"lastAt,omitempty"`
	NextAt          *time.Time   `json:"nextAt,omitempty"`
	Records         []UrgeRecord `json:"records"`
}
