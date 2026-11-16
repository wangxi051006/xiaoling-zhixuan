package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"campusretail/internal/config"
)

type App struct {
	cfg         config.Config
	db          *sql.DB
	log         *slog.Logger
	tz          *time.Location
	mux         *http.ServeMux
	wechat      *wechatClient
	now         func() time.Time
	stopWorkers context.CancelFunc
	workers     sync.WaitGroup
}

func New(cfg config.Config, database *sql.DB, logger *slog.Logger) (*App, error) {
	a := &App{
		cfg: cfg,
		db:  database,
		log: logger,
		tz:  loadShanghai(),
		mux: http.NewServeMux(),
		now: time.Now,
	}
	a.wechat = newWeChatClient(cfg)
	a.registerRoutes()
	ctx, cancel := context.WithCancel(context.Background())
	a.stopWorkers = cancel
	for _, worker := range []func(context.Context){a.startPaymentWorker, a.startExpiryWorker, a.startUrgeOutboxWorker, a.startDeliveryNotificationWorker} {
		a.workers.Add(1)
		go func(run func(context.Context)) { defer a.workers.Done(); run(ctx) }(worker)
	}
	return a, nil
}

// Close stops background work before the database is closed.
func (a *App) Close() { a.stopWorkers(); a.workers.Wait() }

func (a *App) registerRoutes() {
	a.registerNotificationRoutes()
	a.registerDeliveryUpgradeRoutes()
	// Public.
	a.mux.HandleFunc("GET /healthz", a.health)
	a.mux.HandleFunc("POST /api/v1/auth/login", a.login)
	a.mux.HandleFunc("POST /api/v1/auth/staff/login", a.staffLogin)
	a.mux.HandleFunc("POST /api/v1/auth/logout", a.auth(a.logout))
	a.mux.HandleFunc("GET /api/v1/me", a.auth(a.me))
	a.mux.HandleFunc("GET /api/v1/me/frequent", a.auth(a.frequentItems))
	a.mux.HandleFunc("GET /api/v1/stores/{id}/buildings", a.listBuildings)
	a.mux.HandleFunc("GET /api/v1/categories", a.listCategories)
	a.mux.HandleFunc("GET /api/v1/products", a.listProducts)
	a.mux.HandleFunc("GET /api/v1/products/{id}", a.getProduct)
	a.mux.HandleFunc("GET /api/v1/scenes", a.listScenes)

	// Customer.
	a.mux.HandleFunc("GET /api/v1/addresses", a.auth(a.listAddresses))
	a.mux.HandleFunc("POST /api/v1/addresses", a.auth(a.createAddress))
	a.mux.HandleFunc("PATCH /api/v1/addresses/{id}", a.auth(a.updateAddress))
	a.mux.HandleFunc("DELETE /api/v1/addresses/{id}", a.auth(a.deleteAddress))
	a.mux.HandleFunc("POST /api/v1/addresses/{id}/make-default", a.auth(a.makeDefaultAddress))
	a.mux.HandleFunc("POST /api/v1/checkout/quotes", a.auth(a.createQuote))
	a.mux.HandleFunc("POST /api/v1/orders", a.auth(a.createOrder))
	a.mux.HandleFunc("GET /api/v1/orders", a.auth(a.listOrders))
	a.mux.HandleFunc("GET /api/v1/orders/{id}", a.auth(a.getOrder))
	a.mux.HandleFunc("POST /api/v1/orders/{id}/cancel", a.auth(a.cancelOrder))
	a.mux.HandleFunc("POST /api/v1/orders/{id}/after-sales", a.auth(a.createAfterSale))
	a.mux.HandleFunc("POST /api/v1/orders/{id}/urges", a.auth(a.createUrge))
	a.mux.HandleFunc("POST /api/v1/orders/{id}/payment-attempt", a.auth(a.createPaymentAttempt))
	a.mux.HandleFunc("POST /api/v1/orders/{id}/refresh-payment", a.auth(a.refreshPayment))
	a.mux.HandleFunc("GET /api/v1/orders/{id}/receipt-code", a.auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, r, http.StatusGone, "RECEIPT_CODE_RETIRED", "已改为骑手到达配送点后确认送达，无需验证码")
	}))
	a.mux.HandleFunc("POST /api/v1/test/payments/{id}/succeed", a.auth(a.simulatedPayment))

	// Admin.
	a.mux.HandleFunc("GET /api/v1/admin/workbench", a.staff("order_read", a.adminWorkbench))
	a.mux.HandleFunc("GET /api/v1/admin/orders", a.staff("order_read", a.adminOrders))
	a.mux.HandleFunc("GET /api/v1/admin/orders/{id}", a.staff("order_read", a.adminOrderDetail))
	a.mux.HandleFunc("POST /api/v1/admin/orders/{id}/actions", a.staff("order_manage", a.adminOrderAction))
	a.mux.HandleFunc("POST /api/v1/admin/orders/batch-actions", a.staff("order_manage", a.adminBatchOrderAction))
	a.mux.HandleFunc("GET /api/v1/admin/orders/batch-actions", a.staff("order_read", a.adminFindBatch))
	a.mux.HandleFunc("GET /api/v1/admin/orders/batch-actions/{id}", a.staff("order_read", a.adminBatchOrderDetail))
	a.mux.HandleFunc("POST /api/v1/admin/orders/batch-actions/{id}/resume", a.staff("order_manage", a.adminResumeBatch))
	a.mux.HandleFunc("GET /api/v1/admin/products", a.staff("product_read", a.adminProducts))
	a.mux.HandleFunc("POST /api/v1/admin/products", a.staff("product_write", a.adminCreateProduct))
	a.mux.HandleFunc("PATCH /api/v1/admin/products/{id}", a.staff("product_write", a.adminUpdateProduct))
	a.mux.HandleFunc("GET /api/v1/admin/inventory", a.staff("inventory_read", a.adminInventory))
	a.mux.HandleFunc("POST /api/v1/admin/inventory/receipts", a.staff("inventory_write", a.adminReceipt))
	a.mux.HandleFunc("POST /api/v1/admin/inventory/waste", a.staff("inventory_write", a.adminWaste))
	a.mux.HandleFunc("GET /api/v1/admin/refunds", a.staff("refund_read", a.adminRefunds))
	a.mux.HandleFunc("GET /api/v1/admin/refunds/{id}", a.staff("refund_read", a.adminRefundDetail))
	a.mux.HandleFunc("POST /api/v1/admin/refunds", a.staff("refund_write", a.adminCreateRefund))
	a.mux.HandleFunc("GET /api/v1/admin/urges", a.staff("order_read", a.adminUrges))
	a.mux.HandleFunc("POST /api/v1/admin/urges/{id}/handle", a.staff("urge_manage", a.adminHandleUrge))
	a.mux.HandleFunc("GET /api/v1/admin/urge-policy", a.staff("order_read", a.adminUrgePolicy))
	a.mux.HandleFunc("PUT /api/v1/admin/urge-policy", a.staff("order_manage", a.adminUpdateUrgePolicy))
	a.mux.HandleFunc("GET /api/v1/admin/urge-notifications", a.staff("order_read", a.adminUrgeNotifications))
	a.mux.HandleFunc("POST /api/v1/admin/urge-notifications/{id}/read", a.staff("order_read", a.adminReadUrgeNotification))
	a.mux.HandleFunc("POST /api/v1/admin/refunds/{id}/restock", a.staff("inventory_write", a.adminRefundRestock))
	a.mux.HandleFunc("POST /api/v1/test/refunds/{id}/succeed", a.staff("refund_write", a.simulatedRefund))
	a.mux.HandleFunc("GET /api/v1/admin/staff", a.staff("staff_read", a.adminStaff))
	a.mux.HandleFunc("PATCH /api/v1/admin/staff/{id}/roles", a.staff("staff_write", a.adminUpdateStaffRoles))
	a.mux.HandleFunc("GET /api/v1/admin/audit-logs", a.staff("audit_read", a.adminAudit))
	a.mux.HandleFunc("GET /api/v1/admin/reports/dashboard", a.staff("report_read", a.adminDashboard))
	a.mux.HandleFunc("GET /api/v1/admin/reports/daily-sku-sales", a.staff("report_read", a.adminDailySKU))
	a.mux.HandleFunc("POST /api/v1/admin/delivery-tasks/{id}/assign", a.staff("dispatch", a.adminAssignTask))
	a.mux.HandleFunc("POST /api/v1/admin/delivery-tasks/{id}/reassign", a.staff("dispatch", a.adminReassignTask))
	a.mux.HandleFunc("POST /api/v1/admin/delivery-tasks/{id}/resume", a.staff("dispatch", a.adminResumeTask))
	a.mux.HandleFunc("GET /api/v1/admin/courier-candidates", a.staff("dispatch", a.adminCourierCandidates))

	// Courier.
	a.mux.HandleFunc("GET /api/v1/courier/tasks/available", a.staff("courier", a.courierAvailable))
	a.mux.HandleFunc("GET /api/v1/courier/tasks", a.staff("courier", a.courierTasks))
	a.mux.HandleFunc("GET /api/v1/courier/tasks/{id}", a.staff("courier", a.courierTask))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/claim", a.staff("courier", a.courierClaim))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/pickup", a.staff("courier", a.courierPickup))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/start", a.staff("courier", a.courierStart))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/complete", a.staff("courier", a.courierComplete))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/exceptions", a.staff("courier", a.courierException))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/resume", a.staff("courier", a.courierResume))

	// WeChat Pay APIv3 notifications: no JWT, but APIv3 signature is mandatory.
	a.mux.HandleFunc("POST /api/v1/webhooks/wechatpay/payments", a.wechatPaymentNotify)
	a.mux.HandleFunc("POST /api/v1/webhooks/wechatpay/refunds", a.wechatRefundNotify)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := newTraceID()
	if header := strings.TrimSpace(r.Header.Get("X-Request-Id")); header != "" && len(header) <= 64 {
		traceID = header
	}
	r = r.WithContext(context.WithValue(r.Context(), traceKey{}, traceID))
	w.Header().Set("X-Request-Id", traceID)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=()")
	a.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.log.Error("panic recovered", "traceId", traceID, "path", r.URL.Path, "panic", recovered)
			if !recorder.wrote {
				writeJSONError(recorder, r, http.StatusInternalServerError, "INTERNAL_ERROR", "服务器内部错误")
			}
		}
		a.log.Info("http",
			"traceId", traceID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	}()
	a.mux.ServeHTTP(recorder, r)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(body)
}

func (a *App) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return
	}
	allowed := a.cfg.Env != "production" &&
		(origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" || origin == "http://127.0.0.1:8080")
	if !allowed {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-Id, X-Simulated-Payment")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		writeJSONError(w, r, http.StatusServiceUnavailable, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	// Surfacing the applied migration makes "connected to the wrong (old) database"
	// visible to acceptance checks instead of failing later inside a business call.
	var schemaVersion string
	_ = a.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),'') FROM schema_migrations`).Scan(&schemaVersion)
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"status":        "ok",
		"time":          time.Now().UTC().Format(time.RFC3339),
		"paymentMode":   a.cfg.PaymentMode,
		"schemaVersion": schemaVersion,
	})
}
