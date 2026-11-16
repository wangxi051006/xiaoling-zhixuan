//go:build integration
// +build integration

package app

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"campusretail/internal/config"
	"campusretail/internal/db"
)

func TestMySQLRefundConsistency(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	if !strings.Contains(dsn, "_test") {
		t.Fatal("TEST_MYSQL_DSN must point to a disposable *_test database")
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	cleanDatabase(t, database)
	seedIntegrationData(t, database)
	seedAdminForTest(t, database)
	cfg := config.Config{Env: "test", StoreID: 1, JWTSecret: "integration-test-secret-32-bytes!!", AccessTokenTTL: time.Hour, PaymentMode: "simulated", AUTO_MIGRATE: false}
	a, err := New(cfg, database, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := httptest.NewServer(a)
	defer server.Close()
	buyer := loginForTest(t, server.URL, "13900000001")
	other := loginForTest(t, server.URL, "13900000002")
	admin := loginForTest(t, server.URL, "13800000001")

	quote := quoteForTest(t, server.URL, buyer, "2", "1", 2)
	orderResult := postJSON(server.URL+"/api/v1/orders", buyer, map[string]any{
		"requestId": "refund_fixture_order_0001", "storeId": "1",
		"items":     []map[string]any{{"skuId": "2", "quantity": 2}},
		"addressId": "1", "fulfillmentType": "DELIVERY", "quoteId": quote, "expectedTotalCents": 1600,
	})
	if orderResult.status != 201 {
		t.Fatalf("fixture order failed: %d %#v", orderResult.status, orderResult.body)
	}
	orderIDText := orderResult.body["data"].(map[string]any)["orderId"].(string)
	var orderID int64
	_, _ = fmt.Sscan(orderIDText, &orderID)
	if _, err := database.Exec(`UPDATE orders SET status='PAID',payment_status='PAID',paid_at=UTC_TIMESTAMP(6) WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	var orderItemID int64
	if err := database.QueryRow(`SELECT id FROM order_items WHERE order_id=? AND sku_id=2`, orderID).Scan(&orderItemID); err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{
		{"orderItemId": fmt.Sprint(orderItemID), "quantity": 1},
		{"orderItemId": fmt.Sprint(orderItemID), "quantity": 1},
	}
	payload := map[string]any{
		"requestId": "refund_merge_0000000001", "reason": "重复行合并", "items": items,
	}
	// Gate all first calls on the same order lock. Every call must replay the
	// committed refund, rather than reject the winner's reserved quantity.
	gate, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Exec(`SELECT id FROM orders WHERE id=? FOR UPDATE`, orderID); err != nil {
		gate.Rollback()
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	responses := make(chan httpResult, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/after-sales", buyer, payload)
		}()
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(responses)
	var firstID string
	for response := range responses {
		if response.status != 201 {
			t.Fatalf("concurrent refund replay failed: %d %#v", response.status, response.body)
		}
		id := response.body["data"].(map[string]any)["id"].(string)
		if firstID != "" && firstID != id {
			t.Fatalf("concurrent replay returned another refund: %s != %s", id, firstID)
		}
		firstID = id
	}
	var refundID int64
	var refundAmount int64
	var itemCount int
	var itemAmount int64
	if err := database.QueryRow(`SELECT id,amount_cents FROM refunds WHERE order_id=?`, orderID).Scan(&refundID, &refundAmount); err != nil {
		t.Fatal(err)
	}
	_ = database.QueryRow(`SELECT COUNT(*),COALESCE(SUM(amount_cents),0) FROM refund_items WHERE refund_id=?`, refundID).Scan(&itemCount, &itemAmount)
	if itemCount != 1 || itemAmount != refundAmount || refundAmount != 1400 {
		t.Fatalf("refund amount mismatch: refund=%d items=%d sum=%d", refundAmount, itemCount, itemAmount)
	}
	// Another buyer reusing the same requestId must not get the original refund id.
	cross := postJSON(server.URL+"/api/v1/orders/999999/after-sales", other, map[string]any{
		"requestId": "refund_merge_0000000001", "reason": "越权重放", "items": items,
	})
	if cross.status != 409 {
		t.Fatalf("cross-user idempotent replay must be 409, got %d %#v", cross.status, cross.body)
	}
	// A second distinct request for the same line is blocked by pending pre-occupation.
	second := postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/after-sales", buyer, map[string]any{
		"requestId": "refund_pending_00000002", "reason": "再退一件", "items": []map[string]any{{"orderItemId": fmt.Sprint(orderItemID), "quantity": 1}},
	})
	if second.status != 409 {
		t.Fatalf("pending quantity must block second request, got %d %#v", second.status, second.body)
	}
	// Simulated success then verify wrong-SKU restock is rejected.
	ok := postJSON(server.URL+"/api/v1/test/refunds/"+fmt.Sprint(refundID)+"/succeed", admin, map[string]any{"confirm": "SIMULATED_REFUND"})
	if ok.status != 200 {
		t.Fatalf("simulated refund failed: %d %#v", ok.status, ok.body)
	}
	wrong := postJSON(server.URL+"/api/v1/admin/refunds/"+fmt.Sprint(refundID)+"/restock", admin, map[string]any{
		"requestId":    "refund_wrong_lot_00001",
		"refundItemId": fmt.Sprint(firstItemID(t, database, refundID)), "lotId": "1", "quantity": 1,
	})
	if wrong.status != 409 {
		t.Fatalf("wrong SKU restock must be 409, got %d %#v", wrong.status, wrong.body)
	}
	var stockBefore int64
	if err := database.QueryRow(`SELECT qty_on_hand FROM inventory_lots WHERE id=2`).Scan(&stockBefore); err != nil {
		t.Fatal(err)
	}
	restockPayload := map[string]any{
		"requestId":    "refund_partial_restock_01",
		"refundItemId": fmt.Sprint(firstItemID(t, database, refundID)), "lotId": "2", "quantity": 1,
	}
	restockURL := server.URL + "/api/v1/admin/refunds/" + fmt.Sprint(refundID) + "/restock"
	right := postJSON(restockURL, admin, restockPayload)
	if right.status != 200 {
		t.Fatalf("correct SKU restock failed: %d %#v", right.status, right.body)
	}
	for i := 0; i < 3; i++ {
		if r := postJSON(restockURL, admin, restockPayload); r.status != 200 {
			t.Fatalf("restock replay failed: %d %#v", r.status, r.body)
		}
	}
	restockPayload["quantity"] = 2
	if r := postJSON(restockURL, admin, restockPayload); r.status != 409 {
		t.Fatalf("changed restock payload accepted: %d", r.status)
	}
	restockPayload["quantity"] = 1
	restockPayload["requestId"] = "refund_partial_restock_02"
	if r := postJSON(restockURL, admin, restockPayload); r.status != 200 {
		t.Fatalf("remaining quantity could not restock: %d %#v", r.status, r.body)
	}
	restockPayload["requestId"] = "refund_partial_restock_03"
	if r := postJSON(restockURL, admin, restockPayload); r.status != 409 {
		t.Fatalf("over-restock accepted: %d", r.status)
	}
	var actualStock, restocked, movements int64
	if err := database.QueryRow(`SELECT qty_on_hand FROM inventory_lots WHERE id=2`).Scan(&actualStock); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM refund_restocks WHERE refund_item_id=?`, firstItemID(t, database, refundID)).Scan(&restocked); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE movement_type='REFUND_RESTOCK' AND ref_id=?`, refundID).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if actualStock != stockBefore+2 || restocked != 2 || movements != 2 {
		t.Fatalf("restock ledger mismatch stock=%d before=%d quantity=%d movements=%d", actualStock, stockBefore, restocked, movements)
	}
}

func firstItemID(t *testing.T, database *sql.DB, refundID int64) int64 {
	t.Helper()
	var id int64
	if err := database.QueryRow(`SELECT id FROM refund_items WHERE refund_id=? ORDER BY id LIMIT 1`, refundID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedAdminForTest(t *testing.T, database *sql.DB) {
	t.Helper()
	hash, err := HashPassword("CampusDemo!2026")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) VALUES (3,'13800000001','管理员',?,'ACTIVE',1)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO staff_memberships(id,user_id,store_id,active,version) VALUES (1,3,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"store_admin", "refund_read", "refund_write", "inventory_write"} {
		if _, err := database.Exec(`INSERT IGNORE INTO staff_roles(membership_id,role) VALUES (1,?)`, role); err != nil {
			t.Fatal(err)
		}
	}
}
