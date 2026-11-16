package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUrgeCooldownSeconds = 300
	defaultUrgeMaxPerOrder     = 3
)

type urgeCreateRequest struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

type urgeCreateResponse struct {
	Urge          UrgeRecord `json:"urge"`
	Replayed      bool       `json:"replayed"`
	Count         int        `json:"count"`
	MaxPerOrder   int        `json:"maxPerOrder"`
	Remaining     int        `json:"remaining"`
	NextAllowedAt *time.Time `json:"nextAllowedAt,omitempty"`
}

type urgeHandleRequest struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Note            string `json:"note"`
}

type urgePolicyRequest struct {
	CooldownSeconds int   `json:"cooldownSeconds"`
	MaxPerOrder     int   `json:"maxPerOrder"`
	Enabled         bool  `json:"enabled"`
	ExpectedVersion int64 `json:"expectedVersion"`
}

func (a *App) loadUrgePolicy(ctx context.Context, q queryer, storeID int64) (UrgePolicy, error) {
	var policy UrgePolicy
	err := q.QueryRowContext(ctx, `SELECT cooldown_seconds,max_per_order,enabled,version FROM urge_policies WHERE store_id=?`, storeID).
		Scan(&policy.CooldownSeconds, &policy.MaxPerOrder, &policy.Enabled, &policy.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return UrgePolicy{CooldownSeconds: defaultUrgeCooldownSeconds, MaxPerOrder: defaultUrgeMaxPerOrder, Enabled: true}, nil
	}
	return policy, err
}

func urgeCooldownReason(next time.Time, loc *time.Location) string {
	return "冷却中，请在 " + next.In(loc).Format("15:04") + " 后再试"
}

func (a *App) createUrge(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID, err := parseID(r.PathValue("id"), "订单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req urgeCreateRequest
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
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "顾客催单，请尽快处理"
	}
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	fingerprint, _ := hashCanonical(map[string]any{"orderId": orderID, "reason": reason})
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "提交催单失败"})
		return
	}
	defer tx.Rollback()
	// A real row serializes a user's key even when it targets two different
	// orders. Acquiring this before order locks avoids competing gap locks.
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO urge_request_locks(requester_id,request_id)
		VALUES (?,?) ON DUPLICATE KEY UPDATE requester_id=requester_id`, p.UserID, req.RequestID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "锁定催单请求失败"})
		return
	}
	var buyerID, storeID int64
	var status, paymentStatus string
	err = tx.QueryRowContext(r.Context(), `SELECT buyer_id,store_id,status,payment_status FROM orders WHERE id=? FOR UPDATE`, orderID).
		Scan(&buyerID, &storeID, &status, &paymentStatus)
	if errors.Is(err, sql.ErrNoRows) || buyerID != p.UserID {
		writeError(w, r, notFound("订单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取订单失败"})
		return
	}
	// Idempotent replay is checked after the order lock so a concurrent same-key
	// request cannot bypass the cooldown or create a second record.
	var existingID int64
	var existingHash string
	err = tx.QueryRowContext(r.Context(), `SELECT id,request_hash FROM order_urges
		WHERE requester_id=? AND request_id=? FOR UPDATE`, p.UserID, req.RequestID).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != fingerprint {
			writeError(w, r, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同催单参数"))
			return
		}
		record, _ := a.loadUrgeRecord(r.Context(), existingID)
		summary, _ := a.loadUrgeSummary(r.Context(), orderID, p.UserID, false, status, paymentStatus)
		response := urgeCreateResponse{Urge: record, Replayed: true}
		if summary != nil {
			response.Count = summary.Count
			response.MaxPerOrder = summary.MaxPerOrder
			response.Remaining = summary.MaxPerOrder - summary.Count
			response.NextAllowedAt = summary.NextAt
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), response)
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单记录失败"})
		return
	}
	policy, err := a.loadUrgePolicy(r.Context(), tx, storeID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单策略失败"})
		return
	}
	if !policy.Enabled {
		writeError(w, r, conflict("URGE_DISABLED", "该门店暂未开启催单"))
		return
	}
	if !isOneOf(status, "PAID", "PICKING", "READY", "DELIVERING") {
		writeError(w, r, conflict("URGE_STATE_NOT_ALLOWED", "当前订单状态不可催单"))
		return
	}
	if !isOneOf(paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		writeError(w, r, conflict("URGE_PAYMENT_NOT_CONFIRMED", "支付尚未由服务端确认，暂不能催单"))
		return
	}
	var count int
	var last sql.NullTime
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*),MAX(created_at) FROM order_urges WHERE order_id=?`, orderID).Scan(&count, &last); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单次数失败"})
		return
	}
	if count >= policy.MaxPerOrder {
		writeError(w, r, conflict("URGE_LIMIT_REACHED", fmt.Sprintf("该订单最多催 %d 次，已用完", policy.MaxPerOrder)))
		return
	}
	now := a.now().UTC()
	if last.Valid {
		next := last.Time.Add(time.Duration(policy.CooldownSeconds) * time.Second)
		if now.Before(next) {
			writeError(w, r, conflict("URGE_COOLDOWN", urgeCooldownReason(next, a.tz)))
			return
		}
	}
	target := "STORE"
	var courierID int64
	var taskStatus string
	taskErr := tx.QueryRowContext(r.Context(), `SELECT COALESCE(assignee_id,0),status FROM delivery_tasks WHERE order_id=?`, orderID).
		Scan(&courierID, &taskStatus)
	if status == "DELIVERING" && taskErr == nil && courierID > 0 && isOneOf(taskStatus, "ACCEPTED", "PICKED_UP", "DELIVERING") {
		target = "STORE_AND_COURIER"
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO order_urges
		(order_id,store_id,requester_id,request_id,request_hash,reason,status,notification_status,notification_target,urge_number)
		VALUES (?,?,?,?,?,?,'RECEIVED','PENDING',?,?)`,
		orderID, storeID, p.UserID, req.RequestID, fingerprint, reason, target, count+1)
	if err != nil {
		if isDuplicateKey(err) {
			_ = tx.Rollback()
			existing, apiErr := a.loadUrgeByRequest(r.Context(), p.UserID, req.RequestID)
			if apiErr != nil {
				writeError(w, r, apiErr)
				return
			}
			var existingHash string
			if err := a.db.QueryRowContext(r.Context(), `SELECT request_hash FROM order_urges WHERE requester_id=? AND request_id=?`, p.UserID, req.RequestID).Scan(&existingHash); err != nil {
				writeError(w, r, &apiError{500, "DB_ERROR", "读取催单指纹失败"})
				return
			}
			if existingHash != fingerprint {
				writeError(w, r, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同催单参数"))
				return
			}
			writeJSON(w, http.StatusOK, traceIDFrom(r), urgeCreateResponse{Urge: existing, Replayed: true})
			return
		}
		writeError(w, r, &apiError{500, "DB_ERROR", "提交催单失败"})
		return
	}
	urgeID, _ := result.LastInsertId()
	payload := mustJSON(map[string]any{"urgeId": toStringID(urgeID), "orderId": toStringID(orderID), "storeId": toStringID(storeID)})
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO outbox(topic,payload_json) VALUES ('URGE_CREATED',?)`, payload); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "提交催单失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,'ORDER_URGED','CUSTOMER',?,?)`,
		orderID, p.UserID, mustJSON(map[string]any{"urgeId": toStringID(urgeID), "urgeNumber": count + 1, "reason": reason})); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入催单事件失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "提交催单失败"})
		return
	}
	record, _ := a.loadUrgeRecord(r.Context(), urgeID)
	nextAllowed := now.Add(time.Duration(policy.CooldownSeconds) * time.Second)
	writeJSON(w, http.StatusCreated, traceIDFrom(r), urgeCreateResponse{
		Urge: record, Replayed: false, Count: count + 1, MaxPerOrder: policy.MaxPerOrder,
		Remaining: policy.MaxPerOrder - count - 1, NextAllowedAt: &nextAllowed,
	})
}

func (a *App) loadUrgeByRequest(ctx context.Context, requesterID int64, requestID string) (UrgeRecord, *apiError) {
	var id int64
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM order_urges WHERE requester_id=? AND request_id=?`, requesterID, requestID).Scan(&id); err != nil {
		return UrgeRecord{}, &apiError{500, "DB_ERROR", "读取原催单失败"}
	}
	record, err := a.loadUrgeRecord(ctx, id)
	if err != nil {
		return UrgeRecord{}, &apiError{500, "DB_ERROR", "读取原催单失败"}
	}
	return record, nil
}

func (a *App) loadUrgeRecord(ctx context.Context, urgeID int64) (UrgeRecord, error) {
	var item UrgeRecord
	var id, orderID, requesterID, urgeNumber, version int64
	var handledBy sql.NullInt64
	var handledAt sql.NullTime
	var requesterName, handlerName string
	err := a.db.QueryRowContext(ctx, `SELECT u.id,u.order_id,o.order_no,u.requester_id,COALESCE(ru.nickname,''),
		u.reason,u.status,u.notification_status,u.notification_target,u.urge_number,u.version,u.created_at,
		u.handled_by,COALESCE(hu.nickname,''),u.handled_at,u.handle_note
		FROM order_urges u JOIN orders o ON o.id=u.order_id
		LEFT JOIN users ru ON ru.id=u.requester_id LEFT JOIN users hu ON hu.id=u.handled_by
		WHERE u.id=?`, urgeID).Scan(&id, &orderID, &item.OrderNo, &requesterID, &requesterName,
		&item.Reason, &item.Status, &item.NotificationStatus, &item.NotificationTarget, &urgeNumber, &version,
		&item.CreatedAt, &handledBy, &handlerName, &handledAt, &item.HandleNote)
	if err != nil {
		return item, err
	}
	item.ID = toStringID(id)
	item.OrderID = toStringID(orderID)
	item.RequesterID = toStringID(requesterID)
	item.RequesterName = requesterName
	item.UrgeNumber = int(urgeNumber)
	item.Version = version
	if handledBy.Valid {
		item.HandledBy = toStringID(handledBy.Int64)
	}
	item.HandlerName = handlerName
	if handledAt.Valid {
		t := handledAt.Time.UTC()
		item.HandledAt = &t
	}
	return item, nil
}

func (a *App) loadUrgeRecords(ctx context.Context, orderID int64, limit int) []UrgeRecord {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := a.db.QueryContext(ctx, `SELECT u.id FROM order_urges u WHERE u.order_id=? ORDER BY u.id DESC LIMIT ?`, orderID, limit)
	if err != nil {
		return []UrgeRecord{}
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	items := make([]UrgeRecord, 0, len(ids))
	for _, id := range ids {
		if item, err := a.loadUrgeRecord(ctx, id); err == nil {
			items = append(items, item)
		}
	}
	return items
}

func (a *App) loadUrgeSummary(ctx context.Context, orderID, requesterID int64, isAdmin bool, status, paymentStatus string) (*UrgeSummary, error) {
	var storeID int64
	if err := a.db.QueryRowContext(ctx, `SELECT store_id FROM orders WHERE id=?`, orderID).Scan(&storeID); err != nil {
		return nil, err
	}
	policy, err := a.loadUrgePolicy(ctx, a.db, storeID)
	if err != nil {
		return nil, err
	}
	summary := &UrgeSummary{MaxPerOrder: policy.MaxPerOrder, CooldownSeconds: policy.CooldownSeconds, Records: []UrgeRecord{}}
	var last sql.NullTime
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status='RECEIVED'),0),MAX(created_at) FROM order_urges WHERE order_id=?`, orderID).
		Scan(&summary.Count, &summary.PendingCount, &last); err != nil {
		return nil, err
	}
	if last.Valid {
		t := last.Time.UTC()
		summary.LastAt = &t
		if summary.Count < policy.MaxPerOrder {
			next := last.Time.Add(time.Duration(policy.CooldownSeconds) * time.Second)
			summary.NextAt = &next
		}
	}
	if isAdmin {
		summary.Allowed = false
		summary.Reason = "员工端只读，请在订单详情处理催单"
	} else if !policy.Enabled {
		summary.Allowed = false
		summary.Reason = "该门店暂未开启催单"
	} else if !isOneOf(status, "PAID", "PICKING", "READY", "DELIVERING") {
		summary.Allowed = false
		summary.Reason = "当前订单状态不可催单"
	} else if !isOneOf(paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		summary.Allowed = false
		summary.Reason = "支付尚未由服务端确认"
	} else if summary.Count >= policy.MaxPerOrder {
		summary.Allowed = false
		summary.Reason = fmt.Sprintf("该订单最多催 %d 次，已用完", policy.MaxPerOrder)
	} else if summary.NextAt != nil && a.now().UTC().Before(*summary.NextAt) {
		summary.Allowed = false
		summary.Reason = urgeCooldownReason(*summary.NextAt, a.tz)
	} else if requesterID <= 0 {
		summary.Allowed = false
		summary.Reason = "需要本人订单才能催单"
	} else {
		summary.Allowed = true
		summary.Reason = "可以催单"
	}
	summary.Records = a.loadUrgeRecords(ctx, orderID, 50)
	return summary, nil
}

func (a *App) adminUrges(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := pageLimit(r.URL.Query().Get("limit"))
	cursorTime, cursorID, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	query := `SELECT id FROM order_urges WHERE store_id=?`
	args := []any{p.Staff.StoreID}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " AND (? IS NULL OR created_at < ? OR (created_at = ? AND id < ?)) ORDER BY created_at DESC,id DESC LIMIT ?"
	var cursorArg any
	if !cursorTime.IsZero() {
		cursorArg = cursorTime
	}
	args = append(args, cursorArg, cursorArg, cursorArg, cursorID, limit)
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单失败"})
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	items := []UrgeRecord{}
	for _, id := range ids {
		if item, err := a.loadUrgeRecord(r.Context(), id); err == nil {
			items = append(items, item)
		}
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		last := items[len(items)-1]
		lastID, _ := strconv.ParseInt(last.ID, 10, 64)
		next = encodeCursor(last.CreatedAt, lastID)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"items": items, "nextCursor": next})
}

func (a *App) adminHandleUrge(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	urgeID, err := parseID(r.PathValue("id"), "催单ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req urgeHandleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理催单失败"})
		return
	}
	defer tx.Rollback()
	var orderID, storeID, version int64
	var status string
	// Discover immutable identity without locking, then use the same order ->
	// urge lock order as notification delivery. The event FK also locks order.
	err = tx.QueryRowContext(r.Context(), `SELECT order_id FROM order_urges WHERE id=? AND store_id=?`, urgeID, p.Staff.StoreID).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("催单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单失败"})
		return
	}
	err = tx.QueryRowContext(r.Context(), `SELECT store_id FROM orders WHERE id=? AND store_id=? FOR UPDATE`, orderID, p.Staff.StoreID).Scan(&storeID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "锁定催单订单失败"})
		return
	}
	err = tx.QueryRowContext(r.Context(), `SELECT order_id,store_id,status,version FROM order_urges WHERE id=? FOR UPDATE`, urgeID).
		Scan(&orderID, &storeID, &status, &version)
	if errors.Is(err, sql.ErrNoRows) || storeID != p.Staff.StoreID {
		writeError(w, r, notFound("催单不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单失败"})
		return
	}
	if status == "HANDLED" {
		// Release locks/connection before the separate read. HANDLED is final;
		// this prevents concurrent replays exhausting the connection pool.
		if err := tx.Rollback(); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "结束催单事务失败"})
			return
		}
		record, err := a.loadUrgeRecord(r.Context(), urgeID)
		if err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取已处理催单失败"})
			return
		}
		writeJSON(w, http.StatusOK, traceIDFrom(r), record)
		return
	}
	if version != req.ExpectedVersion {
		writeError(w, r, conflict("VERSION_CONFLICT", "催单已被其他人处理，请刷新"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		note = "已查看并安排处理"
	}
	if len([]rune(note)) > 255 {
		note = string([]rune(note)[:255])
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE order_urges SET status='HANDLED',handled_by=?,handled_at=NOW(6),
		handle_note=?,version=version+1 WHERE id=?`, p.UserID, note, urgeID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理催单失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,'URGE_HANDLED','STAFF',?,?)`,
		orderID, p.UserID, mustJSON(map[string]any{"urgeId": toStringID(urgeID), "note": note})); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入催单事件失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE notifications SET read_at=NOW(6) WHERE urge_id=? AND recipient_type='STORE' AND recipient_id=?`,
		urgeID, p.Staff.StoreID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新催单提醒失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id)
		VALUES (?,?,?,?,?,?,?)`, p.UserID, p.Staff.StoreID, "HANDLE_URGE", "ORDER_URGE", toStringID(urgeID),
		mustJSON(map[string]any{"orderId": toStringID(orderID), "note": note}), traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入催单审计失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理催单失败"})
		return
	}
	record, err := a.loadUrgeRecord(r.Context(), urgeID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理已提交，请重新读取催单结果"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), record)
}

func (a *App) adminUrgePolicy(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	policy, err := a.loadUrgePolicy(r.Context(), a.db, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取催单策略失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), policy)
}

func (a *App) adminUpdateUrgePolicy(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req urgePolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if req.CooldownSeconds < 300 || req.CooldownSeconds > 86400 || req.MaxPerOrder < 1 || req.MaxPerOrder > 3 {
		writeError(w, r, badRequest("INVALID_URGE_POLICY", "冷却时间 300-86400 秒，每单最多 1-3 次"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新催单策略失败"})
		return
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRowContext(r.Context(), `SELECT version FROM urge_policies WHERE store_id=? FOR UPDATE`, p.Staff.StoreID).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新催单策略失败"})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO urge_policies(store_id,cooldown_seconds,max_per_order,enabled,version)
			VALUES (?,?,?,?,1)`, p.Staff.StoreID, req.CooldownSeconds, req.MaxPerOrder, boolInt(req.Enabled)); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "更新催单策略失败"})
			return
		}
		version = 1
	} else {
		if req.ExpectedVersion != version {
			writeError(w, r, conflict("VERSION_CONFLICT", "催单策略已被其他人更新，请刷新"))
			return
		}
		version++
		if _, err := tx.ExecContext(r.Context(), `UPDATE urge_policies SET cooldown_seconds=?,max_per_order=?,enabled=?,version=? WHERE store_id=?`,
			req.CooldownSeconds, req.MaxPerOrder, boolInt(req.Enabled), version, p.Staff.StoreID); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "更新催单策略失败"})
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id)
		VALUES (?,?,?,?,?,?,?)`, p.UserID, p.Staff.StoreID, "UPDATE_URGE_POLICY", "STORE", toStringID(p.Staff.StoreID),
		mustJSON(req), traceIDFrom(r)); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "写入催单策略审计失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新催单策略失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), UrgePolicy{CooldownSeconds: req.CooldownSeconds, MaxPerOrder: req.MaxPerOrder, Enabled: req.Enabled, Version: version})
}

func (a *App) adminUrgeNotifications(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,order_id,urge_id,title,body,read_at,created_at FROM notifications
		WHERE recipient_type='STORE' AND recipient_id=? ORDER BY id DESC LIMIT 100`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取站内提醒失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, orderID, urgeID int64
		var title, body string
		var readAt sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &orderID, &urgeID, &title, &body, &readAt, &createdAt); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": toStringID(id), "orderId": toStringID(orderID), "urgeId": toStringID(urgeID),
			"title": title, "body": body, "readAt": readAt, "createdAt": createdAt,
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) adminReadUrgeNotification(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	notificationID, err := parseID(r.PathValue("id"), "通知ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE notifications SET read_at=NOW(6)
		WHERE id=? AND recipient_type='STORE' AND recipient_id=?`, notificationID, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新提醒失败"})
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeError(w, r, notFound("提醒不存在"))
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) startUrgeOutboxWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.processUrgeOutboxBatch(ctx)
		}
	}
}

func (a *App) processUrgeOutboxBatch(ctx context.Context) {
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM outbox
		WHERE topic='URGE_CREATED' AND status IN ('PENDING','PROCESSING') AND next_run_at<=NOW(6)
		ORDER BY id LIMIT 20`)
	if err != nil {
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := a.processUrgeOutbox(ctx, id); err != nil {
			a.log.Error("urge outbox failed", "outboxId", id, "error", err)
		}
	}
}

// Station notifications are local database writes: hold the outbox row lock
// through delivery and acknowledgement. A crash rolls everything back; another
// instance skips this locked row, and no stale worker can overwrite DONE.
func (a *App) processUrgeOutbox(ctx context.Context, outboxID int64) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT payload_json,attempts FROM outbox
   WHERE id=? AND topic='URGE_CREATED' AND status IN ('PENDING','PROCESSING')
   AND next_run_at<=UTC_TIMESTAMP(6) FOR UPDATE SKIP LOCKED`, outboxID).Scan(&payload, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	attempts++
	if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status='PROCESSING',attempts=? WHERE id=?`, attempts, outboxID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT notification_delivery`); err != nil {
		return err
	}
	deliveryErr := a.deliverUrgeNotification(ctx, tx, payload)
	if deliveryErr == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status='DONE',processed_at=UTC_TIMESTAMP(6) WHERE id=?`, outboxID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notification_delivery`); err != nil {
		return deliveryErr
	}
	status := "PENDING"
	if attempts >= 5 {
		status = "FAILED"
	}
	next := a.now().UTC().Add(time.Duration(minInt(300, 1<<minInt(attempts, 8))) * time.Second)
	if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status=?,next_run_at=? WHERE id=?`, status, next, outboxID); err != nil {
		return err
	}
	if status == "FAILED" {
		var event struct {
			UrgeID string `json:"urgeId"`
		}
		if json.Unmarshal(payload, &event) == nil {
			if urgeID, parseErr := strconv.ParseInt(event.UrgeID, 10, 64); parseErr == nil {
				if _, err = tx.ExecContext(ctx, `UPDATE order_urges SET notification_status='FAILED' WHERE id=? AND notification_status='PENDING'`, urgeID); err != nil {
					return err
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return deliveryErr
}

func (a *App) deliverUrgeNotification(ctx context.Context, tx *sql.Tx, payload []byte) error {
	var event struct {
		UrgeID string `json:"urgeId"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	urgeID, err := strconv.ParseInt(event.UrgeID, 10, 64)
	if err != nil {
		return err
	}
	var orderID, storeID int64
	if err = tx.QueryRowContext(ctx, `SELECT order_id FROM order_urges WHERE id=?`, urgeID).Scan(&orderID); err != nil {
		return err
	}
	var orderStatus, paymentStatus, orderNo string
	if err = tx.QueryRowContext(ctx, `SELECT status,payment_status,order_no,store_id FROM orders WHERE id=? FOR UPDATE`, orderID).Scan(&orderStatus, &paymentStatus, &orderNo, &storeID); err != nil {
		return err
	}
	var reason, notificationStatus string
	var urgeNumber int
	if err = tx.QueryRowContext(ctx, `SELECT reason,notification_status,urge_number FROM order_urges WHERE id=? FOR UPDATE`, urgeID).Scan(&reason, &notificationStatus, &urgeNumber); err != nil {
		return err
	}
	if notificationStatus == "SENT" || notificationStatus == "CANCELLED" {
		return nil
	}
	if !isOneOf(orderStatus, "PAID", "PICKING", "READY", "DELIVERING") || !isOneOf(paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		_, err = tx.ExecContext(ctx, `UPDATE order_urges SET notification_status='CANCELLED' WHERE id=?`, urgeID)
		return err
	}
	target := "STORE"
	var courierID int64
	var taskStatus string
	taskErr := tx.QueryRowContext(ctx, `SELECT COALESCE(assignee_id,0),status FROM delivery_tasks WHERE order_id=? FOR UPDATE`, orderID).Scan(&courierID, &taskStatus)
	if taskErr != nil && !errors.Is(taskErr, sql.ErrNoRows) {
		return taskErr
	}
	if orderStatus == "DELIVERING" && taskErr == nil && courierID > 0 && isOneOf(taskStatus, "ACCEPTED", "PICKED_UP", "DELIVERING") {
		target = "STORE_AND_COURIER"
	}
	title := "用户已催单"
	body := fmt.Sprintf("订单 %s 第 %d 次催单：%s", orderNo, urgeNumber, truncate(reason, 120))
	if _, err = tx.ExecContext(ctx, `INSERT INTO notifications(recipient_type,recipient_id,store_id,order_id,urge_id,title,body)
  VALUES ('STORE',?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, storeID, storeID, orderID, urgeID, title, body); err != nil {
		return err
	}
	if target == "STORE_AND_COURIER" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO notifications(recipient_type,recipient_id,store_id,order_id,urge_id,title,body)
   VALUES ('USER',?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, courierID, storeID, orderID, urgeID, title, body); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE order_urges SET notification_status='SENT',notification_target=? WHERE id=?`, target, urgeID)
	return err
}
