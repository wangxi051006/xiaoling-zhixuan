package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

var phonePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

type addressRequest struct {
	BuildingID     string `json:"buildingId"`
	ContactName    string `json:"contactName"`
	Phone          string `json:"phone"`
	Detail         string `json:"detail"`
	ExpectedVersion int64 `json:"expectedVersion"`
}

type addressView struct {
	ID           string `json:"id"`
	BuildingID   string `json:"buildingId"`
	BuildingName string `json:"buildingName"`
	ContactName  string `json:"contactName"`
	Phone        string `json:"phone"`
	Detail       string `json:"detail"`
	IsDefault    bool   `json:"isDefault"`
	Version      int64  `json:"version"`
	DeliveryFeeCents int64 `json:"deliveryFeeCents"`
}

func (a *App) listAddresses(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT a.id, a.building_id, COALESCE(b.name,a.building_name_snapshot), a.contact_name,
		a.phone, a.detail, a.version, b.delivery_fee_cents,
		CASE WHEN u.default_address_id=a.id THEN 1 ELSE 0 END
		FROM addresses a JOIN users u ON u.id=a.owner_id LEFT JOIN buildings b ON b.id=a.building_id
		WHERE a.owner_id=? AND a.deleted_at IS NULL ORDER BY (u.default_address_id=a.id) DESC, a.id DESC`, p.UserID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取地址失败"})
		return
	}
	defer rows.Close()
	items := []addressView{}
	for rows.Next() {
		var v addressView
		var id, buildingID, version, fee int64
		var isDefault int
		if err := rows.Scan(&id, &buildingID, &v.BuildingName, &v.ContactName, &v.Phone, &v.Detail, &version, &fee, &isDefault); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取地址失败"})
			return
		}
		v.ID = toStringID(id)
		v.BuildingID = toStringID(buildingID)
		v.Version = version
		v.DeliveryFeeCents = fee
		v.IsDefault = isDefault == 1
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) createAddress(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req addressRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	v, apiErr := a.validateAddress(r.Context(), p.UserID, req, 0)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	var id int64
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存地址失败"})
		return
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=? FOR UPDATE`, p.UserID).Scan(&p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存地址失败"})
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO addresses
		(owner_id,store_id,building_id,building_name_snapshot,contact_name,phone,detail)
		VALUES (?,?,?,?,?,?,?)`, p.UserID, a.cfg.StoreID, v.BuildingID, v.BuildingName, v.ContactName, v.Phone, v.Detail)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存地址失败"})
		return
	}
	id, _ = result.LastInsertId()
	var hasDefault sql.NullInt64
	_ = tx.QueryRowContext(r.Context(), `SELECT default_address_id FROM users WHERE id=?`, p.UserID).Scan(&hasDefault)
	if !hasDefault.Valid {
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET default_address_id=? WHERE id=?`, id, p.UserID); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "保存地址失败"})
			return
		}
		v.IsDefault = true
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "保存地址失败"})
		return
	}
	v.ID = toStringID(id)
	v.Version = 1
	writeJSON(w, http.StatusCreated, traceIDFrom(r), v)
}

func (a *App) updateAddress(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "地址ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var req addressRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	v, apiErr := a.validateAddress(r.Context(), p.UserID, req, id)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	expected := req.ExpectedVersion
	if expected <= 0 {
		writeError(w, r, badRequest("EXPECTED_VERSION_REQUIRED", "更新地址必须提供 expectedVersion"))
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE addresses SET building_id=?,building_name_snapshot=?,contact_name=?,phone=?,detail=?,version=version+1
		WHERE id=? AND owner_id=? AND version=? AND deleted_at IS NULL`,
		v.BuildingID, v.BuildingName, v.ContactName, v.Phone, v.Detail, id, p.UserID, expected)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "更新地址失败"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		writeError(w, r, conflict("VERSION_CONFLICT", "地址已被修改，请刷新后重试"))
		return
	}
	v.ID = toStringID(id)
	v.Version = expected + 1
	writeJSON(w, http.StatusOK, traceIDFrom(r), v)
}

func (a *App) deleteAddress(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "地址ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "删除地址失败"})
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `UPDATE addresses SET deleted_at=NOW(6),version=version+1 WHERE id=? AND owner_id=? AND deleted_at IS NULL`, id, p.UserID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "删除地址失败"})
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, r, notFound("地址不存在"))
		return
	}
	_, _ = tx.ExecContext(r.Context(), `UPDATE users SET default_address_id=NULL WHERE id=? AND default_address_id=?`, p.UserID, id)
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "删除地址失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) makeDefaultAddress(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id, err := parseID(r.PathValue("id"), "地址ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "设置默认地址失败"})
		return
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM addresses WHERE id=? AND owner_id=? AND deleted_at IS NULL`, id, p.UserID).Scan(&exists); err != nil || exists != 1 {
		writeError(w, r, notFound("地址不存在"))
		return
	}
	if err := tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=? FOR UPDATE`, p.UserID).Scan(&p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "设置默认地址失败"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET default_address_id=? WHERE id=?`, id, p.UserID); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "设置默认地址失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "设置默认地址失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) validateAddress(ctx context.Context, ownerID int64, req addressRequest, addressID int64) (*addressView, *apiError) {
	buildingID, err := parseID(req.BuildingID, "楼栋")
	if err != nil {
		return nil, err.(*apiError)
	}
	contact := strings.TrimSpace(req.ContactName)
	phone := strings.TrimSpace(req.Phone)
	detail := strings.TrimSpace(req.Detail)
	if contact == "" || len([]rune(contact)) > 40 {
		return nil, badRequest("INVALID_CONTACT", "联系人不能为空且不超过 40 字")
	}
	if !phonePattern.MatchString(phone) {
		return nil, badRequest("INVALID_PHONE", "请输入 11 位手机号")
	}
	if detail == "" || len([]rune(detail)) > 200 {
		return nil, badRequest("INVALID_DETAIL", "门牌/交付点不能为空且不超过 200 字")
	}
	var v addressView
	var active bool
	err = a.db.QueryRowContext(ctx, `SELECT id,name,delivery_fee_cents,active FROM buildings WHERE id=? AND store_id=?`, buildingID, a.cfg.StoreID).
		Scan(&buildingID, &v.BuildingName, &v.DeliveryFeeCents, &active)
	if errors.Is(err, sql.ErrNoRows) || !active {
		return nil, badRequest("INVALID_BUILDING", "该楼栋不在服务范围内")
	}
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取楼栋失败"}
	}
	v.BuildingID = toStringID(buildingID)
	v.ContactName = contact
	v.Phone = phone
	v.Detail = detail
	if addressID > 0 {
		var exists int
		_ = a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM addresses WHERE id=? AND owner_id=? AND deleted_at IS NULL`, addressID, ownerID).Scan(&exists)
		if exists != 1 {
			return nil, notFound("地址不存在")
		}
	}
	return &v, nil
}

// addressSnapshot is copied into an order inside the checkout transaction. A
// later address edit or deletion must not change a historical order.
func (a *App) addressSnapshot(ctx context.Context, ownerID, addressID int64) (map[string]any, int64, error) {
	var id, buildingID, fee int64
	var buildingName, contact, phone, detail string
	err := a.db.QueryRowContext(ctx, `SELECT a.id,a.building_id,a.building_name_snapshot,a.contact_name,a.phone,a.detail,b.delivery_fee_cents
		FROM addresses a LEFT JOIN buildings b ON b.id=a.building_id
		WHERE a.id=? AND a.owner_id=? AND a.deleted_at IS NULL`, addressID, ownerID).
		Scan(&id, &buildingID, &buildingName, &contact, &phone, &detail, &fee)
	if err != nil {
		return nil, 0, err
	}
	return map[string]any{
		"id": toStringID(id), "buildingId": toStringID(buildingID), "buildingName": buildingName,
		"contactName": contact, "phone": phone, "detail": detail,
	}, fee, nil
}
