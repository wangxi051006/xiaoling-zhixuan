package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// auditTx writes an append-only audit row inside the caller's transaction.
// Every caller must treat a failure as fatal: a price/stock/role change that
// commits without its audit trail cannot be reconstructed afterwards.
func auditTx(ctx context.Context, tx *sql.Tx, actorID, storeID int64, action, objectType, objectID string, before, after any, traceID string) error {
	var beforeJSON, afterJSON any
	if before != nil {
		beforeJSON = mustJSON(before)
	}
	if after != nil {
		afterJSON = mustJSON(after)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,before_json,after_json,trace_id)
		VALUES (?,?,?,?,?,?,?,?)`, actorID, storeID, action, objectType, objectID, beforeJSON, afterJSON, traceID)
	return err
}

type adminActionRequest struct {
	Action          string `json:"action"`
	ExpectedVersion int64  `json:"expectedVersion"`
	Note            string `json:"note"`
}

type skuInput struct {
	LowStockThreshold *int64 `json:"lowStockThreshold"`
	ID                string `json:"id"`
	SpecName          string `json:"specName"`
	PriceCents        int64  `json:"priceCents"`
	Active            bool   `json:"active"`
}

const maxPriceCents int64 = 99999999

func validSKUInput(sku skuInput) bool {
	return strings.TrimSpace(sku.SpecName) != "" && sku.PriceCents >= 0 && sku.PriceCents <= maxPriceCents &&
		(sku.LowStockThreshold == nil || (*sku.LowStockThreshold >= 0 && *sku.LowStockThreshold <= 2147483647))
}

func skuThreshold(sku skuInput) int64 {
	if sku.LowStockThreshold != nil {
		return *sku.LowStockThreshold
	}
	return 5
}

func productSKUSnapshot(ctx context.Context, tx *sql.Tx, productID int64) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,spec_name,price_cents,active,low_stock_threshold FROM skus WHERE product_id=? ORDER BY id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, price, threshold int64
		var spec string
		var active bool
		if err := rows.Scan(&id, &spec, &price, &active, &threshold); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": toStringID(id), "specName": spec, "priceCents": price, "active": active, "lowStockThreshold": threshold})
	}
	return items, rows.Err()
}

type productInput struct {
	Name            string     `json:"name"`
	CategoryID      string     `json:"categoryId"`
	Description     string     `json:"description"`
	Active          bool       `json:"active"`
	ExpectedVersion int64      `json:"expectedVersion"`
	Skus            []skuInput `json:"skus"`
}

type receiptInput struct {
	SkuID         string `json:"skuId"`
	BatchNo       string `json:"batchNo"`
	ExpiryDate    string `json:"expiryDate"`
	Quantity      int64  `json:"quantity"`
	UnitCostCents int64  `json:"unitCostCents"`
}

type wasteInput struct {
	LotID    string `json:"lotId"`
	Quantity int64  `json:"quantity"`
	Reason   string `json:"reason"`
}

type staffRolesInput struct {
	Roles           []string `json:"roles"`
	ExpectedVersion int64    `json:"expectedVersion"`
}

func (a *App) adminWorkbench(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	counts := map[string]int64{
		"PENDING_PAYMENT": 0, "PAID": 0, "PICKING": 0, "READY": 0,
		"DELIVERING": 0, "COMPLETED": 0, "PAYMENT_EXCEPTION": 0, "CANCELLED": 0,
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT status,COUNT(*) FROM orders WHERE store_id=? GROUP BY status`, p.Staff.StoreID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int64
			if err := rows.Scan(&status, &count); err == nil {
				counts[status] = count
			}
		}
	}
	query := `SELECT o.id,o.order_no,o.buyer_id,u.nickname,o.status,o.payment_status,o.delivery_status,
		o.goods_cents,o.delivery_cents,o.total_cents,o.currency,o.created_at,o.paid_at,o.address_json,
		o.customer_order_ordinal,o.is_first_order,o.is_demo,COALESCE(c.total_created,0),
		(SELECT COALESCE(SUM(quantity),0) FROM order_items oi WHERE oi.order_id=o.id),
		(SELECT COUNT(*) FROM order_urges u WHERE u.order_id=o.id),
		(SELECT COUNT(*) FROM order_urges u WHERE u.order_id=o.id AND u.status='RECEIVED'),
		(SELECT MAX(u.created_at) FROM order_urges u WHERE u.order_id=o.id),o.version
		FROM orders o JOIN users u ON u.id=o.buyer_id
		LEFT JOIN customer_order_counters c ON c.user_id=o.buyer_id
		WHERE o.store_id=? AND o.status IN ('PAID','PICKING','READY','DELIVERING','PAYMENT_EXCEPTION')
		ORDER BY o.created_at DESC,o.id DESC LIMIT 20`
	queue, apiErr := a.queryOrderSummaries(r.Context(), query, []any{p.Staff.StoreID}, true)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	var pendingUrges, unreadUrgeNotifications int64
	_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM order_urges WHERE store_id=? AND status='RECEIVED'`, p.Staff.StoreID).Scan(&pendingUrges)
	_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM notifications WHERE recipient_type='STORE' AND recipient_id=? AND read_at IS NULL`, p.Staff.StoreID).Scan(&unreadUrgeNotifications)
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"counts": counts, "queue": queue, "snapshotAt": a.now().UTC(), "storeId": toStringID(p.Staff.StoreID),
		"pendingUrges": pendingUrges, "unreadUrgeNotifications": unreadUrgeNotifications,
	})
}

func (a *App) adminOrderAction(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req adminActionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	action := strings.ToUpper(strings.TrimSpace(req.Action))
	if !isOneOf(action, "ACCEPT", "PICKING", "READY", "CLOSE") {
		writeError(w, r, badRequest("INVALID_ACTION", "不支持的订单命令"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理订单失败"})
		return
	}
	defer tx.Rollback()
	var status, paymentStatus string
	var version int64
	err = tx.QueryRowContext(r.Context(), `SELECT status,payment_status,version FROM orders WHERE id=? AND store_id=? FOR UPDATE`,
		orderID, p.Staff.StoreID).Scan(&status, &paymentStatus, &version)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("订单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取订单失败"})
		return
	}
	if version != req.ExpectedVersion {
		writeError(w, r, conflict("VERSION_CONFLICT", "订单已被其他人更新，请刷新"))
		return
	}
	target := ""
	switch action {
	case "ACCEPT", "PICKING", "READY":
		var apiErr *apiError
		target, apiErr = orderActionTarget(action, status, paymentStatus)
		if apiErr != nil {
			writeError(w, r, apiErr)
			return
		}
	case "CLOSE":
		if status != "PENDING_PAYMENT" {
			writeError(w, r, conflict("INVALID_STATE", "只有待支付订单可以关闭"))
			return
		}
		var attemptStatus, channel string
		attemptErr := tx.QueryRowContext(r.Context(), `SELECT status,channel FROM payment_attempts WHERE order_id=? FOR UPDATE`, orderID).
			Scan(&attemptStatus, &channel)
		if attemptErr != nil && !errors.Is(attemptErr, sql.ErrNoRows) {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取支付意图失败，库存已保留"})
			return
		}
		if attemptErr == nil && (isOneOf(attemptStatus, "PAID", "UNKNOWN") || (channel != "SIMULATED" && attemptStatus != "CLOSED")) {
			writeError(w, r, conflict("PAYMENT_NOT_CLOSED", "支付结果未明，不能直接释放库存"))
			return
		}
		if attemptErr == nil {
			if _, err := tx.ExecContext(r.Context(), `UPDATE payment_attempts SET status='CLOSED',version=version+1 WHERE order_id=?`, orderID); err != nil {
				writeError(w, r, &apiError{500, "DB_ERROR", "关闭支付意图失败"})
				return
			}
		}
		if err := a.releaseReservations(r.Context(), tx, orderID); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "释放库存失败"})
			return
		}
		target = "CLOSED"
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE orders SET status=?,payment_status=IF(?='CLOSED','CLOSED',payment_status),version=version+1 WHERE id=? AND version=?`, target, target, orderID, version); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理订单失败"})
		return
	}
	eventType := map[string]string{"PICKING": "ORDER_PICKING", "READY": "ORDER_READY", "CLOSED": "ORDER_CLOSED"}[target]
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,?,'STAFF',?,?)`,
		orderID, eventType, p.UserID, mustJSON(map[string]any{"note": truncate(req.Note, 200)})); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入订单事件失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id)
		VALUES (?,?,?,?,?,?,?)`, p.UserID, p.Staff.StoreID, "ORDER_"+action, "ORDER", toStringID(orderID),
		mustJSON(map[string]any{"from": status, "to": target, "version": version + 1}), traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入订单审计失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理订单失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "status": target, "version": version + 1})
}

func (a *App) adminProducts(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,category_id,name,description,active,version FROM products
		WHERE store_id=? ORDER BY id`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取商品失败"})
		return
	}
	defer rows.Close()
	items := []Product{}
	for rows.Next() {
		var product Product
		var id, categoryID int64
		var active bool
		if err := rows.Scan(&id, &categoryID, &product.Name, &product.Description, &active, &product.Version); err != nil {
			continue
		}
		product.ID = toStringID(id)
		product.CategoryID = toStringID(categoryID)
		product.Active = active
		product.Skus = a.loadSKUs(r, id, true)
		items = append(items, product)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) adminCreateProduct(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req productInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Skus) == 0 {
		writeError(w, r, badRequest("INVALID_PRODUCT", "商品名称和至少一个规格不能为空"))
		return
	}
	categoryID, err := parseID(req.CategoryID, "categoryId")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	for _, sku := range req.Skus {
		if !validSKUInput(sku) {
			writeError(w, r, badRequest("INVALID_SKU", "规格名称、价格（0–999999.99元）和非负整数预警阈值必须有效"))
			return
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建商品失败"})
		return
	}
	defer tx.Rollback()
	var categoryCount int
	_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM categories WHERE id=? AND store_id=?`, categoryID, p.Staff.StoreID).Scan(&categoryCount)
	if categoryCount != 1 {
		writeError(w, r, badRequest("INVALID_CATEGORY", "分类不存在"))
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO products(store_id,category_id,name,description,active)
		VALUES (?,?,?,?,?)`, p.Staff.StoreID, categoryID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), boolInt(req.Active))
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建商品失败"})
		return
	}
	productID, _ := result.LastInsertId()
	for _, sku := range req.Skus {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO skus(store_id,product_id,spec_name,price_cents,active,low_stock_threshold) VALUES (?,?,?,?,?,?)`,
			p.Staff.StoreID, productID, strings.TrimSpace(sku.SpecName), sku.PriceCents, boolInt(sku.Active), skuThreshold(sku)); err != nil {
			writeError(w, r, conflict("SKU_DUPLICATE", "规格名称重复"))
			return
		}
	}
	if err := auditTx(r.Context(), tx, p.UserID, p.Staff.StoreID, "CREATE_PRODUCT", "PRODUCT", toStringID(productID), nil,
		map[string]any{"name": req.Name, "skus": req.Skus}, traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建商品失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建商品失败"})
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), map[string]any{"id": toStringID(productID)})
}

func (a *App) adminUpdateProduct(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	productID, err := parseID(r.PathValue("id"), "商品ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req productInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if req.ExpectedVersion <= 0 {
		writeError(w, r, badRequest("EXPECTED_VERSION_REQUIRED", "更新商品必须提供 expectedVersion"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, r, badRequest("INVALID_PRODUCT", "商品名称不能为空"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新商品失败"})
		return
	}
	defer tx.Rollback()
	// Snapshot the real current values first; the audit row must describe what the
	// database actually held, never what the client claims it held.
	before := map[string]any{}
	var bName, bDesc string
	var bActive int
	var currentVersion int64
	if err := tx.QueryRowContext(r.Context(), `SELECT name,description,active,version FROM products WHERE id=? AND store_id=? FOR UPDATE`,
		productID, p.Staff.StoreID).Scan(&bName, &bDesc, &bActive, &currentVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, notFound("商品不存在"))
		} else {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取商品失败"})
		}
		return
	}
	if currentVersion != req.ExpectedVersion {
		writeError(w, r, conflict("VERSION_CONFLICT", "商品已被其他操作更新，请刷新"))
		return
	}
	before["name"], before["description"], before["active"] = bName, bDesc, bActive == 1
	beforeSkus, err := productSKUSnapshot(r.Context(), tx, productID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取规格审计快照失败"})
		return
	}
	before["skus"] = beforeSkus
	result, err := tx.ExecContext(r.Context(), `UPDATE products SET name=?,description=?,active=?,version=version+1
		WHERE id=? AND store_id=? AND version=?`, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), boolInt(req.Active), productID, p.Staff.StoreID, req.ExpectedVersion)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新商品失败"})
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, r, conflict("VERSION_CONFLICT", "商品已被其他操作更新，请刷新"))
		return
	}
	for _, sku := range req.Skus {
		if !validSKUInput(sku) {
			writeError(w, r, badRequest("INVALID_SKU", "规格名称、价格（0–999999.99元）和非负整数预警阈值必须有效"))
			return
		}
		if strings.TrimSpace(sku.ID) == "" {
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO skus(store_id,product_id,spec_name,price_cents,active,low_stock_threshold) VALUES (?,?,?,?,?,?)`,
				p.Staff.StoreID, productID, strings.TrimSpace(sku.SpecName), sku.PriceCents, boolInt(sku.Active), skuThreshold(sku)); err != nil {
				writeError(w, r, conflict("SKU_DUPLICATE", "新增规格名称重复"))
				return
			}
			continue
		}
		skuID, err := parseID(sku.ID, "skuId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		result, err := tx.ExecContext(r.Context(), `UPDATE skus SET spec_name=?,price_cents=?,active=?,low_stock_threshold=COALESCE(?,low_stock_threshold),version=version+1 WHERE id=? AND product_id=? AND store_id=?`,
			strings.TrimSpace(sku.SpecName), sku.PriceCents, boolInt(sku.Active), sku.LowStockThreshold, skuID, productID, p.Staff.StoreID)
		if err != nil {
			writeError(w, r, conflict("SKU_DUPLICATE", "规格名称重复"))
			return
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			writeError(w, r, notFound("规格不属于当前商品"))
			return
		}
	}
	afterSkus, err := productSKUSnapshot(r.Context(), tx, productID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取规格审计快照失败"})
		return
	}
	after := map[string]any{"name": strings.TrimSpace(req.Name), "description": strings.TrimSpace(req.Description), "active": req.Active, "skus": afterSkus}
	if err := auditTx(r.Context(), tx, p.UserID, p.Staff.StoreID, "UPDATE_PRODUCT", "PRODUCT", toStringID(productID), before, after, traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新商品失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新商品失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "version": req.ExpectedVersion + 1})
}

func (a *App) adminInventory(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT l.id,l.sku_id,p.name,s.spec_name,l.batch_no,l.expiry_date,
		l.qty_on_hand,l.qty_reserved,l.unit_cost_cents,l.version
		FROM inventory_lots l JOIN skus s ON s.id=l.sku_id JOIN products p ON p.id=s.product_id
		WHERE l.store_id=? ORDER BY p.id,s.id,l.expiry_date,l.id`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取库存失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, skuID, onHand, reserved, cost, version int64
		var productName, specName, batchNo string
		var expiry time.Time
		if err := rows.Scan(&id, &skuID, &productName, &specName, &batchNo, &expiry, &onHand, &reserved, &cost, &version); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": toStringID(id), "skuId": toStringID(skuID), "productName": productName, "specName": specName,
			"batchNo": batchNo, "expiryDate": expiry.Format("2006-01-02"), "qtyOnHand": onHand,
			"qtyReserved": reserved, "available": onHand - reserved, "unitCostCents": cost, "version": version,
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) adminReceipt(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req receiptInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	skuID, err := parseID(req.SkuID, "skuId")
	if err != nil || req.Quantity <= 0 || req.UnitCostCents < 0 {
		writeError(w, r, badRequest("INVALID_RECEIPT", "入库数量、成本和 SKU 必须有效"))
		return
	}
	expiry, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(req.ExpiryDate), a.tz)
	today := a.now().In(a.tz)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, a.tz)
	if err != nil || expiry.Before(today) {
		writeError(w, r, badRequest("INVALID_EXPIRY", "到期日必须是今天或未来日期"))
		return
	}
	if strings.TrimSpace(req.BatchNo) == "" {
		writeError(w, r, badRequest("BATCH_REQUIRED", "请填写批次号"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	defer tx.Rollback()
	var skuStore int64
	if err := tx.QueryRowContext(r.Context(), `SELECT store_id FROM skus WHERE id=? FOR UPDATE`, skuID).Scan(&skuStore); err != nil || skuStore != p.Staff.StoreID {
		writeError(w, r, notFound("SKU 不存在"))
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO inventory_lots(store_id,sku_id,batch_no,expiry_date,qty_on_hand,qty_reserved,unit_cost_cents)
		VALUES (?,?,?,?,?,0,?) ON DUPLICATE KEY UPDATE qty_on_hand=qty_on_hand+VALUES(qty_on_hand),unit_cost_cents=VALUES(unit_cost_cents),version=version+1`,
		p.Staff.StoreID, skuID, strings.TrimSpace(req.BatchNo), expiry.Format("2006-01-02"), req.Quantity, req.UnitCostCents)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	lotID, _ := result.LastInsertId()
	if lotID == 0 {
		_ = tx.QueryRowContext(r.Context(), `SELECT id FROM inventory_lots WHERE store_id=? AND sku_id=? AND batch_no=?`,
			p.Staff.StoreID, skuID, strings.TrimSpace(req.BatchNo)).Scan(&lotID)
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,unit_cost_cents,ref_type,ref_id,actor_id)
		VALUES (?,?,?,'RECEIPT',?,?,'MANUAL_RECEIPT',?,?)`, p.Staff.StoreID, skuID, lotID, req.Quantity, req.UnitCostCents, toStringID(lotID), p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	var qtyAfter int64
	if err := tx.QueryRowContext(r.Context(), `SELECT qty_on_hand FROM inventory_lots WHERE id=?`, lotID).Scan(&qtyAfter); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	before := map[string]any{"qtyOnHand": qtyAfter - req.Quantity}
	after := map[string]any{"qtyOnHand": qtyAfter, "batchNo": strings.TrimSpace(req.BatchNo), "unitCostCents": req.UnitCostCents}
	if err := auditTx(r.Context(), tx, p.UserID, p.Staff.StoreID, "STOCK_RECEIPT", "INVENTORY_LOT", toStringID(lotID), before, after, traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "入库失败"})
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), map[string]any{"ok": true, "lotId": toStringID(lotID)})
}

func (a *App) adminWaste(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req wasteInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	lotID, err := parseID(req.LotID, "lotId")
	if err != nil || req.Quantity <= 0 || strings.TrimSpace(req.Reason) == "" {
		writeError(w, r, badRequest("INVALID_WASTE", "批次、数量和报损原因必须有效"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "报损失败"})
		return
	}
	defer tx.Rollback()
	var skuID, onHand, reserved int64
	var storeID int64
	err = tx.QueryRowContext(r.Context(), `SELECT store_id,sku_id,qty_on_hand,qty_reserved FROM inventory_lots WHERE id=? FOR UPDATE`, lotID).
		Scan(&storeID, &skuID, &onHand, &reserved)
	if err != nil || storeID != p.Staff.StoreID {
		writeError(w, r, notFound("批次不存在"))
		return
	}
	if onHand-reserved < req.Quantity {
		writeError(w, r, conflict("INSUFFICIENT_AVAILABLE", "报损不能扣减已被订单预留的库存"))
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE inventory_lots SET qty_on_hand=qty_on_hand-?,version=version+1 WHERE id=?`, req.Quantity, lotID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "报损失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,ref_type,ref_id,actor_id)
		VALUES (?,?,?,'WASTE',?,'MANUAL_WASTE',?,?)`, storeID, skuID, lotID, -req.Quantity, toStringID(lotID), p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "报损失败"})
		return
	}
	before := map[string]any{"qtyOnHand": onHand, "qtyReserved": reserved}
	after := map[string]any{"qtyOnHand": onHand - req.Quantity, "qtyReserved": reserved}
	if err := auditTx(r.Context(), tx, p.UserID, storeID, "STOCK_WASTE", "INVENTORY_LOT", toStringID(lotID), before, after, traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "报损失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "报损失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) adminStaff(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT m.id,u.id,u.nickname,COALESCE(u.phone,''),m.active,m.version,
		COALESCE(GROUP_CONCAT(sr.role ORDER BY sr.role),'')
		FROM staff_memberships m JOIN users u ON u.id=m.user_id
		LEFT JOIN staff_roles sr ON sr.membership_id=m.id
		WHERE m.store_id=? GROUP BY m.id,u.id,u.nickname,u.phone,m.active,m.version ORDER BY m.id`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取员工失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var membershipID, userID, version int64
		var nickname, phone, roles string
		var active bool
		if err := rows.Scan(&membershipID, &userID, &nickname, &phone, &active, &version, &roles); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": toStringID(membershipID), "userId": toStringID(userID), "nickname": nickname, "phone": phone,
			"active": active, "version": version, "roles": splitRoles(roles),
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func splitRoles(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func (a *App) adminUpdateStaffRoles(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	membershipID, err := parseID(r.PathValue("id"), "员工ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req staffRolesInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if len(req.Roles) == 0 {
		writeError(w, r, badRequest("ROLES_REQUIRED", "至少保留一个角色"))
		return
	}
	allowed := map[string]bool{
		"store_admin": true, "catalog_operator": true, "warehouse_operator": true,
		"dispatcher": true, "courier": true, "support": true, "finance": true,
	}
	for _, role := range req.Roles {
		if !allowed[role] {
			writeError(w, r, badRequest("INVALID_ROLE", "包含未知角色"))
			return
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
		return
	}
	defer tx.Rollback()
	var storeID, version int64
	var userID int64
	var active bool
	err = tx.QueryRowContext(r.Context(), `SELECT store_id,user_id,version,active FROM staff_memberships WHERE id=? FOR UPDATE`, membershipID).
		Scan(&storeID, &userID, &version, &active)
	if err != nil || storeID != p.Staff.StoreID {
		writeError(w, r, notFound("员工不存在"))
		return
	}
	if version != req.ExpectedVersion {
		writeError(w, r, conflict("VERSION_CONFLICT", "员工权限已被更新，请刷新"))
		return
	}
	if !active {
		writeError(w, r, conflict("INACTIVE_STAFF", "停用员工不能变更角色"))
		return
	}
	previousRoles := []string{}
	if rows, err := tx.QueryContext(r.Context(), `SELECT role FROM staff_roles WHERE membership_id=? ORDER BY role`, membershipID); err == nil {
		for rows.Next() {
			var role string
			if rows.Scan(&role) == nil {
				previousRoles = append(previousRoles, role)
			}
		}
		rows.Close()
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM staff_roles WHERE membership_id=?`, membershipID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
		return
	}
	for _, role := range req.Roles {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO staff_roles(membership_id,role) VALUES (?,?)`, membershipID, role); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE staff_memberships SET version=version+1 WHERE id=?`, membershipID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
		return
	}
	if err := auditTx(r.Context(), tx, p.UserID, p.Staff.StoreID, "UPDATE_STAFF_ROLES", "STAFF", toStringID(membershipID),
		map[string]any{"roles": previousRoles}, map[string]any{"roles": req.Roles}, traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新员工失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "version": version + 1, "userId": toStringID(userID)})
}

func (a *App) adminAudit(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := r.URL.Query()
	from, to, rangeErr := a.shanghaiRange(q.Get("from"), q.Get("to"))
	if rangeErr != nil {
		writeError(w, r, rangeErr)
		return
	}
	page := 1
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, r, badRequest("INVALID_PAGE", "page 必须是正整数"))
			return
		}
		page = n
	}
	size := 20
	if v := strings.TrimSpace(q.Get("pageSize")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, r, badRequest("INVALID_PAGE_SIZE", "pageSize 必须在 1-100 之间"))
			return
		}
		size = n
	}
	where := " WHERE store_id=?"
	args := []any{p.Staff.StoreID}
	if v := strings.TrimSpace(q.Get("action")); v != "" {
		where += " AND action=?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("objectType")); v != "" {
		where += " AND object_type=?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("actorId")); v != "" {
		where += " AND actor_id=?"
		args = append(args, v)
	}
	if !from.IsZero() {
		where += " AND created_at>=?"
		args = append(args, from)
	}
	if !to.IsZero() {
		where += " AND created_at<?"
		args = append(args, to)
	}
	var total int64
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM audit_logs`+where, args...).Scan(&total); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取审计失败"})
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,actor_id,action,object_type,object_id,COALESCE(before_json,'null'),
		COALESCE(after_json,'null'),trace_id,created_at FROM audit_logs`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, size, (page-1)*size)...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取审计失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, actorID int64
		var action, objectType, objectID, traceID string
		var before, after []byte
		var createdAt time.Time
		if err := rows.Scan(&id, &actorID, &action, &objectType, &objectID, &before, &after, &traceID, &createdAt); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": toStringID(id), "actorId": toStringID(actorID), "action": action, "objectType": objectType,
			"objectId": objectID, "before": scanJSON(before), "after": scanJSON(after), "traceId": traceID, "createdAt": createdAt,
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"items": items, "total": total, "page": page, "pageSize": size})
}
