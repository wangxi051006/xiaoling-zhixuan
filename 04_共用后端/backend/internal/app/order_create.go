package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func newOrderNo(now time.Time) string {
	buf := make([]byte, 5)
	_, _ = rand.Read(buf)
	return "ORD" + now.In(loadShanghai()).Format("20060102") + strings.ToUpper(hex.EncodeToString(buf))
}

func (a *App) createOrder(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req CreateOrderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	if header := strings.TrimSpace(r.Header.Get("Idempotency-Key")); header != "" && header != req.RequestID {
		writeError(w, r, badRequest("IDEMPOTENCY_KEY_MISMATCH", "Idempotency-Key 必须与 requestId 一致"))
		return
	}
	storeID := a.cfg.StoreID
	if strings.TrimSpace(req.StoreID) != "" {
		parsed, err := parseID(req.StoreID, "storeId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		storeID = parsed
	}
	if storeID != a.cfg.StoreID {
		writeError(w, r, forbidden("当前门店不可用"))
		return
	}
	items, err := normalizedItemIDs(req.Items)
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	fulfillment := strings.ToUpper(strings.TrimSpace(req.FulfillmentType))
	if !isOneOf(fulfillment, "DELIVERY", "PICKUP") {
		writeError(w, r, badRequest("INVALID_FULFILLMENT", "履约方式必须是 DELIVERY 或 PICKUP"))
		return
	}
	if strings.TrimSpace(req.AddressID) == "" && fulfillment == "DELIVERY" {
		writeError(w, r, badRequest("ADDRESS_REQUIRED", "配送订单必须选择本人地址"))
		return
	}
	var addressID int64
	if strings.TrimSpace(req.AddressID) != "" {
		addressID, err = parseID(req.AddressID, "addressId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
	}
	remark := trimRemark(req.Remark)
	fingerprint, err := hashCanonical(map[string]any{
		"storeId": storeID, "items": items, "addressId": addressID,
		"fulfillmentType": fulfillment, "quoteId": strings.TrimSpace(req.QuoteID),
		"expectedTotalCents": req.ExpectedTotalCents, "remark": remark,
	})
	if err != nil {
		writeError(w, r, &apiError{500, "INTERNAL_ERROR", "无法计算请求指纹"})
		return
	}
	// Fast replay path: a committed key is authoritative even if prices,
	// address or stock changed since the original attempt.
	if existing, apiErr := a.loadIdempotentOrder(r.Context(), p.UserID, req.RequestID, fingerprint); apiErr != nil || existing != nil {
		if apiErr != nil {
			writeError(w, r, apiErr)
			return
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), existing)
		return
	}

	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(), `INSERT INTO idempotency_keys
		(principal_id,command_scope,request_id,request_hash,resource_type,status_code)
		VALUES (?,?,?,?,?,201)`, p.UserID, "CREATE_ORDER", req.RequestID, fingerprint, "ORDER")
	if isDuplicateKey(err) {
		_ = tx.Rollback()
		existing, apiErr := a.loadIdempotentOrder(r.Context(), p.UserID, req.RequestID, fingerprint)
		if apiErr != nil {
			writeError(w, r, apiErr)
			return
		}
		if existing == nil {
			writeError(w, r, conflict("REQUEST_IN_PROGRESS", "相同请求正在处理，请稍后用原 requestId 查询"))
			return
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), existing)
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}

	quoteID := strings.TrimSpace(req.QuoteID)
	if quoteID == "" {
		writeError(w, r, badRequest("QUOTE_REQUIRED", "请先获取服务端报价"))
		return
	}
	var quoteUser, quoteStore int64
	var quoteHash string
	var quoteExpires time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT user_id,store_id,request_hash,expires_at FROM quotes WHERE id=?`, quoteID).
		Scan(&quoteUser, &quoteStore, &quoteHash, &quoteExpires)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, badRequest("QUOTE_NOT_FOUND", "报价不存在或已失效"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取报价失败"})
		return
	}
	if quoteUser != p.UserID || quoteStore != storeID {
		writeError(w, r, forbidden("报价不属于当前用户或门店"))
		return
	}
	if a.now().UTC().After(quoteExpires) {
		writeError(w, r, conflict("QUOTE_EXPIRED", "报价已过期，请重新确认商品与地址"))
		return
	}
	expectedQuoteHash, _ := hashCanonical(map[string]any{
		"storeId": storeID, "addressId": addressID, "fulfillmentType": fulfillment, "items": items,
	})
	if expectedQuoteHash != quoteHash {
		writeError(w, r, conflict("QUOTE_MISMATCH", "报价与本次商品/地址不一致，请重新报价"))
		return
	}
	current, qerr := a.buildQuote(r.Context(), tx, storeID, p.UserID, items, addressID, fulfillment)
	if qerr != nil {
		writeError(w, r, qerr)
		return
	}
	if current.TotalCents != req.ExpectedTotalCents {
		writeError(w, r, conflict("QUOTE_CHANGED", fmt.Sprintf("价格已变化，当前应付 %d 分，请重新确认", current.TotalCents)))
		return
	}
	for _, line := range current.Lines {
		if !line.InStock || !line.Active {
			writeError(w, r, conflict("ITEM_UNAVAILABLE", fmt.Sprintf("%s %s 当前不可售或库存不足", line.ProductName, line.SpecName)))
			return
		}
	}

	var address map[string]any
	if fulfillment == "DELIVERY" {
		address, _, err = addressSnapshotWith(r.Context(), tx, p.UserID, addressID)
		if err != nil {
			writeError(w, r, badRequest("ADDRESS_NOT_FOUND", "地址不存在或不属于本人"))
			return
		}
		point, pointErr := findDeliveryPoint(r.Context(), tx, storeID, stringValue(address["buildingId"]))
		if pointErr != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取配送点配置失败"})
			return
		}
		if point != nil {
			address["deliveryPoint"] = point
		}
	} else {
		address = map[string]any{
			"id": "", "buildingId": "", "buildingName": "店内自取",
			"contactName": "到店自取", "phone": "", "detail": "校零智选门店取货",
		}
	}
	addressJSON, _ := json.Marshal(address)

	// F1 lock order: idempotency command -> customer counter -> order -> lots.
	// The counter row is the serialization point for concurrent first orders.
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO customer_order_counters(user_id,total_created) VALUES (?,0)
		ON DUPLICATE KEY UPDATE user_id=user_id`, p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	var totalCreated int64
	if err := tx.QueryRowContext(r.Context(), `SELECT total_created FROM customer_order_counters WHERE user_id=? FOR UPDATE`, p.UserID).Scan(&totalCreated); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	ordinal := int(totalCreated + 1)
	if _, err := tx.ExecContext(r.Context(), `UPDATE customer_order_counters SET total_created=? WHERE user_id=?`, ordinal, p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	isFirst := ordinal == 1

	expiresAt := a.now().UTC().Add(10 * time.Minute)
	var orderID int64
	var orderNo string
	for attempt := 0; attempt < 3; attempt++ {
		orderNo = newOrderNo(a.now())
		result, insertErr := tx.ExecContext(r.Context(), `INSERT INTO orders
			(order_no,store_id,buyer_id,request_id,request_hash,status,payment_status,delivery_status,
			 goods_cents,delivery_cents,discount_cents,total_cents,currency,address_json,remark,expires_at,
			 customer_order_ordinal,is_first_order,is_demo)
			VALUES (?,?,?,?,?,'PENDING_PAYMENT','UNPAID','UNASSIGNED',?,?,?,?,?,?,?,?,?,?,?)`,
			orderNo, storeID, p.UserID, req.RequestID, fingerprint,
			current.GoodsCents, current.DeliveryCents, current.DiscountCents, current.TotalCents, "CNY",
			addressJSON, remark, expiresAt, ordinal, boolInt(isFirst), boolInt(p.IsDemo))
		if insertErr == nil {
			orderID, _ = result.LastInsertId()
			break
		}
		if !isDuplicateKey(insertErr) {
			writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
			return
		}
	}
	if orderID == 0 {
		writeError(w, r, &apiError{500, "DB_ERROR", "生成订单号失败"})
		return
	}

	today := a.now().In(a.tz).Format("2006-01-02")
	itemIDs := make([]int64, len(current.Lines))
	for index, line := range current.Lines {
		skuID, _ := strconv.ParseInt(line.SkuID, 10, 64)
		result, err := tx.ExecContext(r.Context(), `INSERT INTO order_items
			(order_id,sku_id,product_name,spec_name,unit_price_cents,quantity,goods_cents)
			VALUES (?,?,?,?,?,?,?)`, orderID, skuID, line.ProductName, line.SpecName, line.UnitPriceCents, line.Quantity, line.GoodsCents)
		if err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "创建订单明细失败"})
			return
		}
		itemIDs[index], _ = result.LastInsertId()
		if err := a.reserveLine(r.Context(), tx, storeID, skuID, itemIDs[index], line.Quantity, today); err != nil {
			writeError(w, r, err)
			return
		}
	}

	if _, err := tx.ExecContext(r.Context(), `UPDATE idempotency_keys SET resource_id=?, status_code=201
		WHERE principal_id=? AND command_scope='CREATE_ORDER' AND request_id=?`,
		toStringID(orderID), p.UserID, req.RequestID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	_, _ = tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json)
		VALUES (?,?,?,?,?)`, orderID, "ORDER_CREATED", "CUSTOMER", p.UserID, addressJSON)
	_, _ = tx.ExecContext(r.Context(), `INSERT INTO outbox(topic,payload_json) VALUES ('ORDER_CREATED',?)`,
		mustJSON(map[string]any{"orderId": toStringID(orderID), "orderNo": orderNo}))

	snapshot := make([]ItemSnapshot, 0, len(current.Lines))
	for _, line := range current.Lines {
		snapshot = append(snapshot, ItemSnapshot{
			SkuID: line.SkuID, ProductName: line.ProductName, SpecName: line.SpecName,
			UnitPriceCents: line.UnitPriceCents, Quantity: line.Quantity, GoodsCents: line.GoodsCents,
		})
	}
	response := CreateOrderResponse{
		OrderID: toStringID(orderID), OrderNo: orderNo, TotalCents: current.TotalCents, Currency: "CNY",
		ExpiresAt: expiresAt, ItemsSnapshot: snapshot, Replayed: false,
		CustomerOrderOrdinal: ordinal, IsFirstOrder: isFirst,
	}
	responseJSON, _ := json.Marshal(response)
	if _, err := tx.ExecContext(r.Context(), `UPDATE idempotency_keys SET response_json=?
		WHERE principal_id=? AND command_scope='CREATE_ORDER' AND request_id=?`, responseJSON, p.UserID, req.RequestID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建订单失败"})
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), response)
}

func (a *App) reserveLine(ctx context.Context, tx *sql.Tx, storeID, skuID, orderItemID int64, quantity int, today string) *apiError {
	rows, err := tx.QueryContext(ctx, `SELECT id,expiry_date,qty_on_hand,qty_reserved FROM inventory_lots
		WHERE store_id=? AND sku_id=? AND expiry_date>=? AND qty_on_hand>qty_reserved
		ORDER BY expiry_date,id FOR UPDATE`, storeID, skuID, today)
	if err != nil {
		return &apiError{500, "DB_ERROR", "锁定库存失败"}
	}
	defer rows.Close()
	type lot struct {
		id, onHand, reserved int64
		expiry               time.Time
	}
	lots := []lot{}
	for rows.Next() {
		var item lot
		if err := rows.Scan(&item.id, &item.expiry, &item.onHand, &item.reserved); err != nil {
			return &apiError{500, "DB_ERROR", "读取库存失败"}
		}
		lots = append(lots, item)
	}
	remaining := int64(quantity)
	for _, item := range lots {
		if remaining == 0 {
			break
		}
		available := item.onHand - item.reserved
		if available <= 0 {
			continue
		}
		take := available
		if take > remaining {
			take = remaining
		}
		result, err := tx.ExecContext(ctx, `UPDATE inventory_lots SET qty_reserved=qty_reserved+?,version=version+1
			WHERE id=? AND qty_on_hand-qty_reserved>=?`, take, item.id, take)
		if err != nil {
			return &apiError{500, "DB_ERROR", "预留库存失败"}
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return conflict("STOCK_INSUFFICIENT", "库存刚刚被其他订单占用，请重新选择")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO inventory_reservations(order_id,order_item_id,lot_id,quantity)
			SELECT order_id,id,?,? FROM order_items WHERE id=?`, item.id, take, orderItemID); err != nil {
			return &apiError{500, "DB_ERROR", "记录库存预留失败"}
		}
		_, _ = tx.ExecContext(ctx, `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,ref_type,ref_id)
			VALUES (?,?,?,'RESERVE',?,'ORDER_ITEM',?)`, storeID, skuID, item.id, -take, toStringID(orderItemID))
		remaining -= take
	}
	if remaining > 0 {
		return conflict("STOCK_INSUFFICIENT", "库存不足，整单已回滚")
	}
	return nil
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func (a *App) loadIdempotentOrder(ctx context.Context, userID int64, requestID, fingerprint string) (*CreateOrderResponse, *apiError) {
	var storedHash string
	var responseJSON []byte
	err := a.db.QueryRowContext(ctx, `SELECT request_hash,response_json FROM idempotency_keys
		WHERE principal_id=? AND command_scope='CREATE_ORDER' AND request_id=?`, userID, requestID).
		Scan(&storedHash, &responseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取幂等结果失败"}
	}
	if storedHash != fingerprint {
		return nil, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同参数")
	}
	if len(responseJSON) == 0 {
		return nil, conflict("REQUEST_IN_PROGRESS", "原请求尚未完成，请稍后用原 requestId 查询")
	}
	var response CreateOrderResponse
	if err := json.Unmarshal(responseJSON, &response); err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取原订单结果失败"}
	}
	response.Replayed = true
	return &response, nil
}
