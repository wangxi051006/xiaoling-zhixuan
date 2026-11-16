//go:build integration

package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

const merchantBatchPath = "/api/v1/admin/orders/batch-actions"

func merchantFixture(t *testing.T) (*App, *sql.DB, string, string) {
	t.Helper()
	a, d, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, d)
	return a, d, buyer, guardToken(t, a, 3)
}
func batchPayload(t *testing.T, d *sql.DB, key, action string, ids ...int64) map[string]any {
	t.Helper()
	items := []map[string]any{}
	for _, id := range ids {
		var v int64
		if err := d.QueryRow(`SELECT version FROM orders WHERE id=?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		items = append(items, map[string]any{"orderId": toStringID(id), "expectedVersion": v})
	}
	return map[string]any{"requestId": key, "action": action, "items": items}
}
func batchResults(t *testing.T, data map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing items %#v", data)
	}
	for _, row := range items {
		item := row.(map[string]any)
		out[item["orderId"].(string)] = item
	}
	return out
}
func batchLedger(t *testing.T, d *sql.DB) string {
	t.Helper()
	var out string
	if err := d.QueryRow(`SELECT CONCAT((SELECT COALESCE(SUM(qty_on_hand),0) FROM inventory_lots),':',(SELECT COALESCE(SUM(qty_reserved),0) FROM inventory_lots),':',(SELECT COALESCE(SUM(total_cents),0) FROM orders),':',(SELECT COALESCE(SUM(total_created),0) FROM customer_order_counters),':',(SELECT COALESCE(SUM(is_first_order),0) FROM orders),':',(SELECT COUNT(*) FROM stock_movements))`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMySQLMerchantBatchPartialAndVersions(t *testing.T) {
	a, d, buyer, admin := merchantFixture(t)
	paid, _ := guardPaidOrder(t, a, d, buyer, "batch_paid_fixture_0001")
	unpaid := paymentRaceOrder(t, a, buyer, "batch_unpaid_fixture_0001")
	body := batchPayload(t, d, "batch_partial_request_0001", "ACCEPT", paid, unpaid)
	body["items"] = append(body["items"].([]map[string]any), map[string]any{"orderId": "99999999", "expectedVersion": 1})
	before := batchLedger(t, d)
	first := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 200)
	result := batchResults(t, first)
	if first["status"] != "DONE" || result[toStringID(paid)]["result"] != "SUCCESS" || result[toStringID(unpaid)]["code"] != "INVALID_STATE" || result["99999999"]["code"] != "NOT_FOUND" {
		t.Fatalf("unexpected %#v", first)
	}
	bodyItems := body["items"].([]map[string]any)
	bodyItems[0], bodyItems[2] = bodyItems[2], bodyItems[0]
	replay := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 200)
	if !reflect.DeepEqual(result, batchResults(t, replay)) {
		t.Fatal("reordered request changed results")
	}
	// Same action request, same key, different payload must conflict.
	body["items"] = bodyItems[:1]
	guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 409)
	body["action"] = "READY"
	guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 409)
	stale := map[string]any{"requestId": "batch_stale_version_0001", "action": "READY", "items": []map[string]any{{"orderId": toStringID(paid), "expectedVersion": 1}}}
	staleData := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, stale), 200)
	if batchResults(t, staleData)[toStringID(paid)]["code"] != "VERSION_CONFLICT" {
		t.Fatal(staleData)
	}
	ready := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, batchPayload(t, d, "batch_ready_request_0001", "READY", paid)), 200)
	if batchResults(t, ready)[toStringID(paid)]["result"] != "SUCCESS" {
		t.Fatal(ready)
	}
	if before != batchLedger(t, d) {
		t.Fatal("batch changed stock, money, first order or sales ledger")
	}
	// All list projections expose the version the merchant will confirm.
	for _, url := range []string{"/api/v1/admin/orders", "/api/v1/admin/workbench", "/api/v1/orders"} {
		token := admin
		if url == "/api/v1/orders" {
			token = buyer
		}
		w := guardRequest(a, token, "GET", url, nil)
		guardStatus(t, w, 200)
		if !containsJSONVersion(w.Body.String()) {
			t.Fatalf("missing version: %s", w.Body)
		}
	}
	t.Log("partial success, stable replay, stale versions, all list versions, stock/money/first-count unchanged")
}
func containsJSONVersion(s string) bool {
	for i := 0; i+10 <= len(s); i++ {
		if s[i:i+10] == `"version":` {
			return true
		}
	}
	return false
}

func TestMySQLMerchantBatchConcurrentAndRecovery(t *testing.T) {
	a, d, buyer, admin := merchantFixture(t)
	id, _ := guardPaidOrder(t, a, d, buyer, "batch_concurrent_order_001")
	body := batchPayload(t, d, "batch_concurrent_request_001", "ACCEPT", id)
	const n = 12
	results := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = guardRequest(a, admin, "POST", merchantBatchPath, body) }(i)
	}
	wg.Wait()
	var batchID string
	for _, w := range results {
		data := guardStatus(t, w, 200)
		if batchResults(t, data)[toStringID(id)]["result"] != "SUCCESS" {
			t.Fatal(data)
		}
		batchID = data["batchId"].(string)
	}
	var events, audits int
	if err := d.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=? AND event_type='ORDER_PICKING'`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE object_id=? AND action='BATCH_ACCEPT'`, toStringID(id)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 1 || audits != 1 {
		t.Fatalf("events=%d audits=%d", events, audits)
	}
	found := guardStatus(t, guardRequest(a, admin, "GET", merchantBatchPath+"?requestId=batch_concurrent_request_001", nil), 200)
	if found["batchId"] != batchID {
		t.Fatal("lost response recovery returned another batch")
	}
	guardSQL(t, d, `UPDATE batch_operations SET status='RUNNING' WHERE id=?`, batchID)
	restarted, err := New(a.cfg, d, a.log)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close()
	data := guardStatus(t, guardRequest(restarted, admin, "POST", merchantBatchPath+"/"+batchID+"/resume", map[string]any{}), 200)
	if data["status"] != "DONE" {
		t.Fatal(data)
	}
	// Cross-batch competition must reject an old version without duplicating events.
	body["requestId"] = "batch_other_request_0001"
	data = guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 200)
	if batchResults(t, data)[toStringID(id)]["code"] != "VERSION_CONFLICT" {
		t.Fatal(data)
	}
	t.Log("12 concurrent same-key requests: one order event/audit; response loss lookup and new App resume passed")
}

func TestMySQLMerchantBatchFaultRecovery(t *testing.T) {
	for _, table := range []string{"audit_logs", "order_events"} {
		t.Run(table, func(t *testing.T) {
			a, d, buyer, admin := merchantFixture(t)
			first, _ := guardPaidOrder(t, a, d, buyer, "batch_recovery_first_order")
			second, _ := guardPaidOrder(t, a, d, buyer, "batch_recovery_second_order")
			body := batchPayload(t, d, "batch_recovery_request_001", "ACCEPT", first, second)
			// Abort only the second item's event/audit. The first must remain committed.
			condition := fmt.Sprintf("NEW.order_id=%d", second)
			if table == "audit_logs" {
				condition = fmt.Sprintf("NEW.object_id='%d' AND NEW.action='BATCH_ACCEPT'", second)
			}
			guardSQL(t, d, fmt.Sprintf("CREATE TRIGGER batch_fault BEFORE INSERT ON %s FOR EACH ROW BEGIN IF %s THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected batch failure'; END IF; END", table, condition))
			t.Cleanup(func() { d.Exec(`DROP TRIGGER IF EXISTS batch_fault`) })
			w := guardRequest(a, admin, "POST", merchantBatchPath, body)
			data := guardStatus(t, w, 503)
			if data["batchId"] == nil || data["completedCount"] != float64(1) || data["pendingCount"] != float64(1) {
				t.Fatalf("missing durable progress: %s", w.Body)
			}
			var status string
			if err := d.QueryRow(`SELECT status FROM orders WHERE id=?`, second).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "PAID" {
				t.Fatal("failed item's order did not roll back")
			}
			guardSQL(t, d, `DROP TRIGGER batch_fault`)
			restarted, err := New(a.cfg, d, a.log)
			if err != nil {
				t.Fatal(err)
			}
			restarted.Close()
			batchID := data["batchId"].(string)
			detail := guardStatus(t, guardRequest(restarted, admin, "GET", merchantBatchPath+"/"+batchID, nil), 200)
			if len(batchResults(t, detail)) != 2 {
				t.Fatal("GET must include pending targets")
			}
			resumed := guardStatus(t, guardRequest(restarted, admin, "POST", merchantBatchPath+"/"+batchID+"/resume", map[string]any{}), 200)
			if resumed["status"] != "DONE" || resumed["completedCount"] != float64(2) {
				t.Fatal(resumed)
			}
			var count int
			d.QueryRow(`SELECT COUNT(*) FROM order_events WHERE event_type='ORDER_PICKING'`).Scan(&count)
			if count != 2 {
				t.Fatalf("duplicated events: %d", count)
			}
		})
	}
}

func TestMySQLMerchantBatchBoundsAndOwnership(t *testing.T) {
	a, d, buyer, admin := merchantFixture(t)
	id, _ := guardPaidOrder(t, a, d, buyer, "batch_ownership_order_001")
	body := batchPayload(t, d, "batch_ownership_request_001", "ACCEPT", id)
	guardStatus(t, guardRequest(a, buyer, "POST", merchantBatchPath, body), 403)
	data := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, body), 200)
	batchID := data["batchId"].(string)
	guardSQL(t, d, `INSERT INTO users(id,phone,nickname,password_hash,status) SELECT 8,'13800000008','other admin',password_hash,'ACTIVE' FROM users WHERE id=3`)
	guardSQL(t, d, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (8,8,1,1)`)
	guardSQL(t, d, `INSERT INTO staff_roles(membership_id,role) VALUES (8,'store_admin')`)
	other := guardToken(t, a, 8)
	guardStatus(t, guardRequest(a, other, "GET", merchantBatchPath+"/"+batchID, nil), 404)
	guardStatus(t, guardRequest(a, other, "POST", merchantBatchPath+"/"+batchID+"/resume", map[string]any{}), 403)
	guardSQL(t, d, `INSERT INTO stores(id,name) VALUES (2,'other store')`)
	otherOrder := paymentRaceOrder(t, a, buyer, "batch_cross_store_order_001")
	guardSQL(t, d, `UPDATE orders SET store_id=2 WHERE id=?`, otherOrder)
	cross := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, batchPayload(t, d, "batch_cross_store_req_001", "ACCEPT", otherOrder)), 200)
	if batchResults(t, cross)[toStringID(otherOrder)]["code"] != "NOT_FOUND" {
		t.Fatal(cross)
	}
	items := []map[string]any{}
	for i := 0; i < 50; i++ {
		items = append(items, map[string]any{"orderId": fmt.Sprint(90000 + i), "expectedVersion": 1})
	}
	payload := map[string]any{"requestId": "batch_fifty_items_request", "action": "ACCEPT", "items": items}
	fifty := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, payload), 200)
	if fifty["completedCount"] != float64(50) {
		t.Fatal(fifty)
	}
	payload["items"] = append(items, map[string]any{"orderId": "99999", "expectedVersion": 1})
	guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, payload), 422)
	payload["items"] = []map[string]any{{"orderId": "1"}}
	guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, payload), 422)
	payload["items"] = []map[string]any{{"orderId": "1", "expectedVersion": 1}, {"orderId": "1", "expectedVersion": 2}}
	guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, payload), 409)
	// Cancelled request leaves no non-atomic item result.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	principal, err := a.loadPrincipal(context.Background(), 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.applyBatchItem(ctx, principal, 1, id, 1, "ACCEPT", "cancelled"); err == nil {
		t.Fatal("cancelled operation unexpectedly proceeded")
	}
}
