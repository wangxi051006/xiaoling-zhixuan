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

type courierBatchItem struct {
	TaskID          string `json:"taskId"`
	ExpectedVersion int64  `json:"expectedVersion"`
}
type courierBatchRequest struct {
	RequestID    string             `json:"requestId"`
	Action       string             `json:"action"`
	Items        []courierBatchItem `json:"items"`
	Checked      bool               `json:"checked"`
	ArrivalToken string             `json:"arrivalToken"`
}

func normalizeCourierBatch(req *courierBatchRequest) *apiError {
	req.Action = strings.ToUpper(strings.TrimSpace(req.Action))
	if !validRequestID(req.RequestID) {
		return badRequest("INVALID_REQUEST_ID", "requestId 必须是 16-128 位字母、数字、下划线或连字符")
	}
	if !isOneOf(req.Action, "CLAIM", "PICKUP", "COMPLETE") {
		return badRequest("INVALID_ACTION", "骑手批量操作只支持 CLAIM、PICKUP、COMPLETE")
	}
	if len(req.Items) == 0 || len(req.Items) > 20 {
		return badRequest("INVALID_BATCH_SIZE", "骑手每批必须选择 1-20 个任务")
	}
	seen := map[int64]bool{}
	for i := range req.Items {
		id, err := parseID(req.Items[i].TaskID, "taskId")
		if err != nil {
			return err.(*apiError)
		}
		if seen[id] {
			return badRequest("DUPLICATE_TASK", "同一批次不能重复选择任务")
		}
		seen[id] = true
		if req.Items[i].ExpectedVersion < 1 {
			return badRequest("EXPECTED_VERSION_REQUIRED", "每个任务必须提供 expectedVersion")
		}
		req.Items[i].TaskID = toStringID(id)
	}
	sort.Slice(req.Items, func(i, j int) bool {
		a, _ := parseID(req.Items[i].TaskID, "")
		b, _ := parseID(req.Items[j].TaskID, "")
		return a < b
	})
	return nil
}

func (a *App) courierBatchAction(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req courierBatchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if err := normalizeCourierBatch(&req); err != nil {
		writeError(w, r, err)
		return
	}
	if header := strings.TrimSpace(r.Header.Get("Idempotency-Key")); header != "" && header != req.RequestID {
		writeError(w, r, badRequest("IDEMPOTENCY_KEY_MISMATCH", "请求编号不一致"))
		return
	}
	fingerprint, _ := hashCanonical(req)
	id, err := a.beginCourierBatch(r.Context(), p, req, fingerprint)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.runCourierBatch(w, r, p, id)
}

func (a *App) beginCourierBatch(ctx context.Context, p *Principal, req courierBatchRequest, hash string) (int64, *apiError) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, &apiError{500, "DB_ERROR", "创建骑手批次失败"}
	}
	defer tx.Rollback()
	fail := func(err error) (int64, *apiError) {
		a.log.Error("create courier batch", "error", err)
		return 0, &apiError{500, "DB_ERROR", "创建骑手批次失败"}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO courier_batches(store_id,courier_id,request_id,action,request_hash,checked,arrival_token,expected_count) VALUES (?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, p.Staff.StoreID, p.UserID, req.RequestID, req.Action, hash, req.Checked, req.ArrivalToken, len(req.Items))
	if err != nil {
		return fail(err)
	}
	var id int64
	var previous string
	var expected int
	if err = tx.QueryRowContext(ctx, `SELECT id,request_hash,expected_count FROM courier_batches WHERE store_id=? AND courier_id=? AND request_id=? FOR UPDATE`, p.Staff.StoreID, p.UserID, req.RequestID).Scan(&id, &previous, &expected); err != nil {
		return fail(err)
	}
	if previous != hash {
		return 0, conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同骑手批量参数")
	}
	if expected != len(req.Items) {
		return 0, conflict("BATCH_TARGET_MISMATCH", "批次目标不一致")
	}
	for _, item := range req.Items {
		_, err = tx.ExecContext(ctx, `INSERT INTO courier_batch_items(batch_id,task_id,expected_version) VALUES (?,?,?) ON DUPLICATE KEY UPDATE batch_id=batch_id`, id, item.TaskID, item.ExpectedVersion)
		if err != nil {
			return fail(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return id, nil
}

func (a *App) courierFindBatch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	requestID := strings.TrimSpace(r.URL.Query().Get("requestId"))
	if !validRequestID(requestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "请提供原批次 requestId"))
		return
	}
	var id int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id FROM courier_batches WHERE store_id=? AND courier_id=? AND request_id=?`, p.Staff.StoreID, p.UserID, requestID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("批次不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取骑手批次失败"})
		return
	}
	data, apiErr := a.courierBatchSnapshot(r.Context(), p, id)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, 200, traceIDFrom(r), data)
}

func (a *App) courierBatchDetail(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"), "批次ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	data, apiErr := a.courierBatchSnapshot(r.Context(), principalFrom(r), id)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, 200, traceIDFrom(r), data)
}

func (a *App) courierResumeBatch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "批次ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req struct {
		ArrivalToken string `json:"arrivalToken"`
	}
	if r.ContentLength != 0 {
		if err = decodeJSON(w, r, &req); err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
	}
	// The request fingerprint remains immutable. A fresh arrival proof is an
	// explicit recovery input, and only affects still-pending completion items.
	if req.ArrivalToken != "" {
		if apiErr := a.refreshCourierBatchArrival(r.Context(), p, id, req.ArrivalToken); apiErr != nil {
			writeError(w, r, apiErr)
			return
		}
	}
	a.runCourierBatch(w, r, p, id)
}

func (a *App) refreshCourierBatchArrival(ctx context.Context, p *Principal, id int64, token string) *apiError {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return &apiError{500, "DB_ERROR", "更新批次定位失败"}
	}
	defer tx.Rollback()
	var action, status, previousToken string
	if err = tx.QueryRowContext(ctx, `SELECT action,status,arrival_token FROM courier_batches WHERE id=? AND store_id=? AND courier_id=? FOR UPDATE`, id, p.Staff.StoreID, p.UserID).Scan(&action, &status, &previousToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("批次不存在或不属于当前骑手")
		}
		return &apiError{500, "DB_ERROR", "读取骑手批次失败"}
	}
	// Terminal replay does not need a live proof. runCourierBatch still checks
	// current assignment authority before returning successful item responses.
	if status == "DONE" {
		return nil
	}
	if action != "COMPLETE" {
		return badRequest("INVALID_ACTION", "只有送达批次需要更新到达凭据")
	}
	claims, apiErr := a.parseArrival(token)
	if apiErr != nil {
		return apiErr
	}
	if claims.CourierID != p.UserID || claims.StoreID != p.Staff.StoreID {
		return forbidden("到达凭据不属于当前配送员或门店")
	}
	if previous, previousErr := a.parseArrivalClaims(previousToken, false); previousErr == nil && previous.CourierID == p.UserID && previous.StoreID == p.Staff.StoreID && previous.PointHash != claims.PointHash {
		return conflict("ARRIVAL_POINT_MISMATCH", "恢复批次必须在原配送点重新定位，不能将不同配送点合并送达")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE courier_batches SET arrival_token=? WHERE id=?`, token, id); err != nil {
		return &apiError{500, "DB_ERROR", "保存新到达凭据失败"}
	}
	if err = tx.Commit(); err != nil {
		return &apiError{500, "DB_ERROR", "保存新到达凭据失败"}
	}
	return nil
}

func (a *App) courierBatchSnapshot(ctx context.Context, p *Principal, id int64) (map[string]any, *apiError) {
	var action, status, requestID string
	var expected int
	err := a.db.QueryRowContext(ctx, `SELECT action,status,request_id,expected_count FROM courier_batches WHERE id=? AND store_id=? AND courier_id=?`, id, p.Staff.StoreID, p.UserID).Scan(&action, &status, &requestID, &expected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("批次不存在或不属于当前骑手")
	}
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取骑手批次失败"}
	}
	rows, err := a.db.QueryContext(ctx, `SELECT task_id,expected_version,result,error_code,message,version_after FROM courier_batch_items WHERE batch_id=? ORDER BY task_id`, id)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取骑手批次结果失败"}
	}
	defer rows.Close()
	items := []map[string]any{}
	completed := 0
	success := 0
	failed := 0
	for rows.Next() {
		var taskID, version, expectedVersion int64
		var result, code, message string
		if err := rows.Scan(&taskID, &expectedVersion, &result, &code, &message, &version); err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取骑手批次结果失败"}
		}
		items = append(items, map[string]any{"taskId": toStringID(taskID), "expectedVersion": expectedVersion, "result": result, "code": code, "message": message, "version": version})
		if result != "PENDING" {
			completed++
		}
		if result == "SUCCESS" {
			success++
		}
		if result == "FAILED" {
			failed++
		}
	}
	if rows.Err() != nil {
		return nil, &apiError{500, "DB_ERROR", "读取骑手批次结果失败"}
	}
	if len(items) != expected {
		return nil, conflict("BATCH_TARGET_MISMATCH", "批次目标数量不一致，请联系管理员")
	}
	return map[string]any{"batchId": toStringID(id), "requestId": requestID, "action": action, "status": status, "expectedCount": expected, "completedCount": completed, "pendingCount": expected - completed, "successCount": success, "failedCount": failed, "items": items}, nil
}

func (a *App) runCourierBatch(w http.ResponseWriter, r *http.Request, p *Principal, id int64) {
	data, apiErr := a.courierBatchSnapshot(r.Context(), p, id)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	var runErr *apiError
	for _, item := range data["items"].([]map[string]any) {
		taskID, _ := parseID(item["taskId"].(string), "taskId")
		if apiErr := a.applyCourierBatchItem(r.Context(), p, id, taskID, traceIDFrom(r)); apiErr != nil {
			runErr = apiErr
			break
		}
	}
	data, apiErr = a.courierBatchSnapshot(r.Context(), p, id)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	if runErr == nil && data["pendingCount"].(int) > 0 {
		runErr = &apiError{503, "BATCH_INCOMPLETE", "批次尚有未完成任务，请查询或恢复原批次"}
		for _, item := range data["items"].([]map[string]any) {
			if item["result"] == "PENDING" && strings.HasPrefix(item["code"].(string), "ARRIVAL_") {
				runErr = &apiError{409, item["code"].(string), item["message"].(string)}
				break
			}
		}
	}
	if runErr != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(runErr.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": runErr.code, "message": runErr.msg, "traceId": traceIDFrom(r), "data": data})
		return
	}
	writeJSON(w, 200, traceIDFrom(r), data)
}

// Locks batch -> item -> order -> task -> courier, and commits result and state
// together. A failed item's savepoint also rolls back any partially applied work.
func (a *App) applyCourierBatchItem(ctx context.Context, p *Principal, batchID, taskID int64, traceID string) *apiError {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return &apiError{500, "DB_ERROR", "创建骑手批次事务失败"}
	}
	defer tx.Rollback()
	dbFail := func(err error) *apiError {
		a.log.Error("courier batch item failed", "batchId", batchID, "taskId", taskID, "error", err)
		return &apiError{500, "DB_ERROR", "批次执行中断，请查询并恢复原批次"}
	}
	var owner, storeID int64
	var action, arrivalToken string
	var checked bool
	if err = tx.QueryRowContext(ctx, `SELECT courier_id,store_id,action,checked,arrival_token FROM courier_batches WHERE id=? FOR UPDATE`, batchID).Scan(&owner, &storeID, &action, &checked, &arrivalToken); err != nil {
		return dbFail(err)
	}
	if owner != p.UserID || storeID != p.Staff.StoreID {
		return forbidden("批次不属于当前骑手或门店")
	}
	var expectedVersion int64
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT expected_version,result FROM courier_batch_items WHERE batch_id=? AND task_id=? FOR UPDATE`, batchID, taskID).Scan(&expectedVersion, &previous); err != nil {
		return dbFail(err)
	}
	if previous == "FAILED" {
		return nil
	}
	d, lockErr := lockDelivery(ctx, tx, taskID, storeID)
	var itemErr *apiError
	if lockErr != nil {
		if e, ok := lockErr.(*apiError); ok {
			itemErr = e
		} else {
			return dbFail(lockErr)
		}
	}
	if itemErr == nil && ((action != "CLAIM" && d.assignee != p.UserID) || (action == "CLAIM" && d.assignee != 0 && d.assignee != p.UserID)) {
		itemErr = forbidden("任务已改派或不属于当前配送员")
	}
	if itemErr == nil {
		if e := lockActiveCourier(ctx, tx, storeID, p.UserID); e != nil {
			if apiErr, ok := e.(*apiError); ok {
				itemErr = apiErr
			} else {
				return dbFail(e)
			}
		}
	}
	// Authorization precedes replay; expiry and version checks follow it.
	if previous == "SUCCESS" {
		return itemErr
	}
	if itemErr == nil && d.version != expectedVersion {
		itemErr = conflict("VERSION_CONFLICT", "任务状态已变化，请刷新后重新选择")
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT courier_item_action`); err != nil {
		return dbFail(err)
	}
	version := int64(0)
	if itemErr == nil {
		result, commandErr, _ := a.applyDeliveryCommand(ctx, tx, p, d, deliveryActionRequest{ExpectedVersion: expectedVersion, Checked: checked, ArrivalToken: arrivalToken}, strings.ToLower(action), "COURIER", traceID)
		itemErr = commandErr
		if itemErr == nil {
			version = result["version"].(int64)
		}
	}
	result, code, message := "SUCCESS", "", "操作成功"
	if itemErr != nil {
		if itemErr.status >= 500 {
			return itemErr
		}
		if _, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT courier_item_action`); err != nil {
			return dbFail(err)
		}
		result, code, message = "FAILED", itemErr.code, itemErr.msg
		if action == "COMPLETE" && strings.HasPrefix(code, "ARRIVAL_") {
			result = "PENDING"
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE courier_batch_items SET result=?,error_code=?,message=?,version_after=? WHERE batch_id=? AND task_id=?`, result, code, message, version, batchID, taskID); err != nil {
		return dbFail(err)
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM courier_batch_items WHERE batch_id=? AND result='PENDING'`, batchID).Scan(&pending); err != nil {
		return dbFail(err)
	}
	if pending == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE courier_batches SET status='DONE' WHERE id=?`, batchID); err != nil {
			return dbFail(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return dbFail(err)
	}
	return nil
}
