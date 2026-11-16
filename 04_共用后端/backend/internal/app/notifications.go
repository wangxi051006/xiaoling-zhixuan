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

type userNotification struct {
	ID        string     `json:"id"`
	OrderID   string     `json:"orderId"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	ReadAt    *time.Time `json:"readAt"`
}

// Both the persistent inbox and the outbox are committed with delivery. Network
// delivery is deliberately outside that transaction and cannot undo fulfillment.
func (a *App) enqueueDeliveryNotification(ctx context.Context, tx *sql.Tx, orderID, taskID int64) error {
	var buyer, storeID int64
	var address []byte
	if err := tx.QueryRowContext(ctx, `SELECT buyer_id,store_id,address_json FROM orders WHERE id=?`, orderID).Scan(&buyer, &storeID, &address); err != nil {
		return err
	}
	var snapshot struct {
		DeliveryPoint struct {
			Name string `json:"name"`
		} `json:"deliveryPoint"`
	}
	if err := json.Unmarshal(address, &snapshot); err != nil {
		return err
	}
	place := snapshot.DeliveryPoint.Name
	if place == "" {
		place = "约定配送点"
	}
	// A recipient lock is acquired before allocating an inbox ID. This guarantees
	// recipient-local commit order, so an afterId cursor cannot skip a late commit.
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_recipient_locks(recipient_id) VALUES (?) ON DUPLICATE KEY UPDATE recipient_id=recipient_id`, buyer); err != nil {
		return err
	}
	eventKey := fmt.Sprintf("DELIVERED:%d", orderID)
	result, err := tx.ExecContext(ctx, `INSERT INTO notifications(recipient_type,recipient_id,store_id,order_id,urge_id,title,body,event_key)
 VALUES ('USER',?,?,?,NULL,'骑手已送达',?,?) ON DUPLICATE KEY UPDATE id=id`, buyer, storeID, orderID, "你的订单已由骑手送达，请前往"+truncate(place, 100)+"领取。", eventKey)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM notifications WHERE recipient_type='USER' AND recipient_id=? AND event_key=?`, buyer, eventKey).Scan(&id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('DELIVERY_NOTIFICATION',?)`, mustJSON(map[string]any{"notificationId": toStringID(id), "orderId": toStringID(orderID), "taskId": toStringID(taskID)}))
	return err
}

func (a *App) registerNotificationRoutes() {
	a.mux.HandleFunc("GET /api/v1/notifications", a.auth(a.userNotifications))
	a.mux.HandleFunc("GET /api/v1/notifications/stream", a.auth(a.notificationStream))
	a.mux.HandleFunc("POST /api/v1/notifications/{id}/read", a.auth(a.readUserNotification))
}

func notificationCursor(r *http.Request) (int64, *apiError) {
	raw := r.URL.Query().Get("afterId")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, badRequest("INVALID_CURSOR", "消息游标无效")
	}
	return n, nil
}

func (a *App) queryUserNotifications(ctx context.Context, userID, after int64, limit int) ([]userNotification, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,order_id,title,body,created_at,read_at FROM notifications
 WHERE recipient_type='USER' AND recipient_id=? AND event_key LIKE 'DELIVERED:%' AND id>? ORDER BY id LIMIT ?`, userID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []userNotification{}
	for rows.Next() {
		var n userNotification
		var id, orderID int64
		var read sql.NullTime
		if err := rows.Scan(&id, &orderID, &n.Title, &n.Body, &n.CreatedAt, &read); err != nil {
			return nil, err
		}
		n.ID = toStringID(id)
		n.OrderID = toStringID(orderID)
		n.CreatedAt = n.CreatedAt.UTC()
		if read.Valid {
			v := read.Time.UTC()
			n.ReadAt = &v
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (a *App) userNotifications(w http.ResponseWriter, r *http.Request) {
	cursor, apiErr := notificationCursor(r)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 100 {
			writeError(w, r, badRequest("INVALID_LIMIT", "每次读取1—100条消息"))
			return
		}
		limit = v
	}
	p := principalFrom(r)
	items, err := a.queryUserNotifications(r.Context(), p.UserID, cursor, limit)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取消息失败"})
		return
	}
	var count int
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM notifications WHERE recipient_type='USER' AND recipient_id=? AND event_key LIKE 'DELIVERED:%' AND read_at IS NULL`, p.UserID).Scan(&count); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取未读数量失败"})
		return
	}
	next := toStringID(cursor)
	if len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeJSON(w, 200, traceIDFrom(r), map[string]any{"items": items, "unreadCount": count, "nextCursor": next, "hasMore": len(items) == limit})
}

func (a *App) readUserNotification(w http.ResponseWriter, r *http.Request) {
	id, e := parseID(r.PathValue("id"), "通知ID")
	if e != nil {
		writeError(w, r, e.(*apiError))
		return
	}
	p := principalFrom(r)
	var found int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id FROM notifications WHERE id=? AND recipient_type='USER' AND recipient_id=? AND event_key LIKE 'DELIVERED:%'`, id, p.UserID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("通知不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取通知失败"})
		return
	}
	if _, err = a.db.ExecContext(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,UTC_TIMESTAMP(6)) WHERE id=? AND recipient_type='USER' AND recipient_id=?`, id, p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新通知失败"})
		return
	}
	writeJSON(w, 200, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) notificationStream(w http.ResponseWriter, r *http.Request) {
	cursor, e := notificationCursor(r)
	if e != nil {
		writeError(w, r, e)
		return
	}
	p := principalFrom(r)
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	claims, err := a.parseToken(token)
	if err != nil {
		writeJSONError(w, r, 401, "INVALID_TOKEN", "登录已失效")
		return
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
	if _, err := fmt.Fprint(w, "retry: 1500\n: connected\n\n"); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	for {
		if a.now().UTC().Unix() >= claims.Exp {
			return
		}
		// Recheck account/session revocation while the connection is open.
		if _, err := a.loadPrincipal(r.Context(), p.UserID, claims.SV); err != nil {
			return
		}
		items, err := a.queryUserNotifications(r.Context(), p.UserID, cursor, 100)
		if err != nil {
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		for _, item := range items {
			payload, err := json.Marshal(item)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "id: %s\nevent: delivery_notification\ndata: %s\n\n", item.ID, payload); err != nil {
				return
			}
			cursor, _ = strconv.ParseInt(item.ID, 10, 64)
		}
		if len(items) == 0 {
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
		}
		if err := controller.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// For the local inbox channel delivery is durable once the inbox row exists.
// SSE instances independently tail MySQL, and reconnecting clients supply their
// last received ID. DONE means inbox persisted, not user read/network receipt.
func (a *App) startDeliveryNotificationWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.db.ExecContext(ctx, `UPDATE outbox o JOIN notifications n ON n.id=CAST(JSON_UNQUOTE(JSON_EXTRACT(o.payload_json,'$.notificationId')) AS UNSIGNED)
   SET o.status='DONE',o.processed_at=UTC_TIMESTAMP(6),o.attempts=o.attempts+1
   WHERE o.topic='DELIVERY_NOTIFICATION' AND o.status IN ('PENDING','PROCESSING') AND o.next_run_at<=UTC_TIMESTAMP(6)`); err != nil {
				a.log.Error("delivery inbox acknowledgement failed", "error", err)
			}
		}
	}
}
