package app

import (
	"math"
	"net/http"
	"strings"
	"time"
)

func (a *App) adminDailySKU(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	report, apiErr := a.buildDailySKU(r, r.URL.Query().Get("date"), includeDemo(r), p.Staff.StoreID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), report)
}

func includeDemo(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("includeDemo")))
	if value == "false" || value == "0" {
		return false
	}
	return true
}

func (a *App) buildDailySKU(r *http.Request, date string, withDemo bool, storeID int64) (*DailySKUReport, *apiError) {
	start, _, normalized, err := a.dateWindow(date)
	if err != nil {
		return nil, err.(*apiError)
	}
	_ = start
	if a.cfg.Env == "production" {
		withDemo = false
	}
	demoFilter := "1=1"
	if !withDemo {
		demoFilter = "o.is_demo=0"
	}
	query := `SELECT s.id,p.id,p.name,s.spec_name,
		COALESCE(paid.qty,0),COALESCE(paid.cents,0),COALESCE(paid.paid_orders,0),
		COALESCE(ref.qty,0),COALESCE(ref.cents,0)
		FROM skus s JOIN products p ON p.id=s.product_id
		LEFT JOIN (
			SELECT oi.sku_id,SUM(oi.quantity) qty,SUM(oi.goods_cents) cents,COUNT(DISTINCT oi.order_id) paid_orders
			FROM order_items oi JOIN orders o ON o.id=oi.order_id
			WHERE o.store_id=? AND o.paid_at IS NOT NULL AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=?
			  AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED') AND ` + demoFilter + `
			GROUP BY oi.sku_id
		) paid ON paid.sku_id=s.id
		LEFT JOIN (
			SELECT oi.sku_id,SUM(ri.quantity) qty,SUM(ri.amount_cents) cents
			FROM refund_items ri JOIN refunds r ON r.id=ri.refund_id
			JOIN order_items oi ON oi.id=ri.order_item_id JOIN orders o ON o.id=r.order_id
			WHERE o.store_id=? AND r.status='SUCCESS' AND o.paid_at IS NOT NULL
			  AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=? AND ` + demoFilter + `
			GROUP BY oi.sku_id
		) ref ON ref.sku_id=s.id
		WHERE p.store_id=?
		ORDER BY p.id,s.id`
	rows, err := a.db.QueryContext(r.Context(), query, storeID, normalized, storeID, normalized, storeID)
	if err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取每日商品销售失败"}
	}
	defer rows.Close()
	report := &DailySKUReport{
		Date: normalized, Timezone: "Asia/Shanghai", AsOf: a.now().UTC(),
		Rows: []DailySKURow{}, Totals: DailySKURow{}, DemoIncluded: false,
	}
	var demoCount int64
	_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM orders o WHERE o.store_id=? AND o.is_demo=1
		AND o.paid_at IS NOT NULL AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=?`, storeID, normalized).Scan(&demoCount)
	report.DemoIncluded = demoCount > 0 && withDemo
	for rows.Next() {
		var row DailySKURow
		var skuID, productID int64
		if err := rows.Scan(&skuID, &productID, &row.ProductName, &row.SpecName,
			&row.PaidQuantity, &row.GoodsSalesCents, &row.PaidOrders,
			&row.SuccessfulRefundQuantity, &row.GoodsRefundCents); err != nil {
			return nil, &apiError{500, "DB_ERROR", "读取每日商品销售失败"}
		}
		row.SkuID = toStringID(skuID)
		row.ProductID = toStringID(productID)
		row.NetQuantity = row.PaidQuantity - row.SuccessfulRefundQuantity
		row.GoodsNetCents = row.GoodsSalesCents - row.GoodsRefundCents
		report.Rows = append(report.Rows, row)
		report.Totals.PaidQuantity += row.PaidQuantity
		report.Totals.SuccessfulRefundQuantity += row.SuccessfulRefundQuantity
		report.Totals.NetQuantity += row.NetQuantity
		report.Totals.GoodsSalesCents += row.GoodsSalesCents
		report.Totals.GoodsRefundCents += row.GoodsRefundCents
		report.Totals.GoodsNetCents += row.GoodsNetCents
	}
	if err := rows.Err(); err != nil {
		return nil, &apiError{500, "DB_ERROR", "读取每日商品销售失败"}
	}
	_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(DISTINCT o.id),COALESCE(SUM(o.delivery_cents),0)
		FROM orders o WHERE o.store_id=? AND o.paid_at IS NOT NULL
		AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=? AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED')
		AND `+demoFilter, storeID, normalized).Scan(&report.Totals.PaidOrders, &report.DeliveryCents)
	refundRows, err := a.db.QueryContext(r.Context(), `SELECT r.id,o.order_no,rc.amount_cents,rc.occurred_at
		FROM refund_cash_ledger rc JOIN refunds r ON r.id=rc.refund_id JOIN orders o ON o.id=r.order_id
		WHERE o.store_id=? AND DATE(DATE_ADD(rc.occurred_at, INTERVAL 8 HOUR))=? AND `+demoFilter+` ORDER BY rc.occurred_at`, storeID, normalized)
	if err == nil {
		defer refundRows.Close()
		for refundRows.Next() {
			var cash CashRefund
			var id int64
			if err := refundRows.Scan(&id, &cash.OrderNo, &cash.AmountCents, &cash.OccurredAt); err == nil {
				cash.RefundID = toStringID(id)
				report.CashRefunds = append(report.CashRefunds, cash)
			}
		}
	}
	if report.CashRefunds == nil {
		report.CashRefunds = []CashRefund{}
	}
	return report, nil
}

func (a *App) adminDashboard(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	daily, apiErr := a.buildDailySKU(r, r.URL.Query().Get("date"), includeDemo(r), p.Staff.StoreID)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	dashboard := Dashboard{
		Date: daily.Date, Timezone: daily.Timezone, AsOf: a.now().UTC(),
		GoodsSalesCents: daily.Totals.GoodsSalesCents, GoodsRefundCents: daily.Totals.GoodsRefundCents,
		GoodsNetCents: daily.Totals.GoodsNetCents, DeliveryCents: daily.DeliveryCents,
		DailySKU: *daily, DemoIncluded: daily.DemoIncluded, HourlySales: []HourlySales{},
	}
	demoFilter := "1=1"
	if !includeDemo(r) || a.cfg.Env == "production" {
		demoFilter = "o.is_demo=0"
	}
	_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(*),
		COALESCE(SUM(o.is_first_order=1),0),COALESCE(SUM(o.is_first_order=0),0)
		FROM orders o WHERE o.store_id=? AND o.paid_at IS NOT NULL
		AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=?
		AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED') AND `+demoFilter,
		p.Staff.StoreID, daily.Date).Scan(&dashboard.PaidOrders, &dashboard.NewUserOrders, &dashboard.ReturningOrders)
	_ = a.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(rc.amount_cents),0) FROM refund_cash_ledger rc
		JOIN refunds r ON r.id=rc.refund_id JOIN orders o ON o.id=r.order_id
		WHERE o.store_id=? AND DATE(DATE_ADD(rc.occurred_at, INTERVAL 8 HOUR))=? AND `+demoFilter,
		p.Staff.StoreID, daily.Date).Scan(&dashboard.RefundCashCents)
	var lowStock int64
	err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM (
		SELECT s.id,s.low_stock_threshold,COALESCE(SUM(GREATEST(l.qty_on_hand-l.qty_reserved,0)),0) available
		FROM skus s JOIN products p ON p.id=s.product_id
		LEFT JOIN inventory_lots l ON l.sku_id=s.id AND l.store_id=s.store_id AND l.expiry_date>=CURDATE()
		WHERE s.store_id=? AND s.active=1 AND p.active=1
		GROUP BY s.id,s.low_stock_threshold
	) low WHERE available<=low_stock_threshold`, p.Staff.StoreID).Scan(&lowStock)
	if err != nil {
		writeError(w, r, &apiError{500, "DB_ERROR", "读取库存预警失败"})
		return
	}
	dashboard.LowStockCount = lowStock
	hourly := make([]HourlySales, 24)
	for hour := 0; hour < 24; hour++ {
		hourly[hour] = HourlySales{Hour: hour, Future: isFutureHour(daily.Date, hour, a.tz, a.now())}
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT HOUR(DATE_ADD(o.paid_at, INTERVAL 8 HOUR)) h,COUNT(*),COALESCE(SUM(o.goods_cents),0)
		FROM orders o WHERE o.store_id=? AND o.paid_at IS NOT NULL
		AND DATE(DATE_ADD(o.paid_at, INTERVAL 8 HOUR))=?
		AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED') AND `+demoFilter+`
		GROUP BY h ORDER BY h`, p.Staff.StoreID, daily.Date)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var hour int
			var count, cents int64
			if err := rows.Scan(&hour, &count, &cents); err == nil && hour >= 0 && hour < 24 {
				hourly[hour].Orders = count
				hourly[hour].SalesCents = cents
			}
		}
	}
	dashboard.HourlySales = hourly
	dashboard.Replenishment = a.buildReplenishment(r, demoFilter, p.Staff.StoreID)
	writeJSON(w, http.StatusOK, traceIDFrom(r), dashboard)
}

func isFutureHour(date string, hour int, loc *time.Location, now time.Time) bool {
	if date != now.In(loc).Format("2006-01-02") {
		return false
	}
	return hour > now.In(loc).Hour()
}

func (a *App) buildReplenishment(r *http.Request, demoFilter string, storeID int64) []Replenishment {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT s.id,p.name,s.spec_name,
			COALESCE(stock.available,0),COALESCE(sold.qty,0)
		FROM skus s JOIN products p ON p.id=s.product_id
		LEFT JOIN (
			SELECT l.sku_id,SUM(GREATEST(l.qty_on_hand-l.qty_reserved,0)) available
			FROM inventory_lots l WHERE l.store_id=? AND l.expiry_date>=CURDATE() GROUP BY l.sku_id
		) stock ON stock.sku_id=s.id
		LEFT JOIN (
			SELECT oi.sku_id,SUM(oi.quantity) qty FROM order_items oi JOIN orders o ON o.id=oi.order_id
			WHERE o.store_id=? AND o.paid_at>=DATE_SUB(NOW(6),INTERVAL 7 DAY)
			  AND o.payment_status IN ('PAID','PARTIAL_REFUNDED','REFUNDED') AND `+demoFilter+`
			GROUP BY oi.sku_id
		) sold ON sold.sku_id=s.id
		WHERE s.store_id=? AND s.active=1 AND p.active=1
		ORDER BY (COALESCE(stock.available,0)-COALESCE(sold.qty,0)) ASC, sold.qty DESC LIMIT 8`,
		storeID, storeID, storeID)
	if err != nil {
		return []Replenishment{}
	}
	defer rows.Close()
	items := []Replenishment{}
	for rows.Next() {
		var item Replenishment
		var id int64
		if err := rows.Scan(&id, &item.ProductName, &item.SpecName, &item.AvailableQty, &item.Sold7Days); err != nil {
			continue
		}
		item.SkuID = toStringID(id)
		target := int64(math.Ceil(float64(item.Sold7Days) / 7 * 7))
		item.SuggestedQty = target - item.AvailableQty
		if item.SuggestedQty < 0 {
			item.SuggestedQty = 0
		}
		item.Reason = "近7天销量与可用库存对比；人工复核后确认补货，不自动改库存"
		items = append(items, item)
	}
	return items
}
