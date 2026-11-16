//go:build integration
// +build integration

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"campusretail/internal/config"
)

func TestMySQLRefundRestockShanghaiDateAndConcurrentLimit(t *testing.T) {
	database := refundTestDatabase(t)
	seedAdminForTest(t, database)
	a, err := New(config.Config{Env: "test", StoreID: 1, JWTSecret: "integration-test-secret-32-bytes!!", AccessTokenTTL: 48 * time.Hour, PaymentMode: "simulated"}, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Stop workers before injecting a deterministic clock. The session remains
	// UTC while China has already entered the next calendar date.
	a.Close()
	chinaDay := time.Now().In(a.tz)
	now := time.Date(chinaDay.Year(), chinaDay.Month(), chinaDay.Day(), 0, 30, 0, 0, a.tz)
	a.now = func() time.Time { return now.UTC() }
	server := httptest.NewServer(a)
	defer server.Close()
	admin := loginForTest(t, server.URL, "13800000001")
	order, item := refundOrderFixture(t, database, "restock_calendar_order", 2)
	ref, apiErr := a.createRefund(context.Background(), order, "restock_calendar_refund01", "date", []refundCreateItem{{OrderItemID: fmt.Sprint(item), Quantity: 2}}, "U", 1, 0, false, "")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	var refundID int64
	fmt.Sscan(ref.ID, &refundID)
	if err := a.confirmRefund(context.Background(), refundID, "RESTOCK-DATE", a.now(), "SIMULATED"); err != nil {
		t.Fatal(err)
	}
	url := server.URL + "/api/v1/admin/refunds/" + ref.ID + "/restock"
	refundItem := firstItemID(t, database, refundID)
	payload := map[string]any{"requestId": "restock_expired_date_01", "refundItemId": fmt.Sprint(refundItem), "lotId": "1", "quantity": 1}
	refundSQL(t, database, `UPDATE inventory_lots SET expiry_date=? WHERE id=1`, now.AddDate(0, 0, -1).Format("2006-01-02"))
	if r := postJSON(url, admin, payload); r.status != 404 {
		t.Fatalf("yesterday's lot accepted after Shanghai midnight: %d %#v", r.status, r.body)
	}
	payload["lotId"] = ""
	if r := postJSON(url, admin, payload); r.status != 409 {
		t.Fatalf("automatic lot selection accepted expired stock: %d %#v", r.status, r.body)
	}
	refundSQL(t, database, `UPDATE inventory_lots SET expiry_date=? WHERE id=1`, now.Format("2006-01-02"))
	payload["lotId"] = "1"
	payload["requestId"] = "restock_concurrent_key01"
	var before int64
	if err := database.QueryRow(`SELECT qty_on_hand FROM inventory_lots WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	responses := make(chan httpResult, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- postJSON(url, admin, payload) }()
	}
	wg.Wait()
	close(responses)
	for r := range responses {
		if r.status != 200 {
			t.Fatalf("same-key concurrent restock did not replay: %d %#v", r.status, r.body)
		}
	}
	responses = make(chan httpResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses <- postJSON(url, admin, map[string]any{"requestId": fmt.Sprintf("restock_remaining_%04d", i), "refundItemId": fmt.Sprint(refundItem), "lotId": "1", "quantity": 1})
		}(i)
	}
	wg.Wait()
	close(responses)
	success, exceeded := 0, 0
	for r := range responses {
		if r.status == 200 {
			success++
		} else if r.status == 409 {
			exceeded++
		} else {
			t.Fatalf("unexpected restock result: %d %#v", r.status, r.body)
		}
	}
	if success != 1 || exceeded != 1 {
		t.Fatalf("remaining entitlement raced: success=%d exceeded=%d", success, exceeded)
	}
	var after, restocked, movements int64
	if err := database.QueryRow(`SELECT qty_on_hand FROM inventory_lots WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM refund_restocks WHERE refund_item_id=?`, refundItem).Scan(&restocked); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE movement_type='REFUND_RESTOCK' AND ref_id=?`, refundID).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if after-before != 2 || restocked != 2 || movements != 2 {
		t.Fatalf("duplicate/excess restock altered stock: stockDelta=%d restocked=%d movements=%d", after-before, restocked, movements)
	}
	detail := a.loadRefundItems(context.Background(), refundID)
	if len(detail) != 1 || detail[0].ID != fmt.Sprint(refundItem) || detail[0].SKUID != "1" || detail[0].RestockedQuantity != 2 {
		t.Fatalf("restock DTO missing identity/quantity: %#v", detail)
	}
}
