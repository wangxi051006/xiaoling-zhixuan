//go:build integration

package app

import (
	"strings"
	"testing"
)

func TestMySQLLowStockThresholdReport(t *testing.T) {
	cases := []struct {
		name string
		sql  []string
		want int
	}{
		{"zero stock", []string{`UPDATE inventory_lots SET qty_on_hand=0,qty_reserved=0`}, 2},
		{"threshold equality", []string{`UPDATE inventory_lots SET qty_on_hand=6`, `UPDATE inventory_lots SET qty_on_hand=5 WHERE sku_id=1`}, 1},
		{"reserved quantity", []string{`UPDATE inventory_lots SET qty_on_hand=6`, `UPDATE inventory_lots SET qty_on_hand=10,qty_reserved=5 WHERE sku_id=1`}, 1},
		{"no batches", []string{`DELETE FROM inventory_lots`}, 2},
		{"expired batches", []string{`UPDATE inventory_lots SET expiry_date='2000-01-01'`}, 2},
		{"inactive sku", []string{`UPDATE inventory_lots SET qty_on_hand=0`, `UPDATE skus SET active=0 WHERE id=2`}, 1},
		{"inactive product", []string{`UPDATE inventory_lots SET qty_on_hand=0`, `UPDATE products SET active=0`}, 0},
		{"per sku thresholds", []string{`UPDATE skus SET low_stock_threshold=50 WHERE id=1`, `UPDATE skus SET low_stock_threshold=1 WHERE id=2`}, 1},
		{"multiple batches", []string{`INSERT INTO inventory_lots(store_id,sku_id,batch_no,expiry_date,qty_on_hand,qty_reserved,unit_cost_cents) VALUES (1,2,'EXTRA','2027-12-31',4,0,300)`}, 0},
		{"other store excluded", []string{`INSERT INTO stores(id,name) VALUES(2,'其他门店')`, `INSERT INTO categories(id,store_id,name,active) VALUES(2,2,'分类',1)`, `INSERT INTO products(id,store_id,category_id,name,description,active) VALUES(2,2,2,'其他门店商品','',1)`, `INSERT INTO skus(id,store_id,product_id,spec_name,price_cents,active) VALUES(3,2,2,'规格',100,1)`}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, database, _ := paymentRaceFixture(t)
			a.Close()
			seedAdminForTest(t, database)
			for _, q := range tc.sql {
				guardSQL(t, database, q)
			}
			data := guardStatus(t, guardRequest(a, guardToken(t, a, 3), "GET", "/api/v1/admin/reports/dashboard", nil), 200)
			if data["lowStockCount"] != float64(tc.want) {
				t.Fatalf("lowStockCount=%v want %d", data["lowStockCount"], tc.want)
			}
		})
	}
}

func TestMySQLLowStockDatabaseErrorIsNotZero(t *testing.T) {
	a, database, _ := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	guardSQL(t, database, `RENAME TABLE inventory_lots TO stock_report_fault_lots`)
	defer guardSQL(t, database, `RENAME TABLE stock_report_fault_lots TO inventory_lots`)
	w := guardRequest(a, guardToken(t, a, 3), "GET", "/api/v1/admin/reports/dashboard", nil)
	guardStatus(t, w, 500)
	if !strings.Contains(w.Body.String(), "DB_ERROR") {
		t.Fatal(w.Body.String())
	}
}

func TestMySQLSKUThresholdConfiguration(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	admin := guardToken(t, a, 3)
	sku := map[string]any{"id": "1", "specName": "70g", "priceCents": 650, "active": true, "lowStockThreshold": 0}
	body := map[string]any{"name": "测试薯片", "description": "", "active": true, "expectedVersion": 1, "skus": []any{sku}}
	path := "/api/v1/admin/products/1"
	guardStatus(t, guardRequest(a, buyer, "PATCH", path, body), 403)
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 200)
	var threshold, version int64
	if err := database.QueryRow(`SELECT low_stock_threshold FROM skus WHERE id=1`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if threshold != 0 {
		t.Fatalf("zero threshold=%d", threshold)
	}
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 409)
	body["expectedVersion"] = 2
	for _, invalid := range []any{-1, 1.5, 2147483648} {
		sku["lowStockThreshold"] = invalid
		guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 422)
	}
	sku["lowStockThreshold"] = 3
	sku["priceCents"] = maxPriceCents + 1
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 422)
	sku["priceCents"] = 650
	guardSQL(t, database, `CREATE TRIGGER threshold_audit_reject BEFORE INSERT ON audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit failure'`)
	w := guardRequest(a, admin, "PATCH", path, body)
	guardSQL(t, database, `DROP TRIGGER threshold_audit_reject`)
	guardStatus(t, w, 500)
	if err := database.QueryRow(`SELECT low_stock_threshold FROM skus WHERE id=1`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if threshold != 0 {
		t.Fatalf("audit failure committed threshold %d", threshold)
	}
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 200)
	var before, after string
	if err := database.QueryRow(`SELECT CAST(before_json AS CHAR),CAST(after_json AS CHAR) FROM audit_logs ORDER BY id DESC LIMIT 1`).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, `"lowStockThreshold": 0`) || !strings.Contains(after, `"lowStockThreshold": 3`) {
		t.Fatalf("audit before=%s after=%s", before, after)
	}
	// Older clients omitting the new field preserve its configured value.
	delete(sku, "lowStockThreshold")
	body["expectedVersion"] = 3
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 200)
	if err := database.QueryRow(`SELECT low_stock_threshold FROM skus WHERE id=1`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if threshold != 3 {
		t.Fatalf("omitted threshold reset to %d", threshold)
	}
	// A SKU from another product must not silently succeed or change product version.
	body["expectedVersion"] = 4
	sku["id"] = "99999"
	guardStatus(t, guardRequest(a, admin, "PATCH", path, body), 404)
	if err := database.QueryRow(`SELECT version FROM products WHERE id=1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("rejected SKU committed product version %d", version)
	}
	products := guardRequest(a, admin, "GET", "/api/v1/admin/products", nil)
	if products.Code != 200 || !strings.Contains(products.Body.String(), `"lowStockThreshold":3`) {
		t.Fatalf("threshold not readable: %s", products.Body)
	}
	// New products accept explicit zero and default omitted thresholds to five.
	create := map[string]any{"name": "新商品", "categoryId": "1", "active": true, "skus": []any{map[string]any{"specName": "零阈值", "priceCents": 100, "active": true, "lowStockThreshold": 0}, map[string]any{"specName": "默认阈值", "priceCents": 200, "active": true}}}
	guardStatus(t, guardRequest(a, admin, "POST", "/api/v1/admin/products", create), 201)
	if err := database.QueryRow(`SELECT low_stock_threshold FROM skus WHERE spec_name='默认阈值'`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if threshold != 5 {
		t.Fatalf("default threshold %d", threshold)
	}
}
