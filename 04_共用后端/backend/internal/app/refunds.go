package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type refundCreateItem struct {
	OrderItemID string `json:"orderItemId"`
	Quantity    int    `json:"quantity"`
}

type refundCreateRequest struct {
	RequestID string             `json:"requestId"`
	OrderID   string             `json:"orderId"`
	Reason    string             `json:"reason"`
	Items     []refundCreateItem `json:"items"`
}

type refundConfirmRequest struct {
	Confirm string `json:"confirm"`
}

type restockRequest struct {
	RequestID    string `json:"requestId"`
	RefundItemID string `json:"refundItemId"`
	LotID        string `json:"lotId"`
	Quantity     int    `json:"quantity"`
}

func (a *App) adminRefundDetail(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	refundID, err := parseID(r.PathValue("id"), "退款ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var item Refund
	var scannedID, orderID, requestedBy int64
	var succeeded sql.NullTime
	err = a.db.QueryRowContext(r.Context(), `SELECT r.id,o.id,o.order_no,r.out_refund_no,r.amount_cents,r.status,r.reason,r.requested_by,r.succeeded_at,r.created_at
		FROM refunds r JOIN orders o ON o.id=r.order_id WHERE r.id=? AND o.store_id=?`, refundID, p.Staff.StoreID).
		Scan(&scannedID, &orderID, &item.OrderNo, &item.OutRefundNo, &item.AmountCents, &item.Status, &item.Reason, &requestedBy, &succeeded, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("退款不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取退款失败"})
		return
	}
	// The first scan target is a string field; re-read the numeric id to keep
	// the response stable for old drivers.
	item.ID = toStringID(scannedID)
	item.OrderID = toStringID(orderID)
	item.RequestedBy = toStringID(requestedBy)
	if succeeded.Valid {
		t := succeeded.Time.UTC()
		item.SucceededAt = &t
	}
	item.Items = a.loadRefundItems(r.Context(), refundID)
	writeJSON(w, http.StatusOK, traceIDFrom(r), item)
}

func (a *App) createAfterSale(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req refundCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "售后 requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	if len(req.Items) == 0 {
		writeError(w, r, badRequest("ITEMS_REQUIRED", "请选择售后商品行"))
		return
	}
	refund, apiErr := a.createRefund(r.Context(), orderID, req.RequestID, req.Reason, req.Items, "U", p.UserID, 0, false, traceIDFrom(r))
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), refund)
}

func (a *App) adminCreateRefund(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req refundCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "退款 requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	orderID, err := parseID(req.OrderID, "orderId")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if len(req.Items) == 0 {
		writeError(w, r, badRequest("ITEMS_REQUIRED", "请选择退款商品行"))
		return
	}
	refund, apiErr := a.createRefund(r.Context(), orderID, req.RequestID, req.Reason, req.Items, "R", p.UserID, p.Staff.StoreID, true, traceIDFrom(r))
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), refund)
}

type refundLine struct {
	orderItemID int64
	quantity    int
	unitPrice   int64
}

func normalizeRefundItems(rawItems []refundCreateItem) ([]refundCreateItem, *apiError) {
	normalized := []refundCreateItem{}
	seen := map[int64]int{}
	for _, requested := range rawItems {
		itemID, err := parseID(requested.OrderItemID, "orderItemId")
		if err != nil || requested.Quantity <= 0 {
			return nil, badRequest("INVALID_REFUND_ITEM", "退款商品行或数量无效")
		}
		if idx, ok := seen[itemID]; ok {
			if requested.Quantity > math.MaxInt-normalized[idx].Quantity {
				return nil, badRequest("INVALID_REFUND_ITEM", "退款数量超出范围")
			}
			normalized[idx].Quantity += requested.Quantity
			continue
		}
		seen[itemID] = len(normalized)
		normalized = append(normalized, refundCreateItem{OrderItemID: toStringID(itemID), Quantity: requested.Quantity})
	}
	sort.Slice(normalized, func(i, j int) bool {
		a, _ := parseID(normalized[i].OrderItemID, "orderItemId")
		b, _ := parseID(normalized[j].OrderItemID, "orderItemId")
		return a < b
	})
	return normalized, nil
}

func refundRequestHash(orderID int64, reason string, items []refundCreateItem) string {
	fingerprint, _ := hashCanonical(map[string]any{"orderId": toStringID(orderID), "reason": reason, "items": items})
	return fingerprint
}

func refundRecoveryBlocked(ctx context.Context, tx *sql.Tx, orderID int64) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM refund_recovery_blocks WHERE order_id=?`, orderID).Scan(&count)
	return count > 0, err
}

// Called only after the order is locked and current authorization is checked.
// Compare persisted details for old NULL/order-sensitive fingerprints, then bind
// the canonical hash. An unknown legacy payload is never accepted blindly.
func replayRefund(ctx context.Context, tx *sql.Tx, orderID, requesterID int64, outRefundNo, fingerprint, reason string, items []refundCreateItem) (Refund, bool, *apiError) {
	var result Refund
	var id, existingOrder, existingRequester int64
	var oldHash sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.order_id,r.requested_by,r.request_hash,r.amount_cents,r.status,r.reason,r.created_at,o.order_no
		FROM refunds r JOIN orders o ON o.id=r.order_id WHERE r.out_refund_no=? FOR UPDATE`, outRefundNo).
		Scan(&id, &existingOrder, &existingRequester, &oldHash, &result.AmountCents, &result.Status, &result.Reason, &result.CreatedAt, &result.OrderNo)
	if errors.Is(err, sql.ErrNoRows) {
		return Refund{}, false, nil
	}
	if err != nil {
		return Refund{}, false, &apiError{500, "DB_ERROR", "读取退款幂等记录失败"}
	}
	if existingOrder != orderID || existingRequester != requesterID {
		return Refund{}, true, conflict("IDEMPOTENCY_CONFLICT", "该 requestId 已绑定其他订单或用户")
	}
	if !oldHash.Valid || oldHash.String != fingerprint {
		rows, err := tx.QueryContext(ctx, `SELECT order_item_id,quantity FROM refund_items WHERE refund_id=? ORDER BY order_item_id FOR UPDATE`, id)
		if err != nil {
			return Refund{}, true, &apiError{500, "DB_ERROR", "读取旧退款明细失败"}
		}
		stored := []refundCreateItem{}
		for rows.Next() {
			var itemID int64
			var q int
			if err := rows.Scan(&itemID, &q); err != nil {
				rows.Close()
				return Refund{}, true, &apiError{500, "DB_ERROR", "读取旧退款明细失败"}
			}
			stored = append(stored, refundCreateItem{OrderItemID: toStringID(itemID), Quantity: q})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Refund{}, true, &apiError{500, "DB_ERROR", "读取旧退款明细失败"}
		}
		rows.Close()
		if result.Reason != reason || refundRequestHash(orderID, result.Reason, stored) != fingerprint {
			return Refund{}, true, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同退款参数")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE refunds SET request_hash=? WHERE id=?`, fingerprint, id); err != nil {
			return Refund{}, true, &apiError{500, "DB_ERROR", "绑定旧退款请求失败"}
		}
	}
	result.ID = toStringID(id)
	result.OrderID = toStringID(existingOrder)
	result.RequestedBy = toStringID(existingRequester)
	result.OutRefundNo = outRefundNo
	return result, true, nil
}

func (a *App) createRefund(ctx context.Context, orderID int64, requestID, reason string, rawItems []refundCreateItem, commandScope string, requesterID, staffStoreID int64, isAdmin bool, traceID string) (Refund, *apiError) {
	normalized, apiErr := normalizeRefundItems(rawItems)
	if apiErr != nil {
		return Refund{}, apiErr
	}
	reason = truncate(strings.TrimSpace(reason), 255)
	fingerprint := refundRequestHash(orderID, reason, normalized)
	outRefundNo := commandScope + sha256Hex([]byte(requestID))[:24]
	// Reject another identity/order without ever returning its record. The actual
	// replay still happens under the order lock, including concurrent first calls.
	var boundOrder, boundRequester int64
	err := a.db.QueryRowContext(ctx, `SELECT order_id,requested_by FROM refunds WHERE out_refund_no=?`, outRefundNo).Scan(&boundOrder, &boundRequester)
	if err == nil && (boundOrder != orderID || boundRequester != requesterID) {
		return Refund{}, conflict("IDEMPOTENCY_CONFLICT", "该 requestId 已绑定其他订单或用户")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Refund{}, &apiError{500, "DB_ERROR", "读取退款幂等记录失败"}
	}

	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Refund{}, &apiError{500, "DB_ERROR", "创建退款失败"}
	}
	defer tx.Rollback()

	var storeID, buyerID int64
	var paymentStatus string
	var goodsCents int64
	if err := tx.QueryRowContext(ctx, `SELECT store_id,buyer_id,payment_status,goods_cents FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&storeID, &buyerID, &paymentStatus, &goodsCents); err != nil {
		return Refund{}, notFound("订单不存在")
	}
	if isAdmin {
		if storeID != staffStoreID {
			return Refund{}, forbidden("无权操作其他门店订单")
		}
	} else if buyerID != requesterID {
		return Refund{}, forbidden("只能对本人订单申请售后")
	}
	if blocked, err := refundRecoveryBlocked(ctx, tx, orderID); err != nil {
		return Refund{}, &apiError{500, "DB_ERROR", "读取退款核对状态失败"}
	} else if blocked {
		return Refund{}, conflict("REFUND_REQUIRES_RECONCILIATION", "历史退款账目异常，订单已隔离，请联系管理员核对")
	}
	if existing, found, apiErr := replayRefund(ctx, tx, orderID, requesterID, outRefundNo, fingerprint, reason, normalized); found || apiErr != nil {
		if apiErr != nil {
			return Refund{}, apiErr
		}
		if err := tx.Commit(); err != nil {
			return Refund{}, &apiError{500, "DB_ERROR", "退款重放失败"}
		}
		return existing, nil
	}
	if !isOneOf(paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		return Refund{}, conflict("INVALID_PAYMENT_STATE", "只有已付款订单可以退款")
	}

	lines := []refundLine{}
	total := int64(0)
	for _, requested := range normalized {
		itemID, _ := parseID(requested.OrderItemID, "orderItemId")
		var quantity, refunded, pending int
		var unitPrice int64
		if err := tx.QueryRowContext(ctx, `SELECT quantity,refunded_quantity,pending_refund_quantity,unit_price_cents FROM order_items
			WHERE id=? AND order_id=? FOR UPDATE`, itemID, orderID).Scan(&quantity, &refunded, &pending, &unitPrice); err != nil {
			return Refund{}, badRequest("INVALID_REFUND_ITEM", "退款商品行不属于该订单")
		}
		if refunded < 0 || pending < 0 || refunded > quantity || pending > quantity-refunded || requested.Quantity > quantity-refunded-pending {
			return Refund{}, conflict("REFUND_QUANTITY_EXCEEDED", "退款数量超过可退数量（含处理中退款）")
		}
		if unitPrice < 0 || (unitPrice > 0 && int64(requested.Quantity) > math.MaxInt64/unitPrice) || total > math.MaxInt64-unitPrice*int64(requested.Quantity) {
			return Refund{}, badRequest("INVALID_REFUND_AMOUNT", "退款金额超出范围")
		}
		total += unitPrice * int64(requested.Quantity)
		lines = append(lines, refundLine{orderItemID: itemID, quantity: requested.Quantity, unitPrice: unitPrice})
	}
	if total <= 0 || total > goodsCents {
		return Refund{}, badRequest("INVALID_REFUND_AMOUNT", "退款金额超出订单商品金额")
	}
	var pendingAmount int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_cents),0) FROM refunds
		WHERE order_id=? AND status IN ('REQUESTED','PROCESSING','UNKNOWN','SUCCESS')`, orderID).Scan(&pendingAmount); err != nil {
		return Refund{}, &apiError{500, "DB_ERROR", "读取处理中退款失败"}
	}
	if pendingAmount > goodsCents-total {
		return Refund{}, conflict("REFUND_AMOUNT_EXCEEDED", "处理中退款与本次退款合计超过订单商品金额")
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO refunds(order_id,after_sale_id,out_refund_no,request_hash,amount_cents,currency,status,reason,requested_by)
		VALUES (?,?,?,?,?, 'CNY','REQUESTED',?,?)`, orderID, requestID, outRefundNo, fingerprint, total, reason, requesterID)
	if err != nil {
		if isDuplicateKey(err) {
			return Refund{}, conflict("IDEMPOTENCY_CONFLICT", "退款请求冲突，请用原请求重试")
		}
		return Refund{}, &apiError{500, "DB_ERROR", "创建退款失败"}
	}
	refundID, _ := result.LastInsertId()
	for _, line := range lines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO refund_items(refund_id,order_item_id,quantity,amount_cents) VALUES (?,?,?,?)`,
			refundID, line.orderItemID, line.quantity, line.unitPrice*int64(line.quantity)); err != nil {
			return Refund{}, &apiError{500, "DB_ERROR", "写入退款明细失败"}
		}
		result, err := tx.ExecContext(ctx, `UPDATE order_items SET pending_refund_quantity=pending_refund_quantity+?
			WHERE id=? AND pending_refund_quantity+?<=quantity-refunded_quantity`, line.quantity, line.orderItemID, line.quantity)
		if err != nil {
			return Refund{}, &apiError{500, "DB_ERROR", "预占退款数量失败"}
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return Refund{}, conflict("REFUND_QUANTITY_EXCEEDED", "退款数量超过可退数量")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,'REFUND_REQUESTED','STAFF',?,?)`,
		orderID, requesterID, mustJSON(map[string]any{"refundId": toStringID(refundID), "amountCents": total})); err != nil {
		return Refund{}, &apiError{500, "DB_ERROR", "写入退款事件失败"}
	}
	if isAdmin {
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id)
			VALUES (?,?,?,?,?,?,?)`, requesterID, staffStoreID, "CREATE_REFUND", "REFUND", toStringID(refundID),
			mustJSON(map[string]any{"orderId": toStringID(orderID), "amountCents": total}), traceID); err != nil {
			return Refund{}, &apiError{500, "DB_ERROR", "写入退款审计失败"}
		}
	}
	if err := tx.Commit(); err != nil {
		return Refund{}, &apiError{500, "DB_ERROR", "创建退款失败"}
	}
	return Refund{ID: toStringID(refundID), OrderID: toStringID(orderID), OutRefundNo: outRefundNo, AmountCents: total,
		Status: "REQUESTED", Reason: reason, RequestedBy: toStringID(requesterID), CreatedAt: a.now().UTC()}, nil
}

func (a *App) adminRefunds(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := `SELECT r.id,o.id,o.order_no,r.out_refund_no,r.amount_cents,r.status,r.reason,r.requested_by,r.succeeded_at,r.created_at
		FROM refunds r JOIN orders o ON o.id=r.order_id WHERE o.store_id=?`
	args := []any{p.Staff.StoreID}
	if status != "" {
		query += " AND r.status=?"
		args = append(args, status)
	}
	query += " ORDER BY r.created_at DESC,r.id DESC LIMIT 100"
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取退款失败"})
		return
	}
	defer rows.Close()
	items := []Refund{}
	for rows.Next() {
		var item Refund
		var id, orderID, requestedBy int64
		var succeeded sql.NullTime
		if err := rows.Scan(&id, &orderID, &item.OrderNo, &item.OutRefundNo, &item.AmountCents, &item.Status, &item.Reason, &requestedBy, &succeeded, &item.CreatedAt); err != nil {
			continue
		}
		item.ID = toStringID(id)
		item.OrderID = toStringID(orderID)
		item.RequestedBy = toStringID(requestedBy)
		if succeeded.Valid {
			t := succeeded.Time.UTC()
			item.SucceededAt = &t
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) simulatedRefund(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if a.cfg.PaymentMode != "simulated" {
		writeError(w, r, unavailable("PAYMENT_NOT_CONFIGURED", "模拟退款仅在测试环境开启"))
		return
	}
	refundID, err := parseID(r.PathValue("id"), "退款ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req refundConfirmRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if req.Confirm != "SIMULATED_REFUND" {
		writeError(w, r, badRequest("SIMULATED_CONFIRMATION_REQUIRED", "确认值必须为 SIMULATED_REFUND"))
		return
	}
	var storeID int64
	if err := a.db.QueryRowContext(r.Context(), `SELECT o.store_id FROM refunds r JOIN orders o ON o.id=r.order_id WHERE r.id=?`, refundID).Scan(&storeID); err != nil {
		writeError(w, r, notFound("退款不存在"))
		return
	}
	if storeID != p.Staff.StoreID {
		writeError(w, r, forbidden("无权操作其他门店退款"))
		return
	}
	if err := a.confirmRefund(r.Context(), refundID, "SIMULATED-"+toStringID(refundID), a.now().UTC(), "SIMULATED"); err != nil {
		writeError(w, r, conflict("REFUND_CONFIRM_FAILED", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "status": "SUCCESS", "simulated": true})
}

type refundSuccessEvent struct {
	EventID          string    `json:"eventId"`
	EventType        string    `json:"eventType"`
	RefundID         string    `json:"refundId,omitempty"`
	OutRefundNo      string    `json:"outRefundNo"`
	ProviderRefundID string    `json:"providerRefundId"`
	AmountCents      int64     `json:"amountCents"`
	SucceededAt      time.Time `json:"succeededAt"`
}

func parseWechatSuccessTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil || value.IsZero() {
		return time.Time{}, errors.New("missing or invalid WeChat success_time; reconciliation required")
	}
	return value.UTC(), nil
}

func parseRefundSuccessEvent(payload []byte) (refundSuccessEvent, error) {
	var event refundSuccessEvent
	// Decoding directly into int64 rejects fractional/exponent/overflow values
	// and preserves integers above 2^53 without a float64 round trip.
	if err := json.Unmarshal(payload, &event); err != nil {
		return event, fmt.Errorf("invalid refund event; reconciliation required: %w", err)
	}
	if event.OutRefundNo == "" || strings.TrimSpace(event.OutRefundNo) != event.OutRefundNo || len(event.OutRefundNo) > 64 {
		return event, errors.New("missing or invalid out_refund_no; reconciliation required")
	}
	if event.AmountCents <= 0 {
		return event, errors.New("refund amount must be a positive integer in cents; reconciliation required")
	}
	if event.ProviderRefundID == "" || strings.TrimSpace(event.ProviderRefundID) != event.ProviderRefundID || len(event.ProviderRefundID) > 128 {
		return event, errors.New("missing or invalid provider_refund_id; reconciliation required")
	}
	if event.SucceededAt.IsZero() {
		return event, errors.New("missing refund success time; reconciliation required")
	}
	if event.RefundID != "" {
		if _, err := parseID(event.RefundID, "refundId"); err != nil {
			return event, err
		}
	}
	event.SucceededAt = event.SucceededAt.UTC()
	return event, nil
}

func (a *App) confirmRefundEvent(ctx context.Context, _ paymentEvent, payload []byte) error {
	event, err := parseRefundSuccessEvent(payload)
	if err != nil {
		return err
	}
	var id int64
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM refunds WHERE out_refund_no=?`, event.OutRefundNo).Scan(&id); err != nil {
		return err
	}
	if event.RefundID != "" && event.RefundID != toStringID(id) {
		return errors.New("refund identity mismatch; reconciliation required")
	}
	return a.confirmRefundAmount(ctx, id, event.ProviderRefundID, event.AmountCents, event.SucceededAt, "WECHAT")
}

func (a *App) confirmRefund(ctx context.Context, refundID int64, providerID string, occurred time.Time, source string) error {
	var amount int64
	if err := a.db.QueryRowContext(ctx, `SELECT amount_cents FROM refunds WHERE id=?`, refundID).Scan(&amount); err != nil {
		return err
	}
	return a.confirmRefundAmount(ctx, refundID, providerID, amount, occurred, source)
}

func (a *App) confirmRefundAmount(ctx context.Context, refundID int64, providerID string, amount int64, occurred time.Time, source string) error {
	if amount <= 0 {
		return errors.New("refund amount must be positive")
	}
	if providerID == "" || strings.TrimSpace(providerID) != providerID || len(providerID) > 128 {
		return errors.New("missing or invalid provider refund id")
	}
	if occurred.IsZero() {
		return errors.New("missing refund success time; reconciliation required")
	}
	if !isOneOf(source, "WECHAT", "SIMULATED") {
		return errors.New("invalid refund confirmation source")
	}
	occurred = occurred.UTC().Truncate(time.Microsecond)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var orderID, refundAmount int64
	var status string
	var storedProvider sql.NullString
	var storedSucceeded sql.NullTime
	// All refund commands lock order -> refund -> item, matching creation/restock.
	if err := tx.QueryRowContext(ctx, `SELECT order_id FROM refunds WHERE id=?`, refundID).Scan(&orderID); err != nil {
		return err
	}
	var paymentStatus string
	var goodsCents int64
	if err := tx.QueryRowContext(ctx, `SELECT payment_status,goods_cents FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&paymentStatus, &goodsCents); err != nil {
		return err
	}
	if blocked, err := refundRecoveryBlocked(ctx, tx, orderID); err != nil {
		return err
	} else if blocked {
		return errors.New("historical refunds require reconciliation")
	}
	if err := tx.QueryRowContext(ctx, `SELECT order_id,amount_cents,status,provider_refund_id,succeeded_at FROM refunds WHERE id=? FOR UPDATE`, refundID).
		Scan(&orderID, &refundAmount, &status, &storedProvider, &storedSucceeded); err != nil {
		return err
	}
	if amount != refundAmount {
		return fmt.Errorf("refund amount mismatch: event=%d local=%d", amount, refundAmount)
	}
	if status == "SUCCESS" {
		if source == "WECHAT" {
			if !storedProvider.Valid || storedProvider.String != providerID || !storedSucceeded.Valid || !storedSucceeded.Time.Equal(occurred) {
				return errors.New("successful refund provider/time mismatch; reconciliation required")
			}
			var cashAmount int64
			var cashTime time.Time
			var cashSource string
			if err := tx.QueryRowContext(ctx, `SELECT amount_cents,occurred_at,source FROM refund_cash_ledger WHERE refund_id=?`, refundID).Scan(&cashAmount, &cashTime, &cashSource); err != nil {
				return err
			}
			if cashAmount != amount || cashSource != source || !cashTime.Equal(occurred) {
				return errors.New("successful refund cash ledger mismatch; reconciliation required")
			}
		}
		return nil
	}
	if !isOneOf(status, "REQUESTED", "PROCESSING", "UNKNOWN") {
		return errors.New("refund is not awaiting confirmation")
	}
	if !isOneOf(paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		return errors.New("order is not in a refundable payment state")
	}
	rows, err := tx.QueryContext(ctx, `SELECT order_item_id,quantity FROM refund_items WHERE refund_id=? FOR UPDATE`, refundID)
	if err != nil {
		return err
	}
	type item struct {
		orderItemID int64
		quantity    int
	}
	list := []item{}
	for rows.Next() {
		var current item
		if err := rows.Scan(&current.orderItemID, &current.quantity); err != nil {
			rows.Close()
			return err
		}
		list = append(list, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, current := range list {
		result, err := tx.ExecContext(ctx, `UPDATE order_items SET refunded_quantity=refunded_quantity+?, pending_refund_quantity=pending_refund_quantity-?
			WHERE id=? AND pending_refund_quantity>=?`, current.quantity, current.quantity, current.orderItemID, current.quantity)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errors.New("refund quantity exceeds pending pre-occupation")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refunds SET status='SUCCESS',provider_refund_id=?,succeeded_at=?,version=version+1 WHERE id=?`,
		nullIfEmpty(providerID), occurred, refundID); err != nil {
		return err
	}
	var refundedTotal int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(ri.amount_cents),0) FROM refund_items ri
		JOIN refunds r ON r.id=ri.refund_id WHERE r.order_id=? AND r.status='SUCCESS'`, orderID).Scan(&refundedTotal); err != nil {
		return err
	}
	nextPaymentStatus := "PARTIAL_REFUNDED"
	if refundedTotal >= goodsCents {
		nextPaymentStatus = "REFUNDED"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET payment_status=?,version=version+1 WHERE id=?`, nextPaymentStatus, orderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO refund_cash_ledger(refund_id,amount_cents,occurred_at,source)
		VALUES (?,?,?,?)`, refundID, refundAmount, occurred, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,payload_json) VALUES (?,'REFUND_SUCCEEDED','SYSTEM',?)`,
		orderID, mustJSON(map[string]any{"refundId": toStringID(refundID), "amountCents": refundAmount})); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('REFUND_SUCCEEDED',?)`, mustJSON(map[string]any{"refundId": toStringID(refundID), "orderId": toStringID(orderID)})); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) adminRefundRestock(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	refundID, err := parseID(r.PathValue("id"), "退款ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req restockRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "回库 requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	refundItemID, err := parseID(req.RefundItemID, "refundItemId")
	if err != nil || req.Quantity <= 0 {
		writeError(w, r, badRequest("INVALID_RESTOCK", "请提供退款商品行和正整数回库数量"))
		return
	}
	requestedLot := ""
	if strings.TrimSpace(req.LotID) != "" {
		parsed, err := parseID(req.LotID, "lotId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		requestedLot = toStringID(parsed)
	}
	fingerprint, _ := hashCanonical(map[string]any{"refundId": toStringID(refundID), "refundItemId": toStringID(refundItemID), "lotId": requestedLot, "quantity": req.Quantity})
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "回库失败"})
		return
	}
	defer tx.Rollback()
	var storeID, orderID, orderItemID, skuID, lotID int64
	if err := tx.QueryRowContext(r.Context(), `SELECT id FROM orders WHERE id=(SELECT order_id FROM refunds WHERE id=?) FOR UPDATE`, refundID).Scan(&orderID); err != nil {
		writeError(w, r, notFound("退款不存在"))
		return
	}
	var refundStatus string
	var allowedQuantity int
	err = tx.QueryRowContext(r.Context(), `SELECT o.store_id,o.id,ri.id,ri.order_item_id,oi.sku_id,r.status,ri.quantity
		FROM refunds r JOIN orders o ON o.id=r.order_id JOIN refund_items ri ON ri.refund_id=r.id
		JOIN order_items oi ON oi.id=ri.order_item_id
		WHERE r.id=? AND ri.id=? FOR UPDATE`, refundID, refundItemID).
		Scan(&storeID, &orderID, &refundItemID, &orderItemID, &skuID, &refundStatus, &allowedQuantity)
	if err != nil {
		writeError(w, r, notFound("退款商品行不存在"))
		return
	}
	if storeID != p.Staff.StoreID {
		writeError(w, r, forbidden("无权操作其他门店退款"))
		return
	}
	if blocked, err := refundRecoveryBlocked(r.Context(), tx, orderID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取退款核对状态失败"})
		return
	} else if blocked {
		writeError(w, r, conflict("REFUND_REQUIRES_RECONCILIATION", "历史退款账目异常，订单已隔离"))
		return
	}
	var existingItem int64
	var existingHash sql.NullString
	err = tx.QueryRowContext(r.Context(), `SELECT refund_item_id,request_hash FROM refund_restocks WHERE actor_id=? AND request_id=? FOR UPDATE`, p.UserID, req.RequestID).Scan(&existingItem, &existingHash)
	if err == nil {
		if existingItem != refundItemID || !existingHash.Valid || existingHash.String != fingerprint {
			writeError(w, r, conflict("IDEMPOTENCY_CONFLICT", "同一回库 requestId 携带了不同参数"))
			return
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true, "replayed": true})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取回库请求失败"})
		return
	}
	if refundStatus != "SUCCESS" {
		writeError(w, r, conflict("REFUND_NOT_SUCCESS", "只有成功退款才能做实物回库"))
		return
	}
	var restocked int64
	if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(quantity),0) FROM refund_restocks WHERE refund_item_id=?`, refundItemID).Scan(&restocked); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取累计回库数量失败"})
		return
	}
	if restocked < 0 || restocked > int64(allowedQuantity) || int64(req.Quantity) > int64(allowedQuantity)-restocked {
		writeError(w, r, conflict("RESTOCK_QUANTITY_EXCEEDED", "累计回库数量不能超过退款数量"))
		return
	}
	businessDate := a.now().In(a.tz).Format("2006-01-02")
	if strings.TrimSpace(req.LotID) != "" {
		lotID, err = parseID(req.LotID, "lotId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		var lotStoreID, lotSkuID int64
		if err := tx.QueryRowContext(r.Context(), `SELECT store_id,sku_id FROM inventory_lots WHERE id=? AND expiry_date>=? FOR UPDATE`, lotID, businessDate).
			Scan(&lotStoreID, &lotSkuID); err != nil {
			writeError(w, r, notFound("回库批次不存在或已过期"))
			return
		}
		if lotStoreID != storeID || lotSkuID != skuID {
			writeError(w, r, conflict("LOT_SKU_MISMATCH", "回库批次必须与退款商品 SKU 一致"))
			return
		}
	} else {
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM inventory_lots WHERE store_id=? AND sku_id=? AND expiry_date>=?
			ORDER BY expiry_date,id LIMIT 1 FOR UPDATE`, storeID, skuID, businessDate).Scan(&lotID); err != nil {
			writeError(w, r, conflict("LOT_REQUIRED", "没有可回库的批次，请先建立批次"))
			return
		}
	}
	if lotID == 0 {
		writeError(w, r, conflict("LOT_REQUIRED", "没有可回库的批次，请先建立批次"))
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE inventory_lots SET qty_on_hand=qty_on_hand+?,version=version+1 WHERE id=? AND store_id=? AND sku_id=?`,
		req.Quantity, lotID, storeID, skuID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "回库失败"})
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, r, conflict("LOT_SKU_MISMATCH", "回库批次与退款商品不一致"))
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO refund_restocks(refund_item_id,lot_id,quantity,request_id,request_hash,actor_id) VALUES (?,?,?,?,?,?)`,
		refundItemID, lotID, req.Quantity, req.RequestID, fingerprint, p.UserID); err != nil {
		if isDuplicateKey(err) {
			writeError(w, r, conflict("IDEMPOTENCY_CONFLICT", "回库请求键已用于其他操作"))
			return
		}
		writeError(w, r, &apiError{500, "DB_ERROR", "写入回库记录失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,ref_type,ref_id,actor_id)
		VALUES (?,?,?,'REFUND_RESTOCK',?,'REFUND',?,?)`, storeID, skuID, lotID, req.Quantity, toStringID(refundID), p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入库存流水失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,'REFUND_RESTOCKED','STAFF',?,?)`,
		orderID, p.UserID, mustJSON(map[string]any{"refundId": toStringID(refundID), "quantity": req.Quantity})); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入回库事件失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "回库失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) loadRefundsByOrder(ctx context.Context, orderID int64) ([]Refund, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,out_refund_no,amount_cents,status,reason,requested_by,succeeded_at,created_at
		FROM refunds WHERE order_id=? ORDER BY id DESC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Refund{}
	for rows.Next() {
		var item Refund
		var id, requestedBy int64
		var succeeded sql.NullTime
		if err := rows.Scan(&id, &item.OutRefundNo, &item.AmountCents, &item.Status, &item.Reason, &requestedBy, &succeeded, &item.CreatedAt); err != nil {
			continue
		}
		item.ID = toStringID(id)
		item.OrderID = toStringID(orderID)
		item.RequestedBy = toStringID(requestedBy)
		if succeeded.Valid {
			t := succeeded.Time.UTC()
			item.SucceededAt = &t
		}
		item.Items = a.loadRefundItems(ctx, id)
		items = append(items, item)
	}
	return items, nil
}

func (a *App) loadRefundItems(ctx context.Context, refundID int64) []RefundItem {
	rows, err := a.db.QueryContext(ctx, `SELECT ri.id,ri.order_item_id,oi.sku_id,ri.quantity,ri.amount_cents,oi.product_name,oi.spec_name,
		(SELECT COALESCE(SUM(rr.quantity),0) FROM refund_restocks rr WHERE rr.refund_item_id=ri.id)
		FROM refund_items ri JOIN order_items oi ON oi.id=ri.order_item_id WHERE ri.refund_id=?`, refundID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []RefundItem{}
	for rows.Next() {
		var item RefundItem
		var id, orderItemID, skuID int64
		if err := rows.Scan(&id, &orderItemID, &skuID, &item.Quantity, &item.AmountCents, &item.ProductName, &item.SpecName, &item.RestockedQuantity); err != nil {
			continue
		}
		item.ID = toStringID(id)
		item.SKUID = toStringID(skuID)
		item.OrderItemID = toStringID(orderItemID)
		items = append(items, item)
	}
	return items
}
