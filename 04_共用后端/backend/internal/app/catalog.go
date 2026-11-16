package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) listBuildings(w http.ResponseWriter, r *http.Request) {
	storeID, err := parseID(r.PathValue("id"), "storeId")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, name, delivery_fee_cents FROM buildings WHERE store_id=? AND active=1 ORDER BY id`, storeID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取楼栋失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, fee int64
		var name string
		if err := rows.Scan(&id, &name, &fee); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取楼栋失败"})
			return
		}
		items = append(items, map[string]any{"id": toStringID(id), "name": name, "deliveryFeeCents": fee})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) listCategories(w http.ResponseWriter, r *http.Request) {
	storeID, err := parseStoreQuery(r, a.cfg.StoreID)
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT c.id, c.name, c.sort_order, COUNT(p.id)
		FROM categories c LEFT JOIN products p ON p.category_id=c.id AND p.active=1
		WHERE c.store_id=? AND c.active=1 GROUP BY c.id, c.name, c.sort_order ORDER BY c.sort_order, c.id`, storeID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取分类失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var sortOrder int
		var count int64
		if err := rows.Scan(&id, &name, &sortOrder, &count); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取分类失败"})
			return
		}
		items = append(items, map[string]any{"id": toStringID(id), "name": name, "sortOrder": sortOrder, "productCount": count})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func (a *App) listProducts(w http.ResponseWriter, r *http.Request) {
	storeID, err := parseStoreQuery(r, a.cfg.StoreID)
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("categoryId"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	args := []any{storeID}
	where := "p.store_id=? AND p.active=1"
	if category != "" {
		categoryID, err := strconv.ParseInt(category, 10, 64)
		if err != nil || categoryID <= 0 {
			writeError(w, r, badRequest("INVALID_CATEGORY", "分类参数无效"))
			return
		}
		where += " AND p.category_id=?"
		args = append(args, categoryID)
	}
	if search != "" {
		where += " AND (p.name LIKE ? OR p.description LIKE ?)"
		like := "%" + search + "%"
		args = append(args, like, like)
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT p.id, p.category_id, p.name, p.description, p.active, p.version
		FROM products p WHERE `+where+` ORDER BY p.id`, args...)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取商品失败"})
		return
	}
	defer rows.Close()
	products := []Product{}
	for rows.Next() {
		var p Product
		var id, categoryID int64
		var active bool
		if err := rows.Scan(&id, &categoryID, &p.Name, &p.Description, &active, &p.Version); err != nil {
			writeError(w, r, &apiError{500, "DB_ERROR", "读取商品失败"})
			return
		}
		p.ID = toStringID(id)
		p.CategoryID = toStringID(categoryID)
		p.Active = active
		p.Skus = a.loadSKUs(r, id, false)
		products = append(products, p)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), products)
}

func (a *App) getProduct(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"), "商品ID")
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	var p Product
	var categoryID int64
	var active bool
	err = a.db.QueryRowContext(r.Context(), `SELECT id, category_id, name, description, active, version FROM products WHERE id=? AND store_id=?`,
		id, a.cfg.StoreID).Scan(&id, &categoryID, &p.Name, &p.Description, &active, &p.Version)
	if err == sql.ErrNoRows {
		writeError(w, r, notFound("商品不存在"))
		return
	}
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取商品失败"})
		return
	}
	p.ID = toStringID(id)
	p.CategoryID = toStringID(categoryID)
	p.Active = active
	p.Skus = a.loadSKUs(r, id, false)
	writeJSON(w, http.StatusOK, traceIDFrom(r), p)
}

// loadSKUs returns current availability but never treats it as a reservation.
func (a *App) loadSKUs(r *http.Request, productID int64, includeInactive bool) []SKU {
	query := `SELECT s.id, s.spec_name, s.price_cents, s.active, s.low_stock_threshold,
		COALESCE(SUM(CASE WHEN l.expiry_date >= CURDATE() THEN GREATEST(l.qty_on_hand-l.qty_reserved,0) ELSE 0 END),0),
		MIN(CASE WHEN l.qty_on_hand > l.qty_reserved AND l.expiry_date >= CURDATE() THEN l.expiry_date END)
		FROM skus s LEFT JOIN inventory_lots l ON l.sku_id=s.id AND l.store_id=s.store_id
		WHERE s.product_id=?`
	if !includeInactive {
		query += ` AND s.active=1`
	}
	query += ` GROUP BY s.id, s.spec_name, s.price_cents, s.active, s.low_stock_threshold ORDER BY s.id`
	rows, err := a.db.QueryContext(r.Context(), query, productID)
	if err != nil {
		return []SKU{}
	}
	defer rows.Close()
	items := []SKU{}
	for rows.Next() {
		var s SKU
		var id int64
		var active bool
		var expiry sql.NullTime
		if err := rows.Scan(&id, &s.SpecName, &s.PriceCents, &active, &s.LowStockThreshold, &s.Available, &expiry); err != nil {
			continue
		}
		s.ID = toStringID(id)
		s.Active = active
		if expiry.Valid {
			s.ExpiryDate = expiry.Time.Format("2006-01-02")
		}
		items = append(items, s)
	}
	return items
}

func (a *App) listScenes(w http.ResponseWriter, r *http.Request) {
	// Scenes are deliberately small, editable content, not a recommendation
	// algorithm. Product IDs are resolved at read time so stale IDs disappear.
	scenes := []map[string]any{
		{"id": "late-night", "name": "晚自习补给", "description": "少量咖啡因、咸口与分享装"},
		{"id": "dorm-share", "name": "宿舍分享", "description": "适合 2-4 人一起分"},
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), scenes)
}

func (a *App) frequentItems(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT oi.sku_id,p.name,s.spec_name,s.price_cents,
		COALESCE(SUM(GREATEST(l.qty_on_hand-l.qty_reserved,0)),0),MAX(o.created_at)
		FROM order_items oi JOIN orders o ON o.id=oi.order_id JOIN skus s ON s.id=oi.sku_id JOIN products p ON p.id=s.product_id
		LEFT JOIN inventory_lots l ON l.sku_id=s.id AND l.store_id=s.store_id AND l.expiry_date>=CURDATE()
		WHERE o.buyer_id=? AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED') AND s.active=1 AND p.active=1
		GROUP BY oi.sku_id,p.name,s.spec_name,s.price_cents ORDER BY MAX(o.created_at) DESC LIMIT 8`, p.UserID)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取常买失败"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var skuID, price, available int64
		var name, spec string
		var last time.Time
		if err := rows.Scan(&skuID, &name, &spec, &price, &available, &last); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"skuId": toStringID(skuID), "productName": name, "specName": spec,
			"priceCents": price, "available": available, "lastOrderedAt": last,
		})
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), items)
}

func parseStoreQuery(r *http.Request, fallback int64) (int64, error) {
	value := strings.TrimSpace(r.URL.Query().Get("storeId"))
	if value == "" {
		return fallback, nil
	}
	return parseID(value, "storeId")
}

func readJSONMap(raw []byte) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}
