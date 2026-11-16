package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *App) listOrders(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := pageLimit(r.URL.Query().Get("limit"))
	cursorTime, cursorID, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
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
		WHERE o.buyer_id=?`
	args := []any{p.UserID}
	if status != "" {
		query += " AND o.status=?"
		args = append(args, status)
	}
	query += " AND (? IS NULL OR o.created_at < ? OR (o.created_at = ? AND o.id < ?))"
	query += " ORDER BY o.created_at DESC, o.id DESC LIMIT ?"
	var cursorArg any
	if !cursorTime.IsZero() {
		cursorArg = cursorTime
	}
	args = append(args, cursorArg, cursorArg, cursorArg, cursorID, limit)
	items, apiErr := a.queryOrderSummaries(r.Context(), query, args, false)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"items": items, "nextCursor": nextCursor(items, limit)})
}

func (a *App) adminOrders(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := r.URL.Query()
	status := strings.TrimSpace(q.Get("status"))
	limit := pageLimit(q.Get("limit"))
	cursorTime, cursorID, err := decodeCursor(q.Get("cursor"))
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	from, to, rangeErr := a.shanghaiRange(q.Get("from"), q.Get("to"))
	if rangeErr != nil {
		writeError(w, r, rangeErr)
		return
	}
	buyer := strings.TrimSpace(q.Get("buyer"))
	keyword := strings.TrimSpace(q.Get("keyword"))
	if len([]rune(buyer)) > 40 || len([]rune(keyword)) > 40 {
		writeError(w, r, badRequest("FILTER_TOO_LONG", "姓名或商品关键词不能超过 40 个字符"))
		return
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
		WHERE o.store_id=?`
	args := []any{p.Staff.StoreID}
	if status != "" {
		query += " AND o.status=?"
		args = append(args, status)
	}
	if !from.IsZero() {
		query += " AND o.created_at>=?"
		args = append(args, from)
	}
	if !to.IsZero() {
		query += " AND o.created_at<?"
		args = append(args, to)
	}
	if buyer != "" {
		query += ` AND u.nickname LIKE ? ESCAPE '\\'`
		args = append(args, "%"+likeEscape(buyer)+"%")
	}
	if keyword != "" {
		query += ` AND EXISTS (SELECT 1 FROM order_items oi2 WHERE oi2.order_id=o.id AND oi2.product_name LIKE ? ESCAPE '\\')`
		args = append(args, "%"+likeEscape(keyword)+"%")
	}
	query += " AND (? IS NULL OR o.created_at < ? OR (o.created_at = ? AND o.id < ?))"
	query += " ORDER BY o.created_at DESC, o.id DESC LIMIT ?"
	var cursorArg any
	if !cursorTime.IsZero() {
		cursorArg = cursorTime
	}
	args = append(args, cursorArg, cursorArg, cursorArg, cursorID, limit)
	items, apiErr := a.queryOrderSummaries(r.Context(), query, args, true)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"items": items, "nextCursor": nextCursor(items, limit)})
}

// shanghaiRange converts YYYY-MM-DD bounds (Asia/Shanghai business days) into a
// half-open UTC window: from 00:00 of `from` up to but excluding 00:00 of `to`+1.
func (a *App) shanghaiRange(from, to string) (time.Time, time.Time, *apiError) {
	layout := "2006-01-02"
	var start, end time.Time
	if v := strings.TrimSpace(from); v != "" {
		t, err := time.ParseInLocation(layout, v, a.tz)
		if err != nil {
			return start, end, badRequest("INVALID_DATE", "from 必须是 YYYY-MM-DD")
		}
		start = t
	}
	if v := strings.TrimSpace(to); v != "" {
		t, err := time.ParseInLocation(layout, v, a.tz)
		if err != nil {
			return start, end, badRequest("INVALID_DATE", "to 必须是 YYYY-MM-DD")
		}
		end = t.AddDate(0, 0, 1)
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return start, end, badRequest("INVALID_DATE_RANGE", "from 不能晚于 to")
	}
	return start.UTC(), end.UTC(), nil
}

func likeEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

func nextCursor(items []OrderSummary, limit int) string {
	if len(items) < limit || len(items) == 0 {
		return ""
	}
	last := items[len(items)-1]
	id, _ := parseID(last.ID, "id")
	return encodeCursor(last.CreatedAt, id)
}

func (a *App) queryOrderSummaries(ctx context.Context, query string, args []any, admin bool) ([]OrderSummary, *apiError) {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取订单失败"}
	}
	defer rows.Close()
	items := []OrderSummary{}
	for rows.Next() {
		item, err := scanOrderSummary(rows, admin)
		if err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取订单失败"}
		}
		items = append(items, item)
	}
	return items, nil
}

func scanOrderSummary(scanner interface{ Scan(...any) error }, admin bool) (OrderSummary, error) {
	var item OrderSummary
	var id, buyerID, goods, delivery, total, ordinal, counter int64
	var isFirst, isDemo int
	var urgeCount, pendingUrges int64
	var lastUrge sql.NullTime
	var address []byte
	var paidAt sql.NullTime
	var createdAt time.Time
	err := scanner.Scan(&id, &item.OrderNo, &buyerID, &item.BuyerName, &item.Status, &item.PaymentStatus,
		&item.DeliveryStatus, &goods, &delivery, &total, &item.Currency, &createdAt, &paidAt, &address,
		&ordinal, &isFirst, &isDemo, &counter, &item.ItemCount, &urgeCount, &pendingUrges, &lastUrge, &item.Version)
	if err != nil {
		return item, err
	}
	item.ID = toStringID(id)
	item.BuyerID = toStringID(buyerID)
	item.GoodsCents = goods
	item.DeliveryCents = delivery
	item.TotalCents = total
	item.CreatedAt = createdAt.UTC()
	if paidAt.Valid {
		t := paidAt.Time.UTC()
		item.PaidAt = &t
	}
	item.CustomerOrderOrdinal = int(ordinal)
	item.IsFirstOrder = isFirst == 1
	item.IsDemo = isDemo == 1
	item.CustomerOrderCount = counter
	item.AddressText = addressText(address)
	item.UrgeCount = urgeCount
	item.PendingUrgeCount = pendingUrges
	if lastUrge.Valid {
		t := lastUrge.Time.UTC()
		item.LastUrgeAt = &t
	}
	item.AllowedActions = allowedOrderActions(item.Status, item.PaymentStatus, admin)
	return item, nil
}

func addressText(raw []byte) string {
	var address map[string]any
	if err := json.Unmarshal(raw, &address); err != nil {
		return ""
	}
	building := stringValue(address["buildingName"])
	detail := stringValue(address["detail"])
	if building == "" && detail == "" {
		return ""
	}
	return strings.TrimSpace(building + " " + detail)
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func (a *App) getOrder(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	detail, apiErr := a.loadOrderDetail(r.Context(), id, p.UserID, 0)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), detail)
}

func (a *App) adminOrderDetail(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	detail, apiErr := a.loadOrderDetail(r.Context(), id, 0, p.Staff.StoreID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), detail)
}

func (a *App) loadOrderDetail(ctx context.Context, orderID, buyerID, storeID int64) (*OrderDetail, *apiError) {
	query := `SELECT o.id,o.order_no,o.buyer_id,u.nickname,o.status,o.payment_status,o.delivery_status,
		o.goods_cents,o.delivery_cents,o.total_cents,o.currency,o.created_at,o.paid_at,o.address_json,
		o.customer_order_ordinal,o.is_first_order,o.is_demo,COALESCE(c.total_created,0),
		(SELECT COALESCE(SUM(quantity),0) FROM order_items oi WHERE oi.order_id=o.id),
		o.remark,o.expires_at,o.version,o.payment_exception
		FROM orders o JOIN users u ON u.id=o.buyer_id
		LEFT JOIN customer_order_counters c ON c.user_id=o.buyer_id
		WHERE o.id=?`
	args := []any{orderID}
	if buyerID > 0 {
		query += " AND o.buyer_id=?"
		args = append(args, buyerID)
	}
	if storeID > 0 {
		query += " AND o.store_id=?"
		args = append(args, storeID)
	}
	var detail OrderDetail
	var id, buyer, goods, delivery, total, ordinal, counter, version int64
	var isFirst, isDemo, exception int
	var addressJSON []byte
	var paidAt, expiresAt sql.NullTime
	var createdAt time.Time
	err := a.db.QueryRowContext(ctx, query, args...).Scan(&id, &detail.OrderNo, &buyer, &detail.BuyerName,
		&detail.Status, &detail.PaymentStatus, &detail.DeliveryStatus, &goods, &delivery, &total,
		&detail.Currency, &createdAt, &paidAt, &addressJSON, &ordinal, &isFirst, &isDemo, &counter,
		&detail.ItemCount, &detail.Remark, &expiresAt, &version, &exception)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("订单不存在")
	}
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取订单失败"}
	}
	detail.ID = toStringID(id)
	detail.BuyerID = toStringID(buyer)
	detail.GoodsCents = goods
	detail.DeliveryCents = delivery
	detail.TotalCents = total
	detail.CreatedAt = createdAt.UTC()
	if paidAt.Valid {
		t := paidAt.Time.UTC()
		detail.PaidAt = &t
	}
	if expiresAt.Valid {
		detail.ExpiresAt = expiresAt.Time.UTC()
	}
	detail.CustomerOrderOrdinal = int(ordinal)
	detail.CustomerOrderCount = counter
	detail.IsFirstOrder = isFirst == 1
	detail.IsDemo = isDemo == 1
	detail.Version = version
	detail.PaymentException = exception == 1
	detail.Address = scanJSON(addressJSON)
	detail.AddressText = addressText(addressJSON)
	isAdmin := storeID > 0
	detail.AllowedActions = allowedOrderActions(detail.Status, detail.PaymentStatus, isAdmin)

	items, err := a.loadOrderItems(ctx, id)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取订单明细失败"}
	}
	detail.Items = items
	events, _ := a.loadOrderEvents(ctx, id)
	detail.Events = events
	payment, _ := a.loadPaymentAttempt(ctx, id)
	detail.Payment = payment
	task, _ := a.loadDeliveryTaskByOrder(ctx, id)
	detail.DeliveryTask = task
	refunds, _ := a.loadRefundsByOrder(ctx, id)
	detail.Refunds = refunds
	detail.Urge, _ = a.loadUrgeSummary(ctx, id, buyerID, isAdmin, detail.Status, detail.PaymentStatus)
	return &detail, nil
}

func (a *App) loadOrderItems(ctx context.Context, orderID int64) ([]OrderItem, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,sku_id,product_name,spec_name,unit_price_cents,quantity,goods_cents,refunded_quantity
		FROM order_items WHERE order_id=? ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []OrderItem{}
	for rows.Next() {
		var item OrderItem
		var id, skuID int64
		if err := rows.Scan(&id, &skuID, &item.ProductName, &item.SpecName, &item.UnitPriceCents, &item.Quantity, &item.GoodsCents, &item.RefundedQuantity); err != nil {
			return nil, err
		}
		item.ID = toStringID(id)
		item.SkuID = toStringID(skuID)
		items = append(items, item)
	}
	return items, nil
}

func (a *App) loadOrderEvents(ctx context.Context, orderID int64) ([]OrderEvent, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,event_type,actor_type,COALESCE(actor_id,0),COALESCE(payload_json,'null'),created_at
		FROM order_events WHERE order_id=? ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []OrderEvent{}
	for rows.Next() {
		var item OrderEvent
		var id, actorID int64
		var payload []byte
		if err := rows.Scan(&id, &item.Type, &item.ActorType, &actorID, &payload, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ID = toStringID(id)
		if actorID > 0 {
			item.ActorID = toStringID(actorID)
		}
		item.Payload = scanJSON(payload)
		items = append(items, item)
	}
	return items, nil
}

func (a *App) loadPaymentAttempt(ctx context.Context, orderID int64) (*PaymentAttempt, error) {
	var item PaymentAttempt
	var id int64
	var params []byte
	var expires sql.NullTime
	err := a.db.QueryRowContext(ctx, `SELECT id,channel,out_trade_no,amount_cents,status,COALESCE(prepay_params,'null'),prepay_expires_at
		FROM payment_attempts WHERE order_id=?`, orderID).Scan(&id, &item.Channel, &item.OutTradeNo, &item.AmountCents, &item.Status, &params, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.ID = toStringID(id)
	item.PrepayParams = scanJSON(params)
	if expires.Valid {
		t := expires.Time.UTC()
		item.PrepayExpiresAt = &t
	}
	item.Simulated = item.Channel == "SIMULATED"
	return &item, nil
}

func (a *App) cancelOrder(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "取消订单失败"})
		return
	}
	defer tx.Rollback()
	var status, paymentStatus string
	var buyerID int64
	err = tx.QueryRowContext(r.Context(), `SELECT buyer_id,status,payment_status FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&buyerID, &status, &paymentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("订单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取订单失败"})
		return
	}
	if buyerID != p.UserID {
		writeError(w, r, forbidden("只能取消本人订单"))
		return
	}
	if status != "PENDING_PAYMENT" {
		writeError(w, r, conflict("INVALID_STATE", "当前订单状态不能取消"))
		return
	}
	var attemptID int64
	var channel, attemptStatus string
	attemptErr := tx.QueryRowContext(r.Context(), `SELECT id,channel,status FROM payment_attempts WHERE order_id=? FOR UPDATE`, orderID).
		Scan(&attemptID, &channel, &attemptStatus)
	if attemptErr != nil && !errors.Is(attemptErr, sql.ErrNoRows) {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取支付意图失败，库存已保留"})
		return
	}
	if attemptErr == nil && attemptStatus == "UNKNOWN" {
		writeError(w, r, conflict("PAYMENT_UNKNOWN", "支付结果未知，已保留库存，请稍后查单"))
		return
	}
	if attemptErr == nil && attemptStatus == "PAID" {
		writeError(w, r, conflict("ALREADY_PAID", "订单已支付，请走售后"))
		return
	}
	if attemptErr == nil && channel != "SIMULATED" && attemptStatus != "CLOSED" {
		writeError(w, r, conflict("PAYMENT_NOT_CLOSED", "支付渠道尚未可靠关闭，不能直接释放库存"))
		return
	}
	if attemptErr == nil {
		if _, err := tx.ExecContext(r.Context(), `UPDATE payment_attempts SET status='CLOSED',version=version+1 WHERE id=?`, attemptID); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "关闭支付意图失败"})
			return
		}
	}
	if err := a.releaseReservations(r.Context(), tx, orderID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "释放库存失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE orders SET status='CANCELLED',payment_status='CLOSED',version=version+1 WHERE id=?`, orderID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "取消订单失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id) VALUES (?,'ORDER_CANCELLED','CUSTOMER',?)`, orderID, p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入取消事件失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO outbox(topic,payload_json) VALUES ('ORDER_CANCELLED',?)`, mustJSON(map[string]any{"orderId": toStringID(orderID)})); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入取消通知失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "取消订单失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "status": "CANCELLED"})
}

func (a *App) releaseReservations(ctx context.Context, tx *sql.Tx, orderID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,lot_id,quantity FROM inventory_reservations WHERE order_id=? AND status='RESERVED' FOR UPDATE`, orderID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type reservation struct {
		id, lotID, quantity int64
	}
	list := []reservation{}
	for rows.Next() {
		var item reservation
		if err := rows.Scan(&item.id, &item.lotID, &item.quantity); err != nil {
			return err
		}
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, item := range list {
		result, err := tx.ExecContext(ctx, `UPDATE inventory_lots SET qty_reserved=qty_reserved-?,version=version+1
			WHERE id=? AND qty_reserved>=?`, item.quantity, item.lotID, item.quantity)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.New("inventory reservation cannot be released")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE inventory_reservations SET status='RELEASED' WHERE id=?`, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,ref_type,ref_id)
			SELECT store_id,sku_id,?,'RELEASE',?,'ORDER',? FROM inventory_lots WHERE id=?`,
			item.lotID, item.quantity, toStringID(orderID), item.lotID); err != nil {
			return err
		}
	}
	return nil
}
