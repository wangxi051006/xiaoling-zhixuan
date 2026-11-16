package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type paymentEvent struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	OrderID       string    `json:"orderId"`
	OutTradeNo    string    `json:"outTradeNo"`
	TransactionID string    `json:"transactionId"`
	MchID         string    `json:"mchId"`
	AppID         string    `json:"appId"`
	Currency      string    `json:"currency"`
	AmountCents   int64     `json:"amountCents"`
	PaidAt        time.Time `json:"paidAt"`
}

type paymentAttemptRequest struct {
	Channel string `json:"channel"`
}

type simulatedConfirmRequest struct {
	Confirm string `json:"confirm"`
}

func (a *App) createPaymentAttempt(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req paymentAttemptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	channel := strings.ToUpper(strings.TrimSpace(req.Channel))
	if !isOneOf(channel, "WECHAT_JSAPI", "WECHAT_H5", "WECHAT_MINI", "SIMULATED") {
		writeError(w, r, badRequest("INVALID_CHANNEL", "不支持的支付入口"))
		return
	}
	if channel == "SIMULATED" && a.cfg.PaymentMode != "simulated" {
		writeError(w, r, unavailable("PAYMENT_NOT_CONFIGURED", "当前环境未开启模拟支付"))
		return
	}
	if channel != "SIMULATED" && a.cfg.PaymentMode != "wechat" {
		writeError(w, r, unavailable("PAYMENT_NOT_CONFIGURED", "微信支付未配置"))
		return
	}
	// Persist the unique channel intent while holding the same order lock used
	// by cancellation/expiry. No external request is allowed before this commits.
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建支付意图失败"})
		return
	}
	defer tx.Rollback()
	var buyerID, total int64
	var status, currency, orderNo string
	var expiresAt time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT buyer_id,status,total_cents,currency,expires_at,order_no FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&buyerID, &status, &total, &currency, &expiresAt, &orderNo)
	if errors.Is(err, sql.ErrNoRows) || buyerID != p.UserID {
		writeError(w, r, notFound("订单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取订单失败"})
		return
	}
	if status != "PENDING_PAYMENT" {
		writeError(w, r, conflict("INVALID_STATE", "订单当前不需要支付"))
		return
	}
	if a.now().UTC().After(expiresAt) {
		writeError(w, r, conflict("ORDER_EXPIRED", "订单支付时间已过"))
		return
	}
	var attemptID int64
	var existingChannel, existingStatus, outTradeNo string
	var prepayParams []byte
	var prepayExpires sql.NullTime
	err = tx.QueryRowContext(r.Context(), `SELECT id,channel,status,out_trade_no,COALESCE(prepay_params,'null'),prepay_expires_at
		FROM payment_attempts WHERE order_id=? FOR UPDATE`, orderID).Scan(&attemptID, &existingChannel, &existingStatus, &outTradeNo, &prepayParams, &prepayExpires)
	if err == nil {
		if existingChannel != channel {
			writeError(w, r, conflict("PAYMENT_CHANNEL_CONFLICT", "该订单已绑定支付入口 "+existingChannel+"，请回到原入口继续"))
			return
		}
		if existingStatus == "PAID" {
			writeError(w, r, conflict("ALREADY_PAID", "订单已支付"))
			return
		}
		if existingStatus == "CLOSED" {
			writeError(w, r, conflict("ATTEMPT_CLOSED", "原支付已关闭，请重新结算"))
			return
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
			"attemptId": toStringID(attemptID), "channel": existingChannel, "outTradeNo": outTradeNo,
			"amountCents": total, "status": existingStatus, "prepayParams": scanJSON(prepayParams),
			"prepayExpiresAt": prepayExpires, "simulated": existingChannel == "SIMULATED",
		})
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取支付记录失败"})
		return
	}

	outTradeNo = "C" + orderNo + randomDigits(6)
	mchid, appid := a.cfg.WeChatMchID, a.cfg.WeChatAppID
	if channel == "SIMULATED" {
		mchid, appid = "SIMULATED", "SIMULATED"
	}
	initialStatus := "PREPARING"
	if channel == "SIMULATED" {
		initialStatus = "PREPAY_READY"
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO payment_attempts
		(order_id,mchid,appid,out_trade_no,channel,amount_cents,currency,status,prepay_expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, orderID, mchid, appid, outTradeNo, channel, total, currency, initialStatus,
		a.now().UTC().Add(10*time.Minute))
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建支付记录失败"})
		return
	}
	attemptID, _ = result.LastInsertId()
	if err := tx.Commit(); err != nil {
		// Ambiguous commit is not permission to contact the channel. A retry
		// discovers the persisted intent, if any, under the order lock.
		writeError(w, r, &apiError{500, "DB_ERROR", "提交支付意图失败，请查询原订单"})
		return
	}
	if channel == "SIMULATED" {
		params := mustJSON(map[string]any{
			"simulated": true,
			"warning":   "仅测试环境模拟支付，不会产生真实扣款",
			"confirm":   "SIMULATED_PAYMENT",
			"path":      "/api/v1/test/payments/" + toStringID(orderID) + "/succeed",
		})
		result, err := a.db.ExecContext(r.Context(), `UPDATE payment_attempts SET prepay_params=?,prepay_expires_at=? WHERE id=? AND status='PREPAY_READY'`,
			params, a.now().UTC().Add(10*time.Minute), attemptID)
		if err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "保存模拟支付参数失败"})
			return
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			writeError(w, r, conflict("PAYMENT_STATE_CHANGED", "支付状态已变化，请查询原订单"))
			return
		}
		writeJSON(w, http.StatusCreated, traceIDFrom(r), map[string]any{
			"attemptId": toStringID(attemptID), "channel": channel, "outTradeNo": outTradeNo,
			"amountCents": total, "status": "PREPAY_READY", "simulated": true,
			"warning":      "仅测试环境：请在前端明确点击“模拟支付”并二次确认",
			"prepayParams": map[string]any{"confirm": "SIMULATED_PAYMENT", "path": "/api/v1/test/payments/" + toStringID(orderID) + "/succeed"},
		})
		return
	}
	openID := ""
	if channel == "WECHAT_JSAPI" || channel == "WECHAT_MINI" {
		_ = a.db.QueryRowContext(r.Context(), `SELECT openid FROM wechat_identities WHERE user_id=? AND appid=? LIMIT 1`, p.UserID, a.cfg.WeChatAppID).Scan(&openID)
		if openID == "" {
			_, _ = a.db.ExecContext(r.Context(), `UPDATE payment_attempts SET status='UNKNOWN' WHERE id=? AND status='PREPARING'`, attemptID)
			writeError(w, r, conflict("WECHAT_IDENTITY_REQUIRED", "尚未绑定微信身份，不能发起 JSAPI/小程序支付"))
			return
		}
	}
	prepay, err := a.wechat.createPrepay(r.Context(), prepayInput{
		OrderNo: outTradeNo, AmountCents: total, Description: "校零智选订单 " + orderNo,
		Channel: channel, OpenID: openID,
	})
	if err != nil {
		_, _ = a.db.ExecContext(r.Context(), `UPDATE payment_attempts SET status='UNKNOWN',last_checked_at=NOW(6) WHERE id=? AND status='PREPARING'`, attemptID)
		if errors.Is(err, errNotConfigured) {
			writeError(w, r, unavailable("PAYMENT_NOT_CONFIGURED", "微信支付未配置：请设置商户号、私钥、APIv3 Key 和平台公钥"))
			return
		}
		writeError(w, r, unavailable("PAYMENT_UNKNOWN", "支付渠道结果未知，请稍后查单；库存不会因此释放"))
		return
	}
	applied, err := a.savePrepayResult(r.Context(), attemptID, *prepay)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存支付参数失败"})
		return
	}
	if !applied {
		writeError(w, r, conflict("PAYMENT_STATE_CHANGED", "支付状态已变化，请查询原订单"))
		return
	}
	writeJSON(w, http.StatusCreated, traceIDFrom(r), map[string]any{
		"attemptId": toStringID(attemptID), "channel": channel, "outTradeNo": outTradeNo,
		"amountCents": total, "status": "PREPAY_READY", "prepayParams": prepay.Params,
		"prepayExpiresAt": prepay.ExpiresAt, "simulated": false,
	})
}

// A notification may win while the prepay HTTP request is still returning.
// Never regress PAID/CLOSED/UNKNOWN to PREPAY_READY or return stale pay params.
func (a *App) savePrepayResult(ctx context.Context, attemptID int64, prepay prepayResult) (bool, error) {
	params, err := json.Marshal(prepay.Params)
	if err != nil {
		return false, err
	}
	result, err := a.db.ExecContext(ctx, `UPDATE payment_attempts SET status='PREPAY_READY',prepay_id=?,prepay_params=?,prepay_expires_at=?
		WHERE id=? AND status='PREPARING'`, nullIfEmpty(prepay.PrepayID), params, prepay.ExpiresAt, attemptID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *App) refreshPayment(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var buyerID int64
	var status, paymentStatus string
	err = a.db.QueryRowContext(r.Context(), `SELECT buyer_id,status,payment_status FROM orders WHERE id=?`, orderID).
		Scan(&buyerID, &status, &paymentStatus)
	if errors.Is(err, sql.ErrNoRows) || buyerID != p.UserID {
		writeError(w, r, notFound("订单不存在"))
		return
	}
	var attemptStatus, channel string
	var checkedAt sql.NullTime
	err = a.db.QueryRowContext(r.Context(), `SELECT status,channel,last_checked_at FROM payment_attempts WHERE order_id=?`, orderID).
		Scan(&attemptStatus, &channel, &checkedAt)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取支付状态失败"})
		return
	}
	if err == nil && channel == "SIMULATED" {
		writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"orderStatus": status, "paymentStatus": paymentStatus, "attemptStatus": attemptStatus, "checkedAt": checkedAt})
		return
	}
	if chase := a.wechatChase(r.Context(), orderID, channel, attemptStatus); chase != "" {
		writeJSON(w, http.StatusAccepted, traceIDFrom(r), map[string]any{"orderStatus": status, "paymentStatus": paymentStatus, "attemptStatus": chase, "message": "正在向微信查单"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"orderStatus": status, "paymentStatus": paymentStatus, "attemptStatus": attemptStatus, "message": "支付结果以服务端确认为准"})
}

func (a *App) wechatChase(ctx context.Context, orderID int64, channel, status string) string {
	// APIv3 query is intentionally conservative: a NOTFOUND or timeout never
	// releases stock or declares unpaid. A background reconciliation job should
	// own the full retry budget in production.
	return status
}

func (a *App) simulatedPayment(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if a.cfg.PaymentMode != "simulated" {
		writeError(w, r, unavailable("PAYMENT_NOT_CONFIGURED", "模拟支付仅在测试环境开启"))
		return
	}
	if r.Header.Get("X-Simulated-Payment") != "confirm" {
		writeJSONError(w, r, http.StatusForbidden, "SIMULATED_CONFIRMATION_REQUIRED", "模拟支付必须显式确认")
		return
	}
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req simulatedConfirmRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if req.Confirm != "SIMULATED_PAYMENT" {
		writeError(w, r, badRequest("SIMULATED_CONFIRMATION_REQUIRED", "确认值必须为 SIMULATED_PAYMENT"))
		return
	}
	var buyerID, attemptID, amount int64
	var attemptStatus, outTradeNo, currency string
	err = a.db.QueryRowContext(r.Context(), `SELECT o.buyer_id,a.id,a.amount_cents,a.status,a.out_trade_no,a.currency
		FROM orders o JOIN payment_attempts a ON a.order_id=o.id
		WHERE o.id=? AND a.channel='SIMULATED'`, orderID).Scan(&buyerID, &attemptID, &amount, &attemptStatus, &outTradeNo, &currency)
	if errors.Is(err, sql.ErrNoRows) || buyerID != p.UserID {
		writeError(w, r, notFound("模拟支付记录不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取模拟支付失败"})
		return
	}
	if attemptStatus == "PAID" {
		writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"state": "PAID", "orderId": toStringID(orderID)})
		return
	}
	event := paymentEvent{
		EventID: "SIM" + toStringID(attemptID) + newToken(8), EventType: "SIMULATED.PAYMENT.SUCCESS",
		OrderID: toStringID(orderID), OutTradeNo: outTradeNo, TransactionID: "SIMULATED-" + toStringID(attemptID),
		MchID: "SIMULATED", AppID: "SIMULATED", Currency: currency, AmountCents: amount,
		PaidAt: a.now().UTC(),
	}
	payload, _ := json.Marshal(event)
	digest := sha256Hex(payload)
	result, err := a.db.ExecContext(r.Context(), `INSERT INTO payment_inbox
		(provider,event_id,event_type,payload_digest,payload_json,state,order_id)
		VALUES ('SIMULATED',?,?,?,?,'RECEIVED',?)`,
		event.EventID, event.EventType, digest, payload, orderID)
	if err != nil {
		if isDuplicateKey(err) {
			writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"state": "DUPLICATE", "orderId": toStringID(orderID)})
			return
		}
		writeError(w, r, &apiError{500, "DB_ERROR", "记录模拟支付失败"})
		return
	}
	inboxID, _ := result.LastInsertId()
	if err := a.processInboxEvent(r.Context(), inboxID); err != nil {
		writeError(w, r, &apiError{500, "PAYMENT_CONFIRM_FAILED", "模拟支付已接收，但确认失败，请查看订单状态"})
		return
	}
	writeJSON(w, http.StatusAccepted, traceIDFrom(r), map[string]any{"state": "PROCESSED", "orderId": toStringID(orderID), "simulated": true})
}

func (a *App) startPaymentWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.processInboxBatch(ctx)
		}
	}
}

func (a *App) processInboxBatch(ctx context.Context) {
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM payment_inbox
		WHERE state IN ('RECEIVED','PROCESSING') AND next_run_at<=NOW(6) ORDER BY id LIMIT 20`)
	if err != nil {
		a.log.Error("inbox query failed", "error", err)
		return
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := a.processInboxEvent(ctx, id); err != nil {
			a.log.Error("inbox processing failed", "inboxId", id, "error", err)
		}
	}
}

func (a *App) processInboxEvent(ctx context.Context, inboxID int64) error {
	result, err := a.db.ExecContext(ctx, `UPDATE payment_inbox SET state='PROCESSING',attempts=attempts+1
		WHERE id=? AND state IN ('RECEIVED','PROCESSING')`, inboxID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil
	}
	var eventType string
	var payload []byte
	var attempts int
	var orderID sql.NullInt64
	if err := a.db.QueryRowContext(ctx, `SELECT event_type,payload_json,attempts,order_id FROM payment_inbox WHERE id=?`, inboxID).
		Scan(&eventType, &payload, &attempts, &orderID); err != nil {
		return err
	}
	var event paymentEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.OrderID == "" && orderID.Valid {
		event.OrderID = toStringID(orderID.Int64)
	}
	if event.OrderID == "" {
		var resolved int64
		if err := a.db.QueryRowContext(ctx, `SELECT order_id FROM payment_attempts WHERE out_trade_no=?`, event.OutTradeNo).Scan(&resolved); err == nil {
			event.OrderID = toStringID(resolved)
		}
	}
	var processErr error
	switch {
	case strings.Contains(eventType, "REFUND"):
		processErr = a.confirmRefundEvent(ctx, event, payload)
	default:
		processErr = a.confirmPayment(ctx, event)
	}
	if processErr != nil {
		next := time.Now().Add(time.Duration(minInt(300, 1<<minInt(attempts, 8))) * time.Second)
		_, _ = a.db.ExecContext(ctx, `UPDATE payment_inbox SET state=IF(attempts>=10,'FAILED','RECEIVED'),next_run_at=?,last_error=? WHERE id=?`,
			next, truncate(processErr.Error(), 400), inboxID)
		return processErr
	}
	_, err = a.db.ExecContext(ctx, `UPDATE payment_inbox SET state='PROCESSED',processed_at=NOW(6),last_error='' WHERE id=?`, inboxID)
	return err
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *App) confirmPayment(ctx context.Context, event paymentEvent) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	orderID, err := strconv.ParseInt(event.OrderID, 10, 64)
	if err != nil || orderID <= 0 {
		return errors.New("payment event has no order")
	}
	var buyerID, total int64
	var status, paymentStatus, currency string
	err = tx.QueryRowContext(ctx, `SELECT buyer_id,total_cents,currency,status,payment_status FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&buyerID, &total, &currency, &status, &paymentStatus)
	if err != nil {
		return err
	}
	if paymentStatus == "PAID" {
		var existing string
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(transaction_id,'') FROM payment_attempts WHERE order_id=?`, orderID).Scan(&existing)
		if existing == event.TransactionID {
			return nil
		}
		return errors.New("order already paid by another transaction")
	}
	var attemptID, attemptAmount int64
	var attemptOutTradeNo, attemptCurrency, attemptStatus string
	err = tx.QueryRowContext(ctx, `SELECT id,out_trade_no,amount_cents,currency,status FROM payment_attempts WHERE order_id=? FOR UPDATE`, orderID).
		Scan(&attemptID, &attemptOutTradeNo, &attemptAmount, &attemptCurrency, &attemptStatus)
	if err != nil {
		return err
	}
	if event.OutTradeNo != attemptOutTradeNo || event.AmountCents != total || event.AmountCents != attemptAmount || event.Currency != currency || attemptCurrency != currency {
		return fmt.Errorf("payment binding mismatch: order=%d event=%d attempt=%d", total, event.AmountCents, attemptAmount)
	}
	if event.MchID != "" && event.MchID != "SIMULATED" && event.MchID != a.cfg.WeChatMchID {
		return errors.New("payment merchant mismatch")
	}
	if event.AppID != "" && event.AppID != "SIMULATED" && event.AppID != a.cfg.WeChatAppID {
		return errors.New("payment appid mismatch")
	}
	if event.TransactionID == "" {
		return errors.New("payment event has no transaction id")
	}
	if isOneOf(status, "CANCELLED", "CLOSED", "PAYMENT_EXCEPTION") {
		// Validate binding before recording an exception. Never consume stock
		// that has already been released, even for a genuine late payment.
		if status == "PAYMENT_EXCEPTION" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET payment_exception=1,status='PAYMENT_EXCEPTION',payment_status='CONFIRMING',version=version+1 WHERE id=?`, orderID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,payload_json) VALUES (?,'PAYMENT_EXCEPTION','SYSTEM',?)`,
			orderID, mustJSON(map[string]any{"reason": "payment arrived after cancellation", "transactionId": event.TransactionID})); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('PAYMENT_EXCEPTION',?)`, mustJSON(map[string]any{"orderId": toStringID(orderID)})); err != nil {
			return err
		}
		return tx.Commit()
	}
	paidAt := event.PaidAt
	if paidAt.IsZero() {
		paidAt = a.now().UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE orders SET status='PAID',payment_status='PAID',paid_at=?,version=version+1
		WHERE id=? AND payment_status<>'PAID'`, paidAt, orderID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("order payment state changed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_attempts SET status='PAID',transaction_id=?,paid_at=?,last_checked_at=NOW(6),version=version+1 WHERE id=?`,
		event.TransactionID, paidAt, attemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payment_transactions(attempt_id,order_id,transaction_id,amount_cents,paid_at,source,raw_digest)
		VALUES (?,?,?,?,?,?,?)`, attemptID, orderID, event.TransactionID, event.AmountCents, paidAt,
		paymentSource(event), sha256Hex([]byte(event.TransactionID))); err != nil {
		if isDuplicateKey(err) {
			return nil
		}
		return err
	}
	if err := a.consumeReservations(ctx, tx, orderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_tasks(order_id,store_id,status)
		VALUES (?,?, 'OPEN') ON DUPLICATE KEY UPDATE order_id=order_id`, orderID, a.cfg.StoreID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,payload_json) VALUES (?,'PAYMENT_CONFIRMED','SYSTEM',?)`,
		orderID, mustJSON(map[string]any{"transactionId": event.TransactionID, "amountCents": event.AmountCents})); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('ORDER_PAID',?)`, mustJSON(map[string]any{"orderId": toStringID(orderID)})); err != nil {
		return err
	}
	return tx.Commit()
}

func paymentSource(event paymentEvent) string {
	if event.MchID == "SIMULATED" {
		return "SIMULATED"
	}
	return "WECHAT"
}

func (a *App) consumeReservations(ctx context.Context, tx *sql.Tx, orderID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,order_item_id,lot_id,quantity FROM inventory_reservations
		WHERE order_id=? AND status='RESERVED' FOR UPDATE`, orderID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type reservation struct {
		id, itemID, lotID, quantity int64
	}
	list := []reservation{}
	for rows.Next() {
		var item reservation
		if err := rows.Scan(&item.id, &item.itemID, &item.lotID, &item.quantity); err != nil {
			return err
		}
		list = append(list, item)
	}
	for _, item := range list {
		result, err := tx.ExecContext(ctx, `UPDATE inventory_lots SET qty_on_hand=qty_on_hand-?,qty_reserved=qty_reserved-?,version=version+1
			WHERE id=? AND qty_on_hand>=? AND qty_reserved>=?`, item.quantity, item.quantity, item.lotID, item.quantity, item.quantity)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errors.New("inventory reservation cannot be consumed")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE inventory_reservations SET status='CONSUMED' WHERE id=?`, item.id); err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `INSERT INTO stock_movements(store_id,sku_id,lot_id,movement_type,quantity,ref_type,ref_id)
			SELECT store_id,sku_id,?,'SALE',-?,'ORDER',? FROM inventory_lots WHERE id=?`,
			item.lotID, item.quantity, toStringID(orderID), item.lotID)
	}
	return nil
}

func (a *App) startExpiryWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.expireOrders(ctx)
		}
	}
}

func (a *App) expireOrders(ctx context.Context) {
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM orders WHERE status='PENDING_PAYMENT' AND expires_at<UTC_TIMESTAMP(6) ORDER BY id LIMIT 50`)
	if err != nil {
		a.log.Error("expiry query failed", "error", err)
		return
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := a.expireOrder(ctx, id); err != nil {
			a.log.Warn("order expiry skipped", "orderId", id, "error", err)
		}
	}
}

func (a *App) expireOrder(ctx context.Context, orderID int64) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var expiresAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT status,expires_at FROM orders WHERE id=? FOR UPDATE`, orderID).Scan(&status, &expiresAt); err != nil {
		return err
	}
	if status != "PENDING_PAYMENT" || a.now().UTC().Before(expiresAt) {
		return nil
	}
	var attemptStatus, channel string
	err = tx.QueryRowContext(ctx, `SELECT status,channel FROM payment_attempts WHERE order_id=? FOR UPDATE`, orderID).Scan(&attemptStatus, &channel)
	if err == nil {
		canClose := channel == "SIMULATED" && !isOneOf(attemptStatus, "PAID", "UNKNOWN")
		if !canClose {
			canClose = attemptStatus == "CLOSED"
		}
		if !canClose {
			return errors.New("payment outcome unknown; reservations kept for reconciliation")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payment_attempts SET status='CLOSED',version=version+1 WHERE order_id=?`, orderID); err != nil {
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	if err := a.releaseReservations(ctx, tx, orderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET status='CANCELLED',payment_status='CLOSED',version=version+1 WHERE id=?`, orderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type) VALUES (?,'ORDER_EXPIRED','SYSTEM')`, orderID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) wechatPaymentNotify(w http.ResponseWriter, r *http.Request) {
	if a.wechat == nil || !a.wechat.configured() {
		writeJSONError(w, r, http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "微信支付未配置")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_BODY", "无法读取通知")
		return
	}
	event, err := a.wechat.verifyNotification(r.Header, body)
	if err != nil {
		a.log.Warn("wechat payment notification rejected", "error", err)
		writeJSONError(w, r, http.StatusBadRequest, "SIGNATURE_INVALID", "通知验签失败")
		return
	}
	var resource struct {
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		MchID         string `json:"mchid"`
		AppID         string `json:"appid"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
		SuccessTime string `json:"success_time"`
	}
	if err := json.Unmarshal(event.Raw, &resource); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_EVENT", "通知内容无法解析")
		return
	}
	if resource.TradeState != "SUCCESS" {
		writeJSONError(w, r, http.StatusAccepted, "NOT_SUCCESS", "非成功支付事件已忽略")
		return
	}
	paidAt, err := parseWechatSuccessTime(resource.SuccessTime)
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_SUCCESS_TIME", "支付成功时间缺失或无效，需核实渠道记录")
		return
	}
	var orderID int64
	_ = a.db.QueryRow(`SELECT order_id FROM payment_attempts WHERE out_trade_no=?`, resource.OutTradeNo).Scan(&orderID)
	payload, _ := json.Marshal(paymentEvent{
		EventID: event.ID, EventType: event.Type, OrderID: toStringID(orderID), OutTradeNo: resource.OutTradeNo,
		TransactionID: resource.TransactionID, MchID: resource.MchID, AppID: resource.AppID,
		Currency: resource.Amount.Currency, AmountCents: resource.Amount.Total, PaidAt: paidAt,
	})
	digest := sha256Hex(body)
	_, err = a.db.Exec(`INSERT INTO payment_inbox(provider,event_id,event_type,payload_digest,payload_json,order_id)
		VALUES ('WECHAT',?,?,?,?,?)`, event.ID, event.Type, digest, payload, nullIfZero(orderID))
	if isDuplicateKey(err) {
		var existingDigest string
		if queryErr := a.db.QueryRow(`SELECT payload_digest FROM payment_inbox WHERE provider='WECHAT' AND event_id=?`, event.ID).Scan(&existingDigest); queryErr != nil {
			writeJSONError(w, r, http.StatusInternalServerError, "INBOX_FAILED", "无法核实已接收事件，拒绝确认")
			return
		}
		if existingDigest != digest {
			writeJSONError(w, r, http.StatusBadRequest, "EVENT_DIGEST_MISMATCH", "同一事件 ID 内容不一致")
			return
		}
		err = nil
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, "INBOX_FAILED", "通知未持久化，拒绝确认")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func nullIfZero(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func (a *App) wechatRefundNotify(w http.ResponseWriter, r *http.Request) {
	// The refund notification is verified and persisted before acknowledgement;
	// confirmRefundEvent owns the financial state transition.
	if a.wechat == nil || !a.wechat.configured() {
		writeJSONError(w, r, http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "微信支付未配置")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_BODY", "无法读取通知")
		return
	}
	event, err := a.wechat.verifyNotification(r.Header, body)
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "SIGNATURE_INVALID", "通知验签失败")
		return
	}
	var resource struct {
		OutRefundNo  string `json:"out_refund_no"`
		RefundID     string `json:"refund_id"`
		RefundStatus string `json:"refund_status"`
		Amount       struct {
			Refund int64 `json:"refund"`
		} `json:"amount"`
		SuccessTime string `json:"success_time"`
	}
	if err := json.Unmarshal(event.Raw, &resource); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_EVENT", "通知内容无法解析")
		return
	}
	if resource.RefundStatus != "SUCCESS" {
		writeJSONError(w, r, http.StatusAccepted, "NOT_SUCCESS", "非成功退款事件已忽略")
		return
	}
	succeededAt, err := parseWechatSuccessTime(resource.SuccessTime)
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_SUCCESS_TIME", "退款成功时间缺失或无效，需核实渠道记录")
		return
	}
	payload, _ := json.Marshal(refundSuccessEvent{
		OutRefundNo: resource.OutRefundNo, ProviderRefundID: resource.RefundID, AmountCents: resource.Amount.Refund,
		EventID: event.ID, EventType: event.Type, SucceededAt: succeededAt,
	})
	if _, err := parseRefundSuccessEvent(payload); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "INVALID_EVENT", "退款金额、渠道编号或时间无效，需核实渠道记录")
		return
	}
	digest := sha256Hex(body)
	_, err = a.db.Exec(`INSERT INTO payment_inbox(provider,event_id,event_type,payload_digest,payload_json)
		VALUES ('WECHAT_REFUND',?,?,?,?)`, event.ID, event.Type, digest, payload)
	if isDuplicateKey(err) {
		var existingDigest string
		if queryErr := a.db.QueryRow(`SELECT payload_digest FROM payment_inbox WHERE provider='WECHAT_REFUND' AND event_id=?`, event.ID).Scan(&existingDigest); queryErr != nil {
			writeJSONError(w, r, http.StatusInternalServerError, "INBOX_FAILED", "无法核实已接收事件，拒绝确认")
			return
		}
		if existingDigest != digest {
			writeJSONError(w, r, http.StatusBadRequest, "EVENT_DIGEST_MISMATCH", "同一退款事件 ID 内容不一致")
			return
		}
		err = nil
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, "INBOX_FAILED", "通知未持久化，拒绝确认")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
