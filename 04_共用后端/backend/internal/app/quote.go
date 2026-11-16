package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type quoteItem struct {
	SkuID       int64
	ProductID   int64
	ProductName string
	SpecName    string
	UnitPrice   int64
	Quantity    int
	GoodsCents  int64
	Available   int64
	Active      bool
	ExpiryDate  string
}

func normalizedItemIDs(items []ItemInput) ([]ItemInput, error) {
	normalized, err := normalizeItems(items)
	if err != nil {
		return nil, err
	}
	sort.Slice(normalized, func(i, j int) bool {
		left, _ := strconv.ParseInt(normalized[i].SkuID, 10, 64)
		right, _ := strconv.ParseInt(normalized[j].SkuID, 10, 64)
		return left < right
	})
	return normalized, nil
}

func (a *App) createQuote(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req QuoteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	storeID := a.cfg.StoreID
	if strings.TrimSpace(req.StoreID) != "" {
		parsed, err := parseID(req.StoreID, "storeId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
		storeID = parsed
	}
	if storeID != a.cfg.StoreID {
		writeError(w, r, forbidden("当前门店不可用"))
		return
	}
	items, err := normalizedItemIDs(req.Items)
	if err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	fulfillment := strings.ToUpper(strings.TrimSpace(req.FulfillmentType))
	if !isOneOf(fulfillment, "DELIVERY", "PICKUP") {
		writeError(w, r, badRequest("INVALID_FULFILLMENT", "履约方式必须是 DELIVERY 或 PICKUP"))
		return
	}
	var addressID int64
	if fulfillment == "DELIVERY" {
		if strings.TrimSpace(req.AddressID) == "" {
			writeError(w, r, badRequest("ADDRESS_REQUIRED", "配送订单必须选择本人地址"))
			return
		}
		addressID, err = parseID(req.AddressID, "addressId")
		if err != nil {
			writeError(w, r, err.(*apiError))
			return
		}
	}
	quote, qerr := a.buildQuote(r.Context(), a.db, storeID, p.UserID, items, addressID, fulfillment)
	if qerr != nil {
		writeError(w, r, qerr)
		return
	}
	hash, _ := hashCanonical(map[string]any{
		"storeId": storeID, "addressId": addressID, "fulfillmentType": fulfillment, "items": items,
	})
	payload, _ := json.Marshal(quote)
	quote.QuoteID = newToken(16)
	quote.ExpiresAt = a.now().UTC().Add(10 * time.Minute)
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO quotes
		(id,user_id,store_id,request_hash,payload_json,total_cents,expires_at) VALUES (?,?,?,?,?,?,?)`,
		quote.QuoteID, p.UserID, storeID, hash, payload, quote.TotalCents, quote.ExpiresAt); err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "生成报价失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), quote)
}

func (a *App) buildQuote(ctx context.Context, q queryer, storeID, ownerID int64, items []ItemInput, addressID int64, fulfillment string) (*QuoteResponse, *apiError) {
	quote := &QuoteResponse{
		StoreID:         toStringID(storeID),
		FulfillmentType: fulfillment,
		Lines:           []QuoteLine{},
		Currency:        "CNY",
		ExpiresAt:       a.now().UTC().Add(10 * time.Minute),
	}
	var deliveryCents int64
	if fulfillment == "DELIVERY" {
		address, fee, err := addressSnapshotWith(ctx, q, ownerID, addressID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, badRequest("ADDRESS_NOT_FOUND", "地址不存在或不属于本人")
			}
			return nil, &apiError{500, "DB_ERROR", "读取地址失败"}
		}
		deliveryCents = fee
		quote.AddressID = address["id"].(string)
	}
	quote.DeliveryCents = deliveryCents
	today := a.now().In(a.tz).Format("2006-01-02")
	for _, item := range items {
		skuID, _ := strconv.ParseInt(item.SkuID, 10, 64)
		var line QuoteLine
		line.SkuID = item.SkuID
		line.Quantity = item.Quantity
		var productID int64
		var active, productActive bool
		err := q.QueryRowContext(ctx, `SELECT s.id,s.product_id,p.name,s.spec_name,s.price_cents,s.active,p.active
			FROM skus s JOIN products p ON p.id=s.product_id
			WHERE s.id=? AND s.store_id=?`, skuID, storeID).
			Scan(&skuID, &productID, &line.ProductName, &line.SpecName, &line.UnitPriceCents, &active, &productActive)
		if errors.Is(err, sql.ErrNoRows) {
			line.InStock = false
			line.Active = false
			quote.Lines = append(quote.Lines, line)
			continue
		}
		if err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取商品失败"}
		}
		var available int64
		var expiry sql.NullTime
		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(GREATEST(qty_on_hand-qty_reserved,0)),0), MIN(expiry_date)
			FROM inventory_lots WHERE store_id=? AND sku_id=? AND expiry_date>=?`, storeID, skuID, today).Scan(&available, &expiry)
		if err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取库存失败"}
		}
		line.ProductID = toStringID(productID)
		line.AvailableQty = available
		line.Active = active && productActive
		line.InStock = line.Active && available >= int64(item.Quantity)
		if expiry.Valid {
			line.ExpiryDate = expiry.Time.Format("2006-01-02")
		}
		line.GoodsCents = line.UnitPriceCents * int64(item.Quantity)
		quote.GoodsCents += line.GoodsCents
		quote.Lines = append(quote.Lines, line)
	}
	quote.TotalCents = quote.GoodsCents + quote.DeliveryCents - quote.DiscountCents
	if quote.TotalCents < 0 {
		quote.TotalCents = 0
	}
	return quote, nil
}

func addressSnapshotWith(ctx context.Context, q queryer, ownerID, addressID int64) (map[string]any, int64, error) {
	var id, buildingID, fee int64
	var buildingName, contact, phone, detail string
	err := q.QueryRowContext(ctx, `SELECT a.id,a.building_id,a.building_name_snapshot,a.contact_name,a.phone,a.detail,b.delivery_fee_cents
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
