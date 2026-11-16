package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// Merchant batch operations (P1).
//
// Lock order is fixed for every path: batch_operations -> batch_operation_targets
// -> batch_operation_items -> orders. Each order is handled in its own transaction
// that also records its terminal result, so a crash never leaves "order changed but
// no result" and a concurrent replay can never rewrite a committed result.
//
// A batch stores the exact (order, expectedVersion) list the merchant confirmed, so
// recovery does not depend on the browser still holding the original payload.

const batchOrderLimit = 50

type batchOrderItem struct {
	OrderID         string `json:"orderId"`
	ExpectedVersion int64  `json:"expectedVersion"`
}

type batchOrderActionRequest struct {
	RequestID string           `json:"requestId"`
	Action    string           `json:"action"`
	Items     []batchOrderItem `json:"items"`
}

func orderActionTarget(action, status, paymentStatus string) (string, *apiError) {
	switch action {
	case "ACCEPT", "PICKING":
		if status != "PAID" || paymentStatus != "PAID" {
			return "", conflict("INVALID_STATE", "只有服务端确认已付款的订单才能进入备货")
		}
		return "PICKING", nil
	case "READY":
		if status != "PICKING" {
			return "", conflict("INVALID_STATE", "只有备货中订单才能标记待出餐")
		}
		return "READY", nil
	}
	return "", badRequest("INVALID_ACTION", "不支持的订单命令")
}

type batchProgress struct {
	expected  int
	completed int
	missing   int
}

func (a *App) batchProgress(ctx context.Context, batchID int64) (batchProgress, error) {
	var p batchProgress
	err := a.db.QueryRowContext(ctx, `SELECT b.expected_count,
		(SELECT COUNT(*) FROM batch_operation_items i WHERE i.batch_id=b.id AND i.result IN ('SUCCESS','FAILED','SKIPPED')),
		(SELECT COUNT(*) FROM batch_operation_targets t LEFT JOIN batch_operation_items i
			ON i.batch_id=t.batch_id AND i.order_id=t.order_id WHERE t.batch_id=b.id AND i.order_id IS NULL)
		FROM batch_operations b WHERE b.id=?`, batchID).Scan(&p.expected, &p.completed, &p.missing)
	return p, err
}

func (a *App) adminBatchOrderAction(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req batchOrderActionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	action := strings.ToUpper(strings.TrimSpace(req.Action))
	if !isOneOf(action, "ACCEPT", "READY") {
		writeError(w, r, badRequest("INVALID_ACTION", "批量操作只支持 ACCEPT 或 READY"))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	if header := strings.TrimSpace(r.Header.Get("Idempotency-Key")); header != "" && header != req.RequestID {
		writeError(w, r, conflict("IDEMPOTENCY_KEY_MISMATCH", "请求编号与请求头不一致"))
		return
	}
	if len(req.Items) == 0 || len(req.Items) > batchOrderLimit {
		writeError(w, r, badRequest("INVALID_BATCH_SIZE", "单次批量必须为 1-50 单"))
		return
	}
	byID := map[int64]int64{}
	for _, item := range req.Items {
		id, apiErr := parseID(item.OrderID, "orderId")
		if apiErr != nil {
			writeError(w, r, apiErr.(*apiError))
			return
		}
		if item.ExpectedVersion <= 0 {
			writeError(w, r, badRequest("EXPECTED_VERSION_REQUIRED", "每张订单都必须提供 expectedVersion"))
			return
		}
		if previous, ok := byID[id]; ok && previous != item.ExpectedVersion {
			writeError(w, r, conflict("DUPLICATE_ORDER_VERSION", "同一订单在同一批次中出现了不同版本，请刷新后重试"))
			return
		}
		byID[id] = item.ExpectedVersion
	}
	ids := make([]int64, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	fingerprint, err := hashCanonical(map[string]any{
		"action": action, "storeId": p.Staff.StoreID, "items": idsToVersions(ids, byID),
	})
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "处理批量操作失败"})
		return
	}
	batchID, apiErr := a.beginBatch(r.Context(), p, "ORDER_"+action, req.RequestID, fingerprint, ids, byID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	a.runBatch(w, r, p, batchID, action)
}

// adminResumeBatch continues a batch from its persisted targets. Only the operator
// who created it may resume it, and the staff permission is re-checked by the route.
func (a *App) adminResumeBatch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	batchID, err := parseID(r.PathValue("id"), "批次ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var action string
	var ownerID int64
	if err := a.db.QueryRowContext(r.Context(), `SELECT kind,actor_id FROM batch_operations WHERE id=? AND store_id=?`,
		batchID, p.Staff.StoreID).Scan(&action, &ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, notFound("批次不存在"))
			return
		}
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次失败"})
		return
	}
	if ownerID != p.UserID {
		writeError(w, r, forbidden("只能恢复自己创建的批次"))
		return
	}
	a.runBatch(w, r, p, batchID, strings.TrimPrefix(action, "ORDER_"))
}

// adminFindBatch lets a client that lost the submit response (it only remembers the
// requestId) get the batchId back.
func (a *App) adminFindBatch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	requestID := strings.TrimSpace(r.URL.Query().Get("requestId"))
	if !validRequestID(requestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	var id int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id FROM batch_operations WHERE actor_id=? AND request_id=? AND store_id=? AND kind LIKE 'ORDER_%' ORDER BY id DESC LIMIT 1`,
		p.UserID, requestID, p.Staff.StoreID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("没有找到该 requestId 对应的批次"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"batchId": toStringID(id)})
}

func (a *App) runBatch(w http.ResponseWriter, r *http.Request, p *Principal, batchID int64, action string) {
	var status string
	if err := a.db.QueryRowContext(r.Context(), `SELECT status FROM batch_operations WHERE id=?`, batchID).Scan(&status); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次失败"})
		return
	}
	targets, apiErr := a.loadBatchTargets(r.Context(), batchID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	if status != "DONE" {
		traceID := traceIDFrom(r)
		for _, t := range targets {
			if _, err := a.applyBatchItem(r.Context(), p, batchID, t.orderID, t.expectedVersion, action, traceID); err != nil {
				// Infrastructure uncertainty: stop and report structured progress.
				break
			}
		}
	}

	progress, err := a.batchProgress(r.Context(), batchID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次进度失败"})
		return
	}
	done := progress.missing == 0 && progress.completed == progress.expected && progress.expected > 0
	if done {
		if _, err := a.db.ExecContext(r.Context(), `UPDATE batch_operations SET status='DONE' WHERE id=?`, batchID); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "批次已完成，但状态更新失败，请查询批次"})
			return
		}
	}
	items, apiErr := a.loadBatchItems(r.Context(), batchID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, t := range targets {
		if item, ok := items[t.orderID]; ok {
			out = append(out, item)
		}
	}
	if !done {
		body := map[string]any{
			"code": "BATCH_INCOMPLETE", "message": "批次尚未完成，请用同一 requestId 重试或恢复该批次",
			"traceId": traceIDFrom(r),
			"data": map[string]any{
				"batchId": toStringID(batchID), "status": "RUNNING", "expectedCount": progress.expected,
				"completedCount": progress.completed, "pendingCount": progress.missing, "items": out,
			},
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"batchId": toStringID(batchID), "action": action, "status": "DONE",
		"expectedCount": progress.expected, "completedCount": progress.completed, "pendingCount": 0, "items": out,
	})
}

type batchTarget struct {
	orderID, expectedVersion int64
}

func (a *App) loadBatchTargets(ctx context.Context, batchID int64) ([]batchTarget, *apiError) {
	rows, err := a.db.QueryContext(ctx, `SELECT order_id,expected_version FROM batch_operation_targets WHERE batch_id=? ORDER BY order_id`, batchID)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取批次目标失败"}
	}
	defer rows.Close()
	out := []batchTarget{}
	for rows.Next() {
		var t batchTarget
		if err := rows.Scan(&t.orderID, &t.expectedVersion); err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取批次目标失败"}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取批次目标失败"}
	}
	return out, nil
}

func (a *App) beginBatch(ctx context.Context, p *Principal, kind, requestID, fingerprint string, ids []int64, versions map[int64]int64) (int64, *apiError) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, &apiError{500, "DB_ERROR", "处理批量操作失败"}
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_operations(store_id,actor_id,kind,request_id,payload_hash,expected_count)
		VALUES (?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`,
		p.Staff.StoreID, p.UserID, kind, requestID, fingerprint, len(ids)); err != nil {
		return 0, &apiError{500, "DB_ERROR", "处理批量操作失败"}
	}
	var id int64
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT id,payload_hash FROM batch_operations
		WHERE actor_id=? AND request_id=? FOR UPDATE`, p.UserID, requestID).Scan(&id, &hash); err != nil {
		return 0, &apiError{500, "DB_ERROR", "处理批量操作失败"}
	}
	if hash != fingerprint {
		return 0, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同的批量参数")
	}
	for _, orderID := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO batch_operation_targets(batch_id,order_id,expected_version)
			VALUES (?,?,?) ON DUPLICATE KEY UPDATE order_id=order_id`, id, orderID, versions[orderID]); err != nil {
			return 0, &apiError{500, "DB_ERROR", "处理批量操作失败"}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, &apiError{500, "DB_ERROR", "处理批量操作失败"}
	}
	return id, nil
}

func (a *App) loadBatchItems(ctx context.Context, batchID int64) (map[int64]map[string]any, *apiError) {
	rows, err := a.db.QueryContext(ctx, `SELECT order_id,result,error_code,message,version_after FROM batch_operation_items WHERE batch_id=?`, batchID)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取批量结果失败"}
	}
	defer rows.Close()
	items := map[int64]map[string]any{}
	for rows.Next() {
		var orderID, version int64
		var result, code, message string
		if err := rows.Scan(&orderID, &result, &code, &message, &version); err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取批量结果失败"}
		}
		items[orderID] = map[string]any{"orderId": toStringID(orderID), "result": result, "code": code, "message": message, "version": version}
	}
	if err := rows.Err(); err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取批量结果失败"}
	}
	return items, nil
}

func putBatchItem(ctx context.Context, tx *sql.Tx, batchID, orderID int64, result, code, message string, version int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO batch_operation_items(batch_id,order_id,result,error_code,message,version_after)
		VALUES (?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE order_id=order_id`,
		batchID, orderID, result, code, message, version)
	return err
}

func (a *App) applyBatchItem(ctx context.Context, p *Principal, batchID, orderID, expectedVersion int64, action, traceID string) (string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Serialize this batch before FK/target locks, matching beginBatch lock order.
	var storedActor, storedStore int64
	var storedKind string
	if err := tx.QueryRowContext(ctx, "SELECT actor_id,store_id,kind FROM batch_operations WHERE id=? FOR UPDATE", batchID).Scan(&storedActor, &storedStore, &storedKind); err != nil {
		return "", err
	}
	if storedActor != p.UserID || storedStore != p.Staff.StoreID || storedKind != "ORDER_"+action {
		return "", errors.New("batch ownership mismatch")
	}
	// 1) Lock the target and reuse any terminal result already committed by a
	// concurrent request of the same batch.
	var storedVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT expected_version FROM batch_operation_targets
		WHERE batch_id=? AND order_id=? FOR UPDATE`, batchID, orderID).Scan(&storedVersion); err != nil {
		return "", err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT result FROM batch_operation_items
		WHERE batch_id=? AND order_id=? FOR UPDATE`, batchID, orderID).Scan(&existing)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// 2) No result yet: validate the order under the canonical lock order.
	var status, paymentStatus string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT status,payment_status,version FROM orders WHERE id=? AND store_id=? FOR UPDATE`,
		orderID, p.Staff.StoreID).Scan(&status, &paymentStatus, &version)
	if errors.Is(err, sql.ErrNoRows) {
		if err := putBatchItem(ctx, tx, batchID, orderID, "FAILED", "NOT_FOUND", "订单不存在或不属于当前门店", 0); err != nil {
			return "", err
		}
		return "FAILED", tx.Commit()
	}
	if err != nil {
		return "", err
	}
	if version != storedVersion {
		if err := putBatchItem(ctx, tx, batchID, orderID, "FAILED", "VERSION_CONFLICT", "订单已被其他人更新，请刷新后重试", version); err != nil {
			return "", err
		}
		return "FAILED", tx.Commit()
	}
	target, apiErr := orderActionTarget(action, status, paymentStatus)
	if apiErr != nil {
		if err := putBatchItem(ctx, tx, batchID, orderID, "FAILED", apiErr.code, apiErr.msg, version); err != nil {
			return "", err
		}
		return "FAILED", tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET status=?,version=version+1 WHERE id=?`, target, orderID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json)
		VALUES (?,?,?,?,?)`, orderID, "ORDER_"+target, "STAFF", p.UserID, mustJSON(map[string]any{"batchAction": action})); err != nil {
		return "", err
	}
	if err := auditTx(ctx, tx, p.UserID, p.Staff.StoreID, "BATCH_"+action, "ORDER", toStringID(orderID),
		map[string]any{"status": status}, map[string]any{"status": target}, traceID); err != nil {
		return "", err
	}
	if err := putBatchItem(ctx, tx, batchID, orderID, "SUCCESS", "", "", version+1); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "SUCCESS", nil
}

// adminBatchOrderDetail is read-only: it returns the full expected list, per-item
// state and progress so a client can decide to resume.
func (a *App) adminBatchOrderDetail(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	batchID, err := parseID(r.PathValue("id"), "批次ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var kind, status, requestID string
	var expected int
	err = a.db.QueryRowContext(r.Context(), `SELECT kind,status,request_id,expected_count FROM batch_operations WHERE id=? AND store_id=? AND actor_id=?`,
		batchID, p.Staff.StoreID, p.UserID).Scan(&kind, &status, &requestID, &expected)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("批次不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次失败"})
		return
	}
	targets, apiErr := a.loadBatchTargets(r.Context(), batchID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	items, apiErr := a.loadBatchItems(r.Context(), batchID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	progress, err := a.batchProgress(r.Context(), batchID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取批次进度失败"})
		return
	}
	list := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		entry := map[string]any{"orderId": toStringID(t.orderID), "expectedVersion": t.expectedVersion}
		if item, ok := items[t.orderID]; ok {
			entry["result"] = item["result"]
			entry["code"] = item["code"]
			entry["message"] = item["message"]
		} else {
			entry["result"] = "PENDING"
		}
		list = append(list, entry)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"batchId": toStringID(batchID), "kind": kind, "requestId": requestID, "status": status,
		"expectedCount": progress.expected, "completedCount": progress.completed, "pendingCount": progress.missing,
		"items": list,
	})
}

func idsToVersions(ids []int64, byID map[int64]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"orderId": toStringID(id), "expectedVersion": byID[id]})
	}
	return out
}
