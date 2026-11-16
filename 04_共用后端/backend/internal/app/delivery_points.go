package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type deliveryPoint struct {
	ID           string  `json:"id"`
	BuildingID   string  `json:"buildingId"`
	Name         string  `json:"name"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	RadiusMeters int     `json:"radiusMeters"`
	Active       bool    `json:"active"`
	Version      int64   `json:"version"`
}

func (a *App) registerDeliveryUpgradeRoutes() {
	a.mux.HandleFunc("GET /api/v1/delivery-points", a.listDeliveryPoints)
	a.mux.HandleFunc("GET /api/v1/admin/delivery-points", a.staff("dispatch", a.adminDeliveryPoints))
	a.mux.HandleFunc("POST /api/v1/admin/delivery-points", a.staff("dispatch", a.saveDeliveryPoint))
	a.mux.HandleFunc("PUT /api/v1/admin/delivery-points/{id}", a.staff("dispatch", a.saveDeliveryPoint))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/{id}/arrival", a.staff("courier", a.courierArrival))
	a.mux.HandleFunc("POST /api/v1/courier/arrivals", a.staff("courier", a.courierArrival))
	a.mux.HandleFunc("GET /api/v1/courier/capacity", a.staff("courier", a.courierCapacity))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/batch-actions", a.staff("courier", a.courierBatchAction))
	a.mux.HandleFunc("GET /api/v1/courier/tasks/batch-actions", a.staff("courier", a.courierFindBatch))
	a.mux.HandleFunc("GET /api/v1/courier/tasks/batch-actions/{id}", a.staff("courier", a.courierBatchDetail))
	a.mux.HandleFunc("POST /api/v1/courier/tasks/batch-actions/{id}/resume", a.staff("courier", a.courierResumeBatch))
}

func (a *App) courierCapacity(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	limit, online := 3, true
	err := a.db.QueryRowContext(r.Context(), `SELECT task_limit,online FROM courier_capacities WHERE store_id=? AND courier_id=?`, p.Staff.StoreID, p.UserID).Scan(&limit, &online)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取骑手容量失败"})
		return
	}
	active, err := CountActiveCourierTasks(r.Context(), a.db, p.Staff.StoreID, p.UserID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取骑手任务占用失败"})
		return
	}
	writeJSON(w, 200, traceIDFrom(r), map[string]any{"activeTaskCount": active, "taskLimit": limit, "online": online})
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func (a *App) listDeliveryPoints(w http.ResponseWriter, r *http.Request) {
	a.readDeliveryPoints(w, r, false)
}
func (a *App) adminDeliveryPoints(w http.ResponseWriter, r *http.Request) {
	a.readDeliveryPoints(w, r, true)
}

func (a *App) readDeliveryPoints(w http.ResponseWriter, r *http.Request, admin bool) {
	storeID := a.cfg.StoreID
	if admin {
		storeID = principalFrom(r).Staff.StoreID
	}
	where := " WHERE store_id=?"
	args := []any{storeID}
	if !admin {
		where += " AND active=1"
	}
	if raw := r.URL.Query().Get("buildingId"); raw != "" {
		id, err := parseID(raw, "buildingId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		where += " AND building_id=?"
		args = append(args, id)
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,building_id,name,latitude,longitude,radius_meters,active,version FROM delivery_points`+where+` ORDER BY building_id,id`, args...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取配送点失败"})
		return
	}
	defer rows.Close()
	points := []deliveryPoint{}
	for rows.Next() {
		var point deliveryPoint
		if err := rows.Scan(&point.ID, &point.BuildingID, &point.Name, &point.Latitude, &point.Longitude, &point.RadiusMeters, &point.Active, &point.Version); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取配送点失败"})
			return
		}
		points = append(points, point)
	}
	if rows.Err() != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取配送点失败"})
		return
	}
	writeJSON(w, 200, traceIDFrom(r), points)
}

func (a *App) saveDeliveryPoint(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	// Pointer coordinates distinguish an explicitly configured zero from an omitted value.
	var req struct {
		BuildingID      string   `json:"buildingId"`
		Name            string   `json:"name"`
		Latitude        *float64 `json:"latitude"`
		Longitude       *float64 `json:"longitude"`
		RadiusMeters    int      `json:"radiusMeters"`
		Active          *bool    `json:"active"`
		Version         int64    `json:"version"`
		ExpectedVersion int64    `json:"expectedVersion"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	buildingID, err := parseID(req.BuildingID, "buildingId")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 80 || req.Latitude == nil || req.Longitude == nil || !validCoordinates(*req.Latitude, *req.Longitude) || req.RadiusMeters < 20 || req.RadiusMeters > 300 {
		writeError(w, r, badRequest("INVALID_DELIVERY_POINT", "请填写配送点名称、WGS84 经纬度和 20-300 米范围"))
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	id := int64(0)
	if r.PathValue("id") != "" {
		id, err = parseID(r.PathValue("id"), "配送点ID")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
	}
	version := req.Version
	if version == 0 {
		version = req.ExpectedVersion
	}
	if id > 0 && version < 1 {
		writeError(w, r, badRequest("EXPECTED_VERSION_REQUIRED", "修改配送点必须提供 version"))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存配送点失败"})
		return
	}
	defer tx.Rollback()
	fail := func(err error) {
		if e, ok := err.(*apiError); ok {
			writeError(w, r, e)
		} else {
			a.log.Error("save delivery point", "error", err)
			writeError(w, r, &apiError{500, "DB_ERROR", "保存配送点失败"})
		}
	}
	var enabled bool
	if err = tx.QueryRowContext(r.Context(), `SELECT active FROM buildings WHERE id=? AND store_id=? FOR UPDATE`, buildingID, p.Staff.StoreID).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(notFound("楼栋不属于当前门店"))
		} else {
			fail(err)
		}
		return
	}
	if !enabled && active {
		fail(conflict("BUILDING_INACTIVE", "停用楼栋不能启用配送点"))
		return
	}
	if id == 0 {
		res, e := tx.ExecContext(r.Context(), `INSERT INTO delivery_points(store_id,building_id,name,latitude,longitude,radius_meters,active) VALUES (?,?,?,?,?,?,?)`, p.Staff.StoreID, buildingID, name, *req.Latitude, *req.Longitude, req.RadiusMeters, active)
		if isDuplicateKey(e) {
			fail(conflict("DELIVERY_POINT_EXISTS", "该楼栋已有默认配送点，请编辑现有配送点"))
			return
		}
		if e != nil {
			fail(e)
			return
		}
		id, _ = res.LastInsertId()
		version = 1
	} else {
		var existingBuilding, existingVersion int64
		if err = tx.QueryRowContext(r.Context(), `SELECT building_id,version FROM delivery_points WHERE id=? AND store_id=? FOR UPDATE`, id, p.Staff.StoreID).Scan(&existingBuilding, &existingVersion); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(notFound("配送点不存在"))
			} else {
				fail(err)
			}
			return
		}
		if existingBuilding != buildingID {
			fail(conflict("DELIVERY_POINT_BUILDING_IMMUTABLE", "已有配送点不可移至其他楼栋"))
			return
		}
		if version != existingVersion {
			fail(conflict("VERSION_CONFLICT", "配送点配置已变化，请刷新"))
			return
		}
		_, err = tx.ExecContext(r.Context(), `UPDATE delivery_points SET name=?,latitude=?,longitude=?,radius_meters=?,active=?,version=version+1 WHERE id=?`, name, *req.Latitude, *req.Longitude, req.RadiusMeters, active, id)
		if err != nil {
			fail(err)
			return
		}
		version++
	}
	point := deliveryPoint{toStringID(id), toStringID(buildingID), name, *req.Latitude, *req.Longitude, req.RadiusMeters, active, version}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO audit_logs(actor_id,store_id,action,object_type,object_id,after_json,trace_id) VALUES (?,?,'DELIVERY_POINT_SAVE','DELIVERY_POINT',?,?,?)`, p.UserID, p.Staff.StoreID, point.ID, mustJSON(point), traceIDFrom(r)); err != nil {
		fail(err)
		return
	}
	if err = tx.Commit(); err != nil {
		fail(err)
		return
	}
	writeJSON(w, 200, traceIDFrom(r), point)
}

type pointQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findDeliveryPoint(ctx context.Context, q pointQuerier, storeID int64, buildingID string) (*deliveryPoint, error) {
	if buildingID == "" {
		return nil, nil
	}
	var point deliveryPoint
	err := q.QueryRowContext(ctx, `SELECT id,building_id,name,latitude,longitude,radius_meters,active,version FROM delivery_points WHERE store_id=? AND building_id=? AND active=1`, storeID, buildingID).Scan(&point.ID, &point.BuildingID, &point.Name, &point.Latitude, &point.Longitude, &point.RadiusMeters, &point.Active, &point.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &point, nil
}

func snapshotDeliveryPoint(address map[string]any) *deliveryPoint {
	raw, exists := address["deliveryPoint"]
	if !exists {
		return nil
	}
	var point deliveryPoint
	if json.Unmarshal(mustJSON(raw), &point) != nil || point.ID == "" || point.Version <= 0 || !validCoordinates(point.Latitude, point.Longitude) || point.RadiusMeters < 20 || point.RadiusMeters > 300 {
		return nil
	}
	return &point
}

func (a *App) taskDeliveryPoint(ctx context.Context, address map[string]any, storeID int64) *deliveryPoint {
	if point := snapshotDeliveryPoint(address); point != nil {
		return point
	}
	point, _ := findDeliveryPoint(ctx, a.db, storeID, stringValue(address["buildingId"]))
	return point
}

func setTaskDeliveryPoint(task *DeliveryTask, point *deliveryPoint) {
	if point == nil {
		return
	}
	task.DeliveryPointID = point.ID
	task.DeliveryPointName = point.Name
	task.Latitude = &point.Latitude
	task.Longitude = &point.Longitude
	task.RadiusMeters = &point.RadiusMeters
}

// Caller already owns the order/task locks. Historical orders bind only by the
// original building ID to an explicit merchant configuration, then freeze it.
func (a *App) lockOrderDeliveryPoint(ctx context.Context, tx *sql.Tx, d lockedDelivery) (*deliveryPoint, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT address_json FROM orders WHERE id=? AND store_id=?`, d.orderID, d.storeID).Scan(&raw); err != nil {
		return nil, err
	}
	var address map[string]any
	if err := json.Unmarshal(raw, &address); err != nil {
		return nil, err
	}
	if point := snapshotDeliveryPoint(address); point != nil {
		return point, nil
	}
	point, err := findDeliveryPoint(ctx, tx, d.storeID, stringValue(address["buildingId"]))
	if err != nil {
		return nil, err
	}
	if point == nil {
		return nil, conflict("DELIVERY_POINT_REQUIRED", "该订单尚未配置配送点，请联系商家按楼栋配置")
	}
	address["deliveryPoint"] = point
	if _, err = tx.ExecContext(ctx, `UPDATE orders SET address_json=? WHERE id=?`, mustJSON(address), d.orderID); err != nil {
		return nil, err
	}
	return point, nil
}

type arrivalRequest struct {
	TaskIDs    []string  `json:"taskIds"`
	Latitude   *float64  `json:"latitude"`
	Longitude  *float64  `json:"longitude"`
	Accuracy   float64   `json:"accuracy"`
	ObservedAt time.Time `json:"observedAt"`
}
type arrivalBinding struct {
	TaskID     int64 `json:"taskId"`
	Generation int64 `json:"generation"`
}
type arrivalClaims struct {
	Type      string           `json:"type"`
	StoreID   int64            `json:"storeId"`
	CourierID int64            `json:"courierId"`
	PointHash string           `json:"pointHash"`
	Tasks     []arrivalBinding `json:"tasks"`
	ExpiresAt int64            `json:"expiresAt"`
}

func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const radians = math.Pi / 180
	dlat, dlon := (lat2-lat1)*radians, (lon2-lon1)*radians
	h := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*radians)*math.Cos(lat2*radians)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 6371000 * 2 * math.Asin(math.Sqrt(math.Min(1, h)))
}

func validateArrivalLocation(now time.Time, req arrivalRequest, point *deliveryPoint) *apiError {
	if req.Latitude == nil || req.Longitude == nil || !validCoordinates(*req.Latitude, *req.Longitude) {
		return badRequest("INVALID_LOCATION", "请提供有效的 WGS84 定位")
	}
	if req.ObservedAt.IsZero() || now.Sub(req.ObservedAt) > 60*time.Second || req.ObservedAt.Sub(now) > 15*time.Second {
		return conflict("LOCATION_STALE", "定位已过期，请重新获取当前位置")
	}
	if math.IsNaN(req.Accuracy) || math.IsInf(req.Accuracy, 0) || req.Accuracy <= 0 || req.Accuracy > math.Min(50, float64(point.RadiusMeters)) {
		return conflict("LOCATION_INACCURATE", "定位精度不足，请到室外重新定位")
	}
	if distanceMeters(*req.Latitude, *req.Longitude, point.Latitude, point.Longitude) > float64(point.RadiusMeters) {
		return conflict("OUTSIDE_DELIVERY_POINT", "尚未到达订单指定配送点")
	}
	return nil
}

func deliveryPointHash(point *deliveryPoint) string {
	hash, _ := hashCanonical(point)
	return hash
}

func (a *App) signArrival(claims arrivalClaims) string {
	payload := base64.RawURLEncoding.EncodeToString(mustJSON(claims))
	mac := hmac.New(sha256.New, []byte(a.cfg.JWTSecret))
	mac.Write([]byte("courier-arrival-v1." + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) parseArrival(token string) (*arrivalClaims, *apiError) {
	return a.parseArrivalClaims(token, true)
}

// Recovery may inspect an expired, correctly signed proof to preserve its point
// scope. Only parseArrival (which enforces expiry) authorizes delivery work.
func (a *App) parseArrivalClaims(token string, enforceExpiry bool) (*arrivalClaims, *apiError) {
	if token == "" {
		return nil, conflict("ARRIVAL_REQUIRED", "请先到达配送点并完成定位校验")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 10000 {
		return nil, badRequest("ARRIVAL_INVALID", "到达凭据无效，请重新定位")
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.JWTSecret))
	mac.Write([]byte("courier-arrival-v1." + parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, badRequest("ARRIVAL_INVALID", "到达凭据无效，请重新定位")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, badRequest("ARRIVAL_INVALID", "到达凭据无效")
	}
	var claims arrivalClaims
	if json.Unmarshal(data, &claims) != nil || claims.Type != "courier-arrival-v1" || len(claims.Tasks) == 0 || len(claims.Tasks) > 20 {
		return nil, badRequest("ARRIVAL_INVALID", "到达凭据无效")
	}
	if enforceExpiry && claims.ExpiresAt <= a.now().UTC().Unix() {
		return nil, conflict("ARRIVAL_EXPIRED", "到达凭据已过期，请重新定位；已成功订单保持完成")
	}
	return &claims, nil
}

func (a *App) verifyTaskArrival(ctx context.Context, tx *sql.Tx, p *Principal, d lockedDelivery, token string) *apiError {
	claims, apiErr := a.parseArrival(token)
	if apiErr != nil {
		return apiErr
	}
	if claims.StoreID != d.storeID || claims.CourierID != p.UserID {
		return forbidden("到达凭据不属于当前配送员或门店")
	}
	matched := false
	for _, binding := range claims.Tasks {
		if binding.TaskID == d.id && binding.Generation == d.generation {
			matched = true
			break
		}
	}
	if !matched {
		return conflict("ARRIVAL_TASK_MISMATCH", "到达凭据不包含当前任务或任务已经改派")
	}
	point, err := a.lockOrderDeliveryPoint(ctx, tx, d)
	if err != nil {
		if e, ok := err.(*apiError); ok {
			return e
		}
		return &apiError{500, "DB_ERROR", "读取配送点快照失败"}
	}
	if claims.PointHash != deliveryPointHash(point) {
		return conflict("ARRIVAL_POINT_MISMATCH", "配送点与到达凭据不一致，请重新定位")
	}
	return nil
}

func (a *App) courierArrival(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req arrivalRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	if id := r.PathValue("id"); id != "" {
		req.TaskIDs = []string{id}
	}
	if len(req.TaskIDs) == 0 || len(req.TaskIDs) > 20 {
		writeError(w, r, badRequest("INVALID_BATCH_SIZE", "到达校验必须包含 1-20 个任务"))
		return
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, raw := range req.TaskIDs {
		id, err := parseID(raw, "taskId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "校验到达失败"})
		return
	}
	defer tx.Rollback()
	fail := func(err error) {
		if e, ok := err.(*apiError); ok {
			writeError(w, r, e)
		} else {
			a.log.Error("arrival verification failed", "error", err)
			writeError(w, r, &apiError{500, "DB_ERROR", "校验到达失败"})
		}
	}
	claims := arrivalClaims{Type: "courier-arrival-v1", StoreID: p.Staff.StoreID, CourierID: p.UserID, ExpiresAt: a.now().UTC().Add(2 * time.Minute).Unix()}
	var point *deliveryPoint
	for _, id := range ids {
		d, err := lockDelivery(r.Context(), tx, id, p.Staff.StoreID)
		if err != nil {
			fail(err)
			return
		}
		if d.assignee != p.UserID {
			fail(forbidden("任务已改派或不属于当前配送员"))
			return
		}
		if !d.pickedAt.Valid || d.status != "DELIVERING" || d.orderStatus != "DELIVERING" {
			fail(conflict("INVALID_STATE", "仅配送中的任务可校验到达"))
			return
		}
		current, err := a.lockOrderDeliveryPoint(r.Context(), tx, d)
		if err != nil {
			fail(err)
			return
		}
		if point == nil {
			point = current
			claims.PointHash = deliveryPointHash(point)
		} else if claims.PointHash != deliveryPointHash(current) {
			fail(conflict("MIXED_DELIVERY_POINTS", "批量送达只能选择同一配送点及同一配置快照的任务"))
			return
		}
		claims.Tasks = append(claims.Tasks, arrivalBinding{id, d.generation})
	}
	if err := lockActiveCourier(r.Context(), tx, p.Staff.StoreID, p.UserID); err != nil {
		fail(err)
		return
	}
	if err := validateArrivalLocation(a.now().UTC(), req, point); err != nil {
		fail(err)
		return
	}
	if err := tx.Commit(); err != nil {
		fail(err)
		return
	}
	writeJSON(w, 200, traceIDFrom(r), map[string]any{"arrivalToken": a.signArrival(claims), "expiresAt": time.Unix(claims.ExpiresAt, 0).UTC(), "deliveryPointId": point.ID, "deliveryPointName": point.Name, "taskIds": req.TaskIDs})
}
