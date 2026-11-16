package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type deliveryActionRequest struct {
	RequestID       string `json:"requestId"`
	AssigneeID      string `json:"assigneeId"`
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expectedVersion"`
	ArrivalToken    string `json:"arrivalToken"`
	Checked         bool   `json:"checked"`
	Type            string `json:"type"`
	Note            string `json:"note"`
}

func (a *App) loadDeliveryTaskByOrder(ctx context.Context, orderID int64) (*DeliveryTask, error) {
	return a.queryDeliveryTask(ctx, `WHERE t.order_id=?`, []any{orderID}, false)
}

func (a *App) queryDeliveryTask(ctx context.Context, where string, args []any, redact bool) (*DeliveryTask, error) {
	var task DeliveryTask
	var id, orderID, assigneeID, version, storeID int64
	var generation int
	var addressJSON []byte
	var orderStatus string
	var assignedAt, startedAt, completedAt sql.NullTime
	query := `SELECT t.id,t.order_id,o.order_no,t.status,COALESCE(t.assignee_id,0),COALESCE(u.nickname,''),t.version,
		t.assignment_generation,o.address_json,t.created_at,t.assigned_at,t.started_at,t.completed_at,o.status,t.store_id,
		(SELECT COALESCE(SUM(quantity),0) FROM order_items oi WHERE oi.order_id=o.id)
		FROM delivery_tasks t JOIN orders o ON o.id=t.order_id LEFT JOIN users u ON u.id=t.assignee_id ` + where
	err := a.db.QueryRowContext(ctx, query, args...).Scan(&id, &orderID, &task.OrderNo, &task.Status, &assigneeID, &task.AssigneeName,
		&version, &generation, &addressJSON, &task.CreatedAt, &assignedAt, &startedAt, &completedAt, &orderStatus, &storeID, &task.ItemCount)
	if err != nil {
		return nil, err
	}
	task.ID = toStringID(id)
	task.OrderID = toStringID(orderID)
	if assigneeID > 0 {
		task.AssigneeID = toStringID(assigneeID)
	}
	task.Version = version
	task.AssignmentGeneration = generation
	task.OrderStatus = orderStatus
	if assignedAt.Valid {
		t := assignedAt.Time.UTC()
		task.AssignedAt = &t
	}
	if startedAt.Valid {
		t := startedAt.Time.UTC()
		task.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time.UTC()
		task.CompletedAt = &t
	}
	var address map[string]any
	_ = json.Unmarshal(addressJSON, &address)
	setTaskDeliveryPoint(&task, a.taskDeliveryPoint(ctx, address, storeID))
	task.BuildingName = stringValue(address["buildingName"])
	task.AddressText = strings.TrimSpace(task.BuildingName + " " + stringValue(address["detail"]))
	// Contact details are only exposed inside the actionable window. EXCEPTION is
	// deliberately excluded so a stuck task cannot keep leaking the buyer's phone.
	if !redact && isOneOf(task.Status, "ACCEPTED", "PICKED_UP", "DELIVERING") {
		task.ContactName = stringValue(address["contactName"])
		task.Phone = stringValue(address["phone"])
	}
	task.Items, _ = a.loadOrderItems(ctx, orderID)
	task.Urges = []UrgeRecord{}
	if !redact && !isOneOf(task.Status, "COMPLETED", "CANCELLED") {
		task.Urges = a.loadUrgeRecords(ctx, orderID, 20)
	} else {
		task.ContactName = ""
		task.Phone = ""
		task.AddressText = task.BuildingName
	}
	return &task, nil
}

func (a *App) adminCourierCandidates(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT sm.user_id,u.nickname,COALESCE(cc.online,1),
		(SELECT COUNT(*) FROM delivery_tasks dt WHERE dt.store_id=sm.store_id AND dt.assignee_id=sm.user_id AND dt.status IN (`+activeCourierTaskStatesSQL+`)),COALESCE(cc.task_limit,3)
		FROM staff_memberships sm
		JOIN staff_roles sr ON sr.membership_id=sm.id AND sr.role='courier'
		JOIN users u ON u.id=sm.user_id
		LEFT JOIN courier_capacities cc ON cc.store_id=sm.store_id AND cc.courier_id=sm.user_id
		WHERE sm.store_id=? AND sm.active=1 AND u.status='ACTIVE'
		ORDER BY sm.user_id`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取骑手候选失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var userID int64
		var nickname string
		var online, active, limit int
		if err := rows.Scan(&userID, &nickname, &online, &active, &limit); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": toStringID(userID), "nickname": nickname,
			"online": online == 1, "activeTaskCount": active, "taskLimit": limit,
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) courierAvailable(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT t.id,t.order_id,o.order_no,t.status,t.version,t.assignment_generation,
		o.address_json,t.created_at,o.status,
		(SELECT COALESCE(SUM(quantity),0) FROM order_items oi WHERE oi.order_id=o.id)
		FROM delivery_tasks t JOIN orders o ON o.id=t.order_id
		WHERE t.store_id=? AND t.status='OPEN' AND t.assignee_id IS NULL AND o.status IN ('PAID','PICKING','READY')
		ORDER BY t.created_at,t.id LIMIT 50`, p.Staff.StoreID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取可接任务失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, orderID, version int64
		var generation int
		var orderNo, status, orderStatus string
		var addressJSON []byte
		var createdAt time.Time
		var itemCount int
		if err := rows.Scan(&id, &orderID, &orderNo, &status, &version, &generation, &addressJSON, &createdAt, &orderStatus, &itemCount); err != nil {
			continue
		}
		var address map[string]any
		_ = json.Unmarshal(addressJSON, &address)
		// The unassigned pool intentionally omits phone, room and contact.
		item := map[string]any{
			"id": toStringID(id), "orderId": toStringID(orderID), "orderNo": orderNo, "status": status,
			"version": version, "assignmentGeneration": generation,
			"buildingName": stringValue(address["buildingName"]), "itemCount": itemCount,
			"createdAt": createdAt, "orderStatus": orderStatus,
		}
		if point := a.taskDeliveryPoint(r.Context(), address, p.Staff.StoreID); point != nil {
			item["deliveryPointId"] = point.ID
			item["deliveryPointName"] = point.Name
			item["latitude"] = point.Latitude
			item["longitude"] = point.Longitude
			item["radiusMeters"] = point.RadiusMeters
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) courierTasks(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := `WHERE t.assignee_id=? AND t.store_id=?`
	args := []any{p.UserID, p.Staff.StoreID}
	if status != "" {
		query += " AND t.status=?"
		args = append(args, status)
	}
	query += " ORDER BY t.created_at DESC,t.id DESC LIMIT 100"
	rows, err := a.db.QueryContext(r.Context(), `SELECT t.id FROM delivery_tasks t `+query, args...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取任务失败"})
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
	items := []DeliveryTask{}
	for _, id := range ids {
		task, err := a.queryDeliveryTask(r.Context(), `WHERE t.id=? AND t.assignee_id=?`, []any{id, p.UserID}, false)
		if err == nil {
			items = append(items, *task)
		}
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) courierTask(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "任务ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	task, err := a.queryDeliveryTask(r.Context(), `WHERE t.id=? AND t.store_id=? AND t.assignee_id=?`, []any{id, p.Staff.StoreID, p.UserID}, false)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("任务不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取任务失败"})
		return
	}
	if task.Status == "COMPLETED" || task.AssigneeID != toStringID(p.UserID) {
		task.Urges = []UrgeRecord{}
		task.ContactName = ""
		task.Phone = ""
		task.AddressText = task.BuildingName
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), task)
}

func (a *App) courierClaim(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "claim", "COURIER", false)
}
func (a *App) courierPickup(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "pickup", "COURIER", false)
}
func (a *App) courierStart(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "start", "COURIER", false)
}
func (a *App) courierComplete(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "complete", "COURIER", false)
}
func (a *App) courierException(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "exceptions", "COURIER", false)
}
func (a *App) courierResume(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "resume", "COURIER", false)
}
func (a *App) adminAssignTask(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "assign", "STAFF", true)
}
func (a *App) adminReassignTask(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "reassign", "STAFF", true)
}
func (a *App) adminResumeTask(w http.ResponseWriter, r *http.Request) {
	a.deliveryCommand(w, r, "resume", "STAFF", true)
}

type lockedDelivery struct {
	id, orderID, storeID, assignee, version int64
	generation                              int64
	status, orderStatus, paymentStatus      string
	pickedAt                                sql.NullTime
}

// All commands lock request -> order -> task. State changes, event records,
// capacity and replay response commit together. Authorization precedes replay.
func (a *App) deliveryCommand(w http.ResponseWriter, r *http.Request, action, actor string, admin bool) {
	p := principalFrom(r)
	taskID, err := parseID(r.PathValue("id"), "任务ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req deliveryActionRequest
	if err = decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if !validRequestID(req.RequestID) {
		writeError(w, r, badRequest("INVALID_REQUEST_ID", "配送 requestId 必须是 16-128 位字母、数字、下划线或连字符"))
		return
	}
	if header := strings.TrimSpace(r.Header.Get("Idempotency-Key")); header != "" && header != req.RequestID {
		writeError(w, r, badRequest("IDEMPOTENCY_KEY_MISMATCH", "请求编号不一致"))
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	req.Reason = strings.TrimSpace(req.Reason)
	fingerprint, _ := hashCanonical(map[string]any{"taskId": taskID, "action": action, "request": req})
	scope := "DELIVERY_" + strings.ToUpper(action)
	// Capacity is counted after its row lock: READ COMMITTED avoids using a
	// snapshot created before another delivery command finished while we waited.
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "创建配送事务失败"})
		return
	}
	defer tx.Rollback()
	fail := func(err error) {
		if apiErr, ok := err.(*apiError); ok {
			writeError(w, r, apiErr)
		} else {
			a.log.Error("delivery command failed", "action", action, "taskId", taskID, "error", err)
			writeError(w, r, &apiError{500, "DB_ERROR", "配送操作失败，请用原请求重试"})
		}
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO idempotency_keys(principal_id,command_scope,request_id,request_hash,resource_type)
 VALUES (?,?,?,?,'DELIVERY_TASK') ON DUPLICATE KEY UPDATE id=id`, p.UserID, scope, req.RequestID, fingerprint); err != nil {
		fail(err)
		return
	}
	var keyID int64
	var previousHash string
	var previous []byte
	var statusCode int
	if err = tx.QueryRowContext(r.Context(), `SELECT id,request_hash,response_json,status_code FROM idempotency_keys WHERE principal_id=? AND command_scope=? AND request_id=? FOR UPDATE`, p.UserID, scope, req.RequestID).Scan(&keyID, &previousHash, &previous, &statusCode); err != nil {
		fail(err)
		return
	}
	if previousHash != fingerprint {
		fail(conflict("IDEMPOTENCY_CONFLICT", "同一 requestId 携带了不同配送参数"))
		return
	}
	d, err := lockDelivery(r.Context(), tx, taskID, p.Staff.StoreID)
	if err != nil {
		fail(err)
		return
	}
	if !admin && ((action != "claim" && d.assignee != p.UserID) || (action == "claim" && d.assignee != 0 && d.assignee != p.UserID)) {
		fail(forbidden("任务已改派或不属于当前配送员"))
		return
	}
	if !admin {
		if err = lockActiveCourier(r.Context(), tx, p.Staff.StoreID, p.UserID); err != nil {
			fail(err)
			return
		}
	}
	if statusCode != 0 {
		var saved struct {
			Data    map[string]any `json:"data"`
			Code    string         `json:"code"`
			Message string         `json:"message"`
		}
		if err = json.Unmarshal(previous, &saved); err != nil {
			fail(err)
			return
		}
		if statusCode >= 400 {
			writeJSONError(w, r, statusCode, saved.Code, saved.Message)
		} else {
			writeJSON(w, statusCode, traceIDFrom(r), saved.Data)
		}
		return
	}
	if d.version != req.ExpectedVersion {
		fail(conflict("VERSION_CONFLICT", "配送状态已变化，请刷新"))
		return
	}
	result, commandErr, persistFailure := a.applyDeliveryCommand(r.Context(), tx, p, d, req, action, actor, traceIDFrom(r))
	if commandErr != nil && !persistFailure {
		fail(commandErr)
		return
	}
	statusCode = 200
	saved := map[string]any{"data": result}
	if commandErr != nil {
		statusCode = commandErr.status
		saved = map[string]any{"code": commandErr.code, "message": commandErr.msg}
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE idempotency_keys SET resource_id=?,response_json=?,status_code=? WHERE id=?`, toStringID(taskID), mustJSON(saved), statusCode, keyID); err != nil {
		fail(err)
		return
	}
	if err = tx.Commit(); err != nil {
		fail(err)
		return
	}
	if commandErr != nil {
		writeError(w, r, commandErr)
	} else {
		writeJSON(w, 200, traceIDFrom(r), result)
	}
}

func lockDelivery(ctx context.Context, tx *sql.Tx, taskID, storeID int64) (lockedDelivery, error) {
	d := lockedDelivery{id: taskID, storeID: storeID}
	if err := tx.QueryRowContext(ctx, `SELECT order_id FROM delivery_tasks WHERE id=? AND store_id=?`, taskID, storeID).Scan(&d.orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return d, notFound("任务不存在")
		}
		return d, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT status,payment_status FROM orders WHERE id=? AND store_id=? FOR UPDATE`, d.orderID, storeID).Scan(&d.orderStatus, &d.paymentStatus); err != nil {
		return d, err
	}
	err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(assignee_id,0),version,picked_at,assignment_generation FROM delivery_tasks WHERE id=? AND store_id=? FOR UPDATE`, taskID, storeID).Scan(&d.status, &d.assignee, &d.version, &d.pickedAt, &d.generation)
	return d, err
}

func lockActiveCourier(ctx context.Context, tx *sql.Tx, storeID, userID int64) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=? FOR UPDATE`, userID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return badRequest("INVALID_COURIER", "配送员不存在")
		}
		return err
	}
	if status != "ACTIVE" {
		return badRequest("INVALID_COURIER", "配送员已停用")
	}
	var membershipID int64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT id,active FROM staff_memberships WHERE user_id=? AND store_id=? FOR UPDATE`, userID, storeID).Scan(&membershipID, &active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return badRequest("INVALID_COURIER", "配送员不属于当前门店")
		}
		return err
	}
	if !active {
		return badRequest("INVALID_COURIER", "配送员成员资格已停用")
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM staff_roles WHERE membership_id=? AND role='courier' FOR UPDATE`, membershipID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return badRequest("INVALID_COURIER", "员工没有配送角色")
		}
		return err
	}
	return nil
}

func (a *App) applyDeliveryCommand(ctx context.Context, tx *sql.Tx, p *Principal, d lockedDelivery, req deliveryActionRequest, action, actor, traceID string) (map[string]any, *apiError, bool) {
	dbError := func(err error) (map[string]any, *apiError, bool) {
		a.log.Error("delivery transaction failed", "taskId", d.id, "action", action, "error", err)
		return nil, &apiError{500, "DB_ERROR", "配送操作失败，请用原请求重试"}, false
	}
	reject := func(err *apiError) (map[string]any, *apiError, bool) { return nil, err, false }
	if !isOneOf(d.paymentStatus, "PAID", "PARTIAL_REFUNDED") {
		return reject(conflict("PAYMENT_NOT_CONFIRMED", "订单支付状态不允许履约"))
	}
	target := d.status
	switch action {
	case "claim", "assign", "reassign":
		if !isOneOf(d.orderStatus, "PAID", "PICKING", "READY") {
			return reject(conflict("INVALID_STATE", "订单当前不能指派或领取"))
		}
		targetID := p.UserID
		if action != "claim" {
			var err error
			targetID, err = parseID(req.AssigneeID, "assigneeId")
			if err != nil {
				return reject(err.(*apiError))
			}
			if err = lockActiveCourier(ctx, tx, d.storeID, targetID); err != nil {
				if apiErr, ok := err.(*apiError); ok {
					return reject(apiErr)
				}
				return dbError(err)
			}
		}
		if action == "reassign" {
			// An exception must not erase physical custody: pickup is durable
			// evidence even when the display state has changed to EXCEPTION.
			if d.pickedAt.Valid || !isOneOf(d.status, "OPEN", "ACCEPTED", "EXCEPTION") {
				return reject(conflict("INVALID_STATE", "已取货或配送中任务不能直接改派"))
			}
		} else if d.status != "OPEN" || d.assignee != 0 {
			return reject(conflict("TASK_ALREADY_ASSIGNED", "任务已被指派或领取"))
		}
		ids := []int64{targetID}
		if d.assignee != 0 && d.assignee != targetID {
			ids = append(ids, d.assignee)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if err := claimCapacity(ctx, tx, d.storeID, id, 0); err != nil {
				return dbError(err)
			}
		}
		if d.assignee != targetID {
			if d.assignee != 0 {
				if err := claimCapacity(ctx, tx, d.storeID, d.assignee, -1); err != nil {
					return dbError(err)
				}
			}
			if err := claimCapacity(ctx, tx, d.storeID, targetID, 1); err != nil {
				return reject(conflict("CAPACITY_EXCEEDED", "配送员离线或任务已达上限"))
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET assignee_id=?,status='ACCEPTED',version=version+1,assigned_at=UTC_TIMESTAMP(6),accepted_at=UTC_TIMESTAMP(6),assignment_generation=assignment_generation+1 WHERE id=?`, targetID, d.id); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='ASSIGNED',version=version+1 WHERE id=?`, d.orderID); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id) VALUES (?,?,?,?,?,?,?)`, p.UserID, d.storeID, "DELIVERY_"+strings.ToUpper(action), "DELIVERY_TASK", toStringID(d.id), mustJSON(map[string]any{"assigneeId": toStringID(targetID), "reason": truncate(req.Reason, 200)}), traceID); err != nil {
			return dbError(err)
		}
		target = "ACCEPTED"
	case "pickup":
		if d.status == "EXCEPTION" {
			return reject(conflict("TASK_EXCEPTION", "任务已登记异常，请先恢复配送后再取货"))
		}
		if d.status != "ACCEPTED" || d.orderStatus != "READY" {
			return reject(conflict("ORDER_NOT_READY", "商家备齐且已接受任务后才能取货"))
		}
		if !req.Checked {
			return reject(badRequest("ITEMS_NOT_CHECKED", "请核对商品后确认取货"))
		}
		target = "PICKED_UP"
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='PICKED_UP',version=version+1,picked_at=UTC_TIMESTAMP(6) WHERE id=?`, d.id); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='PICKED_UP',version=version+1 WHERE id=?`, d.orderID); err != nil {
			return dbError(err)
		}
	case "start":
		if d.status == "EXCEPTION" {
			return reject(conflict("TASK_EXCEPTION", "任务已登记异常，请先恢复配送后再出发"))
		}
		if d.status != "PICKED_UP" || d.orderStatus != "READY" {
			return reject(conflict("INVALID_STATE", "仅备齐且已取货的订单可以开始配送"))
		}
		target = "DELIVERING"
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='DELIVERING',version=version+1,started_at=UTC_TIMESTAMP(6) WHERE id=?`, d.id); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET status='DELIVERING',delivery_status='DELIVERING',version=version+1 WHERE id=?`, d.orderID); err != nil {
			return dbError(err)
		}
	case "complete":
		if d.status == "EXCEPTION" {
			return reject(conflict("TASK_EXCEPTION", "任务已登记异常，请先恢复配送后再确认送达"))
		}
		if !d.pickedAt.Valid || d.status != "DELIVERING" || d.orderStatus != "DELIVERING" {
			return reject(conflict("INVALID_STATE", "只有配送中的任务可以确认送达"))
		}
		if apiErr := a.verifyTaskArrival(ctx, tx, p, d, req.ArrivalToken); apiErr != nil {
			return reject(apiErr)
		}
		// Count the completing task before it leaves the active state set. This
		// also initializes missing legacy capacity rows without subtracting twice.
		if err := claimCapacity(ctx, tx, d.storeID, p.UserID, -1); err != nil {
			return dbError(err)
		}
		target = "COMPLETED"
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='COMPLETED',version=version+1,completed_at=UTC_TIMESTAMP(6) WHERE id=?`, d.id); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET status='COMPLETED',delivery_status='DELIVERED',version=version+1 WHERE id=?`, d.orderID); err != nil {
			return dbError(err)
		}
		if err := a.enqueueDeliveryNotification(ctx, tx, d.orderID, d.id); err != nil {
			return dbError(err)
		}
	case "exceptions":
		if req.Note == "" {
			return reject(badRequest("NOTE_REQUIRED", "请填写异常说明"))
		}
		exceptionReason := strings.TrimSpace(req.Type)
		if exceptionReason == "" {
			exceptionReason = strings.TrimSpace(req.Reason)
		}
		if exceptionReason == "" {
			return reject(badRequest("REASON_REQUIRED", "请选择异常原因"))
		}
		if !isOneOf(d.status, "ACCEPTED", "PICKED_UP", "DELIVERING") {
			return reject(conflict("INVALID_STATE", "当前任务不能登记异常"))
		}
		target = "EXCEPTION"
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='EXCEPTION',version=version+1,exception_reason=?,exception_note=?,exception_at=UTC_TIMESTAMP(6) WHERE id=?`, truncate(exceptionReason, 40), truncate(req.Note, 255), d.id); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='EXCEPTION',version=version+1 WHERE id=?`, d.orderID); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('DELIVERY_EXCEPTION',?)`, mustJSON(map[string]any{"orderId": toStringID(d.orderID), "reason": truncate(exceptionReason, 40), "note": truncate(req.Note, 200)})); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id) VALUES (?,?,?,?,?,?,?)`, p.UserID, d.storeID, "DELIVERY_EXCEPTION", "DELIVERY_TASK", toStringID(d.id), mustJSON(map[string]any{"reason": truncate(exceptionReason, 40), "picked": d.pickedAt.Valid}), traceID); err != nil {
			return dbError(err)
		}
	case "resume":
		// Recovery follows physical custody instead of forcing a status: if the goods
		// were picked up they stay with the courier (task -> DELIVERING), otherwise they
		// are still in the store (task -> ACCEPTED, ready for pickup again). It never
		// touches payment, stock or courier capacity, so a retry cannot double count.
		if d.status != "EXCEPTION" {
			return reject(conflict("INVALID_STATE", "只有异常任务可以恢复配送"))
		}
		if req.Note == "" {
			return reject(badRequest("NOTE_REQUIRED", "请填写恢复说明"))
		}
		// Recovery follows the evidence, not a single flag: picked-but-not-started
		// must come back as PICKED_UP (order stays READY) so `start` can run again;
		// picked-and-started returns to DELIVERING; never-picked returns to ACCEPTED.
		switch {
		case d.pickedAt.Valid && d.orderStatus == "DELIVERING":
			target = "DELIVERING"
			if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='DELIVERING',version=version+1,exception_resolved_at=UTC_TIMESTAMP(6),exception_resolved_by=? WHERE id=?`, p.UserID, d.id); err != nil {
				return dbError(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='DELIVERING',version=version+1 WHERE id=?`, d.orderID); err != nil {
				return dbError(err)
			}
		case d.pickedAt.Valid && d.orderStatus == "READY":
			target = "PICKED_UP"
			if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='PICKED_UP',version=version+1,exception_resolved_at=UTC_TIMESTAMP(6),exception_resolved_by=? WHERE id=?`, p.UserID, d.id); err != nil {
				return dbError(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='PICKED_UP',version=version+1 WHERE id=?`, d.orderID); err != nil {
				return dbError(err)
			}
		case !d.pickedAt.Valid && d.orderStatus == "READY":
			target = "ACCEPTED"
			if _, err := tx.ExecContext(ctx, `UPDATE delivery_tasks SET status='ACCEPTED',version=version+1,exception_resolved_at=UTC_TIMESTAMP(6),exception_resolved_by=? WHERE id=?`, p.UserID, d.id); err != nil {
				return dbError(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET delivery_status='ASSIGNED',version=version+1 WHERE id=?`, d.orderID); err != nil {
				return dbError(err)
			}
		default:
			return reject(conflict("INVALID_STATE", "任务与订单状态不匹配，无法恢复配送"))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(topic,payload_json) VALUES ('DELIVERY_RESUMED',?)`, mustJSON(map[string]any{"orderId": toStringID(d.orderID), "resumedTo": target})); err != nil {
			return dbError(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id) VALUES (?,?,?,?,?,?,?)`, p.UserID, d.storeID, "DELIVERY_RESUME", "DELIVERY_TASK", toStringID(d.id), mustJSON(map[string]any{"resumedTo": target, "note": truncate(req.Note, 200)}), traceID); err != nil {
			return dbError(err)
		}
	}
	eventName := deliveryEventName(action)
	if eventName == "" {
		eventName = "DELIVERY_" + strings.ToUpper(action)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,actor_id,payload_json) VALUES (?,?,?,?,?)`, d.orderID, eventName, actor, p.UserID, mustJSON(map[string]any{"taskId": toStringID(d.id), "note": truncate(req.Note, 200)})); err != nil {
		return dbError(err)
	}
	return map[string]any{"ok": true, "taskId": toStringID(d.id), "status": target, "version": d.version + 1}, nil, false
}

// deliveryEventName keeps the public timeline vocabulary singular and stable even
// though the HTTP action names are not identical ("exceptions" -> DELIVERY_EXCEPTION).
func deliveryEventName(action string) string {
	switch action {
	case "claim":
		return "DELIVERY_CLAIM"
	case "assign":
		return "DELIVERY_ASSIGN"
	case "reassign":
		return "DELIVERY_REASSIGN"
	case "pickup":
		return "DELIVERY_PICKUP"
	case "start":
		return "DELIVERY_START"
	case "complete":
		return "DELIVERY_COMPLETE"
	case "exceptions":
		return "DELIVERY_EXCEPTION"
	case "resume":
		return "DELIVERY_RESUME"
	}
	return ""
}

// An exception retains physical/operational custody and therefore capacity.
const activeCourierTaskStatesSQL = "'ACCEPTED','PICKED_UP','DELIVERING','EXCEPTION'"

// CountActiveCourierTasks is shared by live capacity checks and the demo seed.
// Callers that use the count for mutations must hold the capacity row lock and
// use READ COMMITTED; a plain read is suitable for a read-only capacity display.
func CountActiveCourierTasks(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, storeID, courierID int64) (int, error) {
	var active int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_tasks WHERE store_id=? AND assignee_id=? AND status IN (`+activeCourierTaskStatesSQL+`)`, storeID, courierID).Scan(&active)
	return active, err
}

func claimCapacity(ctx context.Context, tx *sql.Tx, storeID, courierID int64, delta int) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO courier_capacities(store_id,courier_id,online,active_task_count,task_limit) VALUES (?,?,1,0,3) ON DUPLICATE KEY UPDATE courier_id=courier_id`, storeID, courierID); err != nil {
		return err
	}
	var limit, online int
	if err := tx.QueryRowContext(ctx, `SELECT task_limit,online FROM courier_capacities WHERE store_id=? AND courier_id=? FOR UPDATE`, storeID, courierID).Scan(&limit, &online); err != nil {
		return err
	}
	// The unique row is the serialization point. Do not lock the scanned tasks:
	// another command can already own its task while waiting for this courier.
	active, err := CountActiveCourierTasks(ctx, tx, storeID, courierID)
	if err != nil {
		return err
	}
	if delta > 0 && online == 0 {
		return errors.New("courier offline")
	}
	next := active + delta
	// Existing overload must be visible and drainable. Only additions are
	// rejected by the upper limit; synchronization and release may remain above it.
	if next < 0 || delta > 0 && next > limit {
		return errors.New("courier capacity exceeded")
	}
	_, err = tx.ExecContext(ctx, `UPDATE courier_capacities SET active_task_count=?,version=version+1 WHERE store_id=? AND courier_id=?`, next, storeID, courierID)
	return err
}
