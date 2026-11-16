//go:build integration
// +build integration

package app

import (
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

func TestMySQLUrgeRules(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; skipping real MySQL integration test")
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
	cfg := config.Config{
		Env: "test", StoreID: 1, JWTSecret: "integration-test-secret-32-bytes!!",
		AccessTokenTTL: time.Hour, PaymentMode: "simulated", AUTO_MIGRATE: false,
	}
	a, err := New(cfg, database, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := httptest.NewServer(a)
	defer server.Close()
	token1 := loginForTest(t, server.URL, "13900000001")
	token2 := loginForTest(t, server.URL, "13900000002")
	quote := quoteForTest(t, server.URL, token1, "1", "1", 1)
	orderResult := postJSON(server.URL+"/api/v1/orders", token1, map[string]any{
		"requestId": "urge_fixture_order_0001", "storeId": "1",
		"items":     []map[string]any{{"skuId": "1", "quantity": 1}},
		"addressId": "1", "fulfillmentType": "DELIVERY", "quoteId": quote, "expectedTotalCents": 850,
	})
	if orderResult.status != 201 {
		t.Fatalf("fixture order failed: %d %#v", orderResult.status, orderResult.body)
	}
	orderData := orderResult.body["data"].(map[string]any)
	orderIDText := orderData["orderId"].(string)
	var orderID int64
	if _, err := fmt.Sscan(orderIDText, &orderID); err != nil {
		t.Fatal(err)
	}
	// This test isolates urge semantics; payment is marked paid directly so
	// the urge path is tested independently from the payment channel.
	if _, err := database.Exec(`UPDATE orders SET status='PAID',payment_status='PAID',paid_at=NOW(6) WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	var totalBefore, ordinalBefore, reservedBefore int64
	_ = database.QueryRow(`SELECT total_cents,customer_order_ordinal FROM orders WHERE id=?`, orderID).Scan(&totalBefore, &ordinalBefore)
	_ = database.QueryRow(`SELECT COALESCE(SUM(qty_reserved),0) FROM inventory_lots WHERE id=1`).Scan(&reservedBefore)
	const workers = 10
	body := map[string]any{"requestId": "urge_integration_same_0001", "reason": "请尽快出餐"}
	results := make([]httpResult, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index] = postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/urges", token1, body)
		}(index)
	}
	group.Wait()
	created := 0
	ids := map[string]bool{}
	for _, result := range results {
		if result.status == 201 {
			created++
		}
		if data, ok := result.body["data"].(map[string]any); ok {
			if urge, ok := data["urge"].(map[string]any); ok {
				if id, ok := urge["id"].(string); ok {
					ids[id] = true
				}
			}
		}
		if result.status >= 500 {
			t.Fatalf("unexpected 5xx: %d %#v", result.status, result.body)
		}
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("same request must create one urge, created=%d ids=%v", created, ids)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM order_urges WHERE order_id=?`, orderID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected one urge record, got %d (%v)", count, err)
	}
	different := postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/urges", token1, map[string]any{
		"requestId": "urge_integration_same_0001", "reason": "不同参数",
	})
	if different.status != 409 {
		t.Fatalf("same key different params must be 409, got %d %#v", different.status, different.body)
	}
	cooldown := postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/urges", token1, map[string]any{
		"requestId": "urge_integration_new_0002", "reason": "再催一次",
	})
	if cooldown.status != 409 {
		t.Fatalf("cooldown must reject second urge, got %d %#v", cooldown.status, cooldown.body)
	}
	crossUser := postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/urges", token2, map[string]any{
		"requestId": "urge_integration_cross_0003", "reason": "越权",
	})
	if crossUser.status != 404 {
		t.Fatalf("cross-user urge must be 404, got %d %#v", crossUser.status, crossUser.body)
	}
	if _, err := database.Exec(`UPDATE orders SET status='COMPLETED',delivery_status='DELIVERED' WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	completed := postJSON(server.URL+"/api/v1/orders/"+orderIDText+"/urges", token1, map[string]any{
		"requestId": "urge_integration_completed_0004", "reason": "完成后再催",
	})
	if completed.status != 409 {
		t.Fatalf("completed order urge must be 409, got %d %#v", completed.status, completed.body)
	}
	var totalAfter, ordinalAfter, reservedAfter int64
	_ = database.QueryRow(`SELECT total_cents,customer_order_ordinal FROM orders WHERE id=?`, orderID).Scan(&totalAfter, &ordinalAfter)
	_ = database.QueryRow(`SELECT COALESCE(SUM(qty_reserved),0) FROM inventory_lots WHERE id=1`).Scan(&reservedAfter)
	if totalBefore != totalAfter || ordinalBefore != ordinalAfter || reservedBefore != reservedAfter {
		t.Fatalf("urge changed order/inventory: total %d->%d ordinal %d->%d reserved %d->%d",
			totalBefore, totalAfter, ordinalBefore, ordinalAfter, reservedBefore, reservedAfter)
	}
}
