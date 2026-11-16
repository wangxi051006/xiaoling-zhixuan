//go:build integration

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func guardRequest(a *App, token, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Simulated-Payment", "confirm")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func guardStatus(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body)
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if data, ok := response["data"].(map[string]any); ok {
		return data
	}
	return response
}
func guardSQL(t *testing.T, database *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := database.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func guardToken(t *testing.T, a *App, id int64) string {
	t.Helper()
	p, err := a.loadPrincipal(context.Background(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.issueToken(p)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func guardPaidOrder(t *testing.T, a *App, database *sql.DB, buyer, key string) (int64, int64) {
	t.Helper()
	id := paymentRaceOrder(t, a, buyer, key)
	guardStatus(t, guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/payment-attempt", id), map[string]any{"channel": "SIMULATED"}), 201)
	guardStatus(t, guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/test/payments/%d/succeed", id), map[string]any{"confirm": "SIMULATED_PAYMENT"}), 202)
	var task int64
	if err := database.QueryRow(`SELECT id FROM delivery_tasks WHERE order_id=?`, id).Scan(&task); err != nil {
		t.Fatal(err)
	}
	return id, task
}

func TestMySQLDeliveryCommandsAndPrivacy(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close() // deterministic test controls background work explicitly
	seedAdminForTest(t, database)
	for _, id := range []int64{4, 5} {
		guardSQL(t, database, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) SELECT ?,?,CONCAT('rider',?),password_hash,'ACTIVE',1 FROM users WHERE id=1`, id, fmt.Sprintf("1380000000%d", id), id)
		guardSQL(t, database, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (?,?,1,1)`, id, id)
		guardSQL(t, database, `INSERT INTO staff_roles(membership_id,role) VALUES (?,'courier')`, id)
	}
	admin, rider, next := guardToken(t, a, 3), guardToken(t, a, 4), guardToken(t, a, 5)
	oid, tid := guardPaidOrder(t, a, database, buyer, "guard_delivery_order_00001")
	path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
	adminPath := fmt.Sprintf("/api/v1/admin/delivery-tasks/%d", tid)
	t.Run("unassigned_privacy", func(t *testing.T) { guardStatus(t, guardRequest(a, rider, "GET", path, nil), 404) })
	t.Run("disabled_assignee", func(t *testing.T) {
		guardSQL(t, database, `UPDATE users SET status='DISABLED' WHERE id=5`)
		guardStatus(t, guardRequest(a, admin, "POST", adminPath+"/assign", map[string]any{"requestId": "guard_disabled_assign_0001", "assigneeId": "5", "expectedVersion": 1}), 422)
		guardSQL(t, database, `UPDATE users SET status='ACTIVE' WHERE id=5`)
	})
	claim := map[string]any{"requestId": "guard_claim_same_0000001", "expectedVersion": 1}
	t.Run("claim_concurrent_replays", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make([]*httptest.ResponseRecorder, 10)
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i] = guardRequest(a, rider, "POST", path+"/claim", claim) }(i)
		}
		wg.Wait()
		for _, w := range results {
			guardStatus(t, w, 200)
		}
		var version, count int
		if err := database.QueryRow(`SELECT version FROM delivery_tasks WHERE id=?`, tid).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if version != 2 || count != 1 {
			t.Fatalf("version=%d count=%d", version, count)
		}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/claim", map[string]any{"requestId": claim["requestId"], "expectedVersion": 2}), 409)
	})
	t.Run("reassignment_revokes_even_old_replay", func(t *testing.T) {
		payload := map[string]any{"requestId": "guard_reassign_original_001", "assigneeId": "5", "expectedVersion": 2}
		guardStatus(t, guardRequest(a, admin, "POST", adminPath+"/reassign", payload), 200)
		guardStatus(t, guardRequest(a, admin, "POST", adminPath+"/reassign", payload), 200)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/claim", claim), 403)
		guardStatus(t, guardRequest(a, rider, "GET", path, nil), 404)
	})
	pickup := map[string]any{"requestId": "guard_pickup_original_001", "expectedVersion": 3, "checked": true}
	t.Run("ready_checked_and_event_atomicity", func(t *testing.T) {
		guardStatus(t, guardRequest(a, next, "POST", path+"/pickup", pickup), 409)
		var version int64
		database.QueryRow(`SELECT version FROM orders WHERE id=?`, oid).Scan(&version)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid), map[string]any{"action": "ACCEPT", "expectedVersion": version}), 200)
		database.QueryRow(`SELECT version FROM orders WHERE id=?`, oid).Scan(&version)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid), map[string]any{"action": "READY", "expectedVersion": version}), 200)
		guardStatus(t, guardRequest(a, next, "POST", path+"/pickup", map[string]any{"requestId": "guard_pickup_unchecked_001", "expectedVersion": 3, "checked": false}), 422)
		guardSQL(t, database, `CREATE TRIGGER guard_reject_delivery_event BEFORE INSERT ON order_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test event failure'`)
		w := guardRequest(a, next, "POST", path+"/pickup", pickup)
		guardSQL(t, database, `DROP TRIGGER guard_reject_delivery_event`)
		guardStatus(t, w, 500)
		var state string
		database.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, tid).Scan(&state)
		if state != "ACCEPTED" {
			t.Fatal(state)
		}
		guardStatus(t, guardRequest(a, next, "POST", path+"/pickup", pickup), 200)
		guardStatus(t, guardRequest(a, next, "POST", path+"/pickup", pickup), 200)
	})
	guardStatus(t, guardRequest(a, next, "POST", path+"/start", map[string]any{"requestId": "guard_start_original_00001", "expectedVersion": 4}), 200)
	guardStatus(t, guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/urges", oid), map[string]any{"requestId": "guard_delivery_urge_00001", "reason": "test private urge"}), 201)
	t.Run("arrival_required_and_single_completion", func(t *testing.T) {
		guardStatus(t, guardRequest(a, buyer, "GET", fmt.Sprintf("/api/v1/orders/%d/receipt-code", oid), nil), 410)
		guardStatus(t, guardRequest(a, next, "POST", path+"/complete", map[string]any{"requestId": "guard_no_arrival_00001", "expectedVersion": 5}), 409)
		guardStatus(t, guardRequest(a, next, "POST", path+"/complete", map[string]any{"requestId": "guard_forged_arrival_001", "expectedVersion": 5, "arrivalToken": "forged.proof"}), 422)
		arrivalToken := receiptReplacementToken(t, a, database, admin, next, tid)
		payload := map[string]any{"requestId": "guard_complete_original_001", "expectedVersion": 5, "arrivalToken": arrivalToken}
		var wg sync.WaitGroup
		results := make([]*httptest.ResponseRecorder, 10)
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i] = guardRequest(a, next, "POST", path+"/complete", payload) }(i)
		}
		wg.Wait()
		for _, w := range results {
			guardStatus(t, w, 200)
		}
		var used int
		database.QueryRow(`SELECT COUNT(*) FROM notifications WHERE order_id=? AND event_key IS NOT NULL`, oid).Scan(&used)
		if used != 1 {
			t.Fatalf("delivery notification written %d times", used)
		}
		var events int
		database.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=? AND event_type='DELIVERY_COMPLETE'`, oid).Scan(&events)
		if events != 1 {
			t.Fatalf("completion events %d", events)
		}
	})
	t.Run("completed_list_and_detail_redacted", func(t *testing.T) {
		for _, url := range []string{path, "/api/v1/courier/tasks?status=COMPLETED"} {
			w := guardRequest(a, next, "GET", url, nil)
			data := guardStatus(t, w, 200)
			check := func(item map[string]any) {
				t.Helper()
				if strings.Contains(fmt.Sprint(item["addressText"]), "101") || item["contactName"] != nil || item["phone"] != nil || item["latestUrge"] != nil || strings.Contains(fmt.Sprint(item["urges"]), "test private urge") {
					t.Fatalf("completed task contains private fields: %#v", item)
				}
			}
			if items, ok := data["data"].([]any); ok {
				if len(items) == 0 {
					t.Fatal("completed list unexpectedly empty")
				}
				for _, raw := range items {
					check(raw.(map[string]any))
				}
			} else {
				check(data)
			}
		}
	})
}

func TestMySQLUrgeAuditAndOutboxAtomicity(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	admin := guardToken(t, a, 3)
	first, _ := guardPaidOrder(t, a, database, buyer, "guard_urge_first_order_001")
	second, _ := guardPaidOrder(t, a, database, buyer, "guard_urge_second_order_001")
	t.Run("different_order_same_key_concurrent", func(t *testing.T) {
		results := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i, id := range []int64{first, second} {
			wg.Add(1)
			go func(i int, id int64) {
				defer wg.Done()
				results[i] = guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/urges", id), map[string]any{"requestId": "guard_cross_order_urge_001"})
			}(i, id)
		}
		wg.Wait()
		if !((results[0].Code == 201 && results[1].Code == 409) || (results[1].Code == 201 && results[0].Code == 409)) {
			t.Fatalf("HTTP %d %d", results[0].Code, results[1].Code)
		}
	})
	t.Run("outbox_multiworker_exactly_one_delivery", func(t *testing.T) {
		var id int64
		database.QueryRow(`SELECT id FROM outbox WHERE topic='URGE_CREATED' LIMIT 1`).Scan(&id)
		var wg sync.WaitGroup
		errs := make([]error, 10)
		for i := range errs {
			wg.Add(1)
			go func(i int) { defer wg.Done(); errs[i] = a.processUrgeOutbox(context.Background(), id) }(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var state string
		var attempts, notices int
		database.QueryRow(`SELECT status,attempts FROM outbox WHERE id=?`, id).Scan(&state, &attempts)
		database.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&notices)
		if state != "DONE" || attempts != 1 || notices != 1 {
			t.Fatalf("state=%s attempts=%d notifications=%d", state, attempts, notices)
		}
		if err := a.processUrgeOutbox(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("create_event_failure_rolls_back", func(t *testing.T) {
		oid, _ := guardPaidOrder(t, a, database, buyer, "guard_urge_event_order_001")
		guardSQL(t, database, `CREATE TRIGGER guard_reject_urge_event BEFORE INSERT ON order_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test event failure'`)
		w := guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/urges", oid), map[string]any{"requestId": "guard_urge_event_fail_0001"})
		guardSQL(t, database, `DROP TRIGGER guard_reject_urge_event`)
		guardStatus(t, w, 500)
		var count int
		database.QueryRow(`SELECT COUNT(*) FROM order_urges WHERE order_id=?`, oid).Scan(&count)
		if count != 0 {
			t.Fatal(count)
		}
	})
	t.Run("policy_audit_failure_rolls_back", func(t *testing.T) {
		before := guardStatus(t, guardRequest(a, admin, "GET", "/api/v1/admin/urge-policy", nil), 200)
		guardSQL(t, database, `CREATE TRIGGER guard_reject_urge_audit BEFORE INSERT ON audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test audit failure'`)
		w := guardRequest(a, admin, "PUT", "/api/v1/admin/urge-policy", map[string]any{"expectedVersion": before["version"], "cooldownSeconds": 600, "maxPerOrder": 3, "enabled": true})
		guardSQL(t, database, `DROP TRIGGER guard_reject_urge_audit`)
		guardStatus(t, w, 500)
		after := guardStatus(t, guardRequest(a, admin, "GET", "/api/v1/admin/urge-policy", nil), 200)
		if after["cooldownSeconds"] != before["cooldownSeconds"] || after["version"] != before["version"] {
			t.Fatalf("policy changed: %v -> %v", before, after)
		}
	})
	t.Run("notification_error_no_partial_delivery_then_retry", func(t *testing.T) {
		oid, _ := guardPaidOrder(t, a, database, buyer, "guard_notification_order_001")
		data := guardStatus(t, guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/urges", oid), map[string]any{"requestId": "guard_notice_fault_00001"}), 201)
		uid := data["urge"].(map[string]any)["id"].(string)
		var outbox int64
		database.QueryRow(`SELECT id FROM outbox WHERE topic='URGE_CREATED' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.urgeId'))=?`, uid).Scan(&outbox)
		guardSQL(t, database, `CREATE TRIGGER guard_reject_notification BEFORE INSERT ON notifications FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test notification failure'`)
		err := a.processUrgeOutbox(context.Background(), outbox)
		guardSQL(t, database, `DROP TRIGGER guard_reject_notification`)
		if err == nil {
			t.Fatal("fault not exercised")
		}
		var count int
		database.QueryRow(`SELECT COUNT(*) FROM notifications WHERE urge_id=?`, uid).Scan(&count)
		if count != 0 {
			t.Fatal(count)
		}
		guardSQL(t, database, `UPDATE outbox SET next_run_at=UTC_TIMESTAMP(6) WHERE id=?`, outbox)
		if err = a.processUrgeOutbox(context.Background(), outbox); err != nil {
			t.Fatal(err)
		}
		database.QueryRow(`SELECT COUNT(*) FROM notifications WHERE urge_id=?`, uid).Scan(&count)
		if count != 1 {
			t.Fatal(count)
		}
	})
}

// Keep this separate from the successful completion scenario: an exception
// after pickup must preserve physical custody, not reopen direct assignment.
func TestMySQLPickedUpExceptionCannotBeReassigned(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	for _, id := range []int64{4, 5} {
		guardSQL(t, database, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) SELECT ?,?,CONCAT('rider',?),password_hash,'ACTIVE',1 FROM users WHERE id=1`, id, fmt.Sprintf("1380000000%d", id), id)
		guardSQL(t, database, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (?,?,1,1)`, id, id)
		guardSQL(t, database, `INSERT INTO staff_roles(membership_id,role) VALUES (?,'courier')`, id)
	}
	admin, rider := guardToken(t, a, 3), guardToken(t, a, 4)
	oid, tid := guardPaidOrder(t, a, database, buyer, "guard_custody_exception_001")
	path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
	guardStatus(t, guardRequest(a, rider, "POST", path+"/claim", map[string]any{"requestId": "guard_custody_claim_0001", "expectedVersion": 1}), 200)
	for _, action := range []string{"ACCEPT", "READY"} {
		var version int64
		if err := database.QueryRow(`SELECT version FROM orders WHERE id=?`, oid).Scan(&version); err != nil {
			t.Fatal(err)
		}
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid), map[string]any{"action": action, "expectedVersion": version}), 200)
	}
	guardStatus(t, guardRequest(a, rider, "POST", path+"/pickup", map[string]any{"requestId": "guard_custody_pickup_0001", "expectedVersion": 2, "checked": true}), 200)
	guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions", map[string]any{"requestId": "guard_custody_exception_01", "expectedVersion": 3, "type": "OTHER", "note": "货物已取，需人工交接"}), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/reassign", tid), map[string]any{"requestId": "guard_custody_reassign_01", "expectedVersion": 4, "assigneeId": "5"}), 409)
	var assignee, version int64
	var status string
	if err := database.QueryRow(`SELECT assignee_id,status,version FROM delivery_tasks WHERE id=?`, tid).Scan(&assignee, &status, &version); err != nil {
		t.Fatal(err)
	}
	if assignee != 4 || status != "EXCEPTION" || version != 4 {
		t.Fatalf("custody lost: assignee=%d status=%s version=%d", assignee, status, version)
	}
}

func TestMySQLHandleUrgeWaitsForOrderBeforeLockingUrge(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	admin := guardToken(t, a, 3)
	oid, _ := guardPaidOrder(t, a, database, buyer, "guard_handle_lock_order_01")
	data := guardStatus(t, guardRequest(a, buyer, "POST", fmt.Sprintf("/api/v1/orders/%d/urges", oid), map[string]any{"requestId": "guard_handle_lock_urge_01"}), 201)
	uid := data["urge"].(map[string]any)["id"].(string)
	blocker, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var locked int64
	if err = blocker.QueryRow(`SELECT id FROM orders WHERE id=? FOR UPDATE`, oid).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- guardRequest(a, admin, "POST", "/api/v1/admin/urges/"+uid+"/handle", map[string]any{"expectedVersion": 1, "note": "lock-order regression"})
	}()
	// Observe an actual lock wait, not a sleep-based assumption about timing.
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		// PROCESSLIST exposes this application's own connections without the
		// global PROCESS privilege required by INNODB_TRX. The blocker already
		// owns order, so reaching either statement proves the handler is waiting.
		if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE DB=DATABASE() AND ID<>CONNECTION_ID() AND (INFO LIKE 'SELECT store_id FROM orders WHERE%' OR INFO LIKE 'INSERT INTO order_events%')`).Scan(&n); err != nil {
			blocker.Rollback()
			<-done
			t.Fatal(err)
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	probe, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		blocker.Rollback()
		<-done
		t.Fatal(err)
	}
	// A correctly ordered handler waiting for order must not already own urge.
	probeErr := probe.QueryRow(`SELECT id FROM order_urges WHERE id=? FOR UPDATE NOWAIT`, uid).Scan(&locked)
	probe.Rollback()
	blocker.Rollback()
	response := <-done
	if !waiting {
		t.Fatal("handler did not reach a verified order lock wait")
	}
	if probeErr != nil {
		t.Fatalf("handler held urge while waiting for order: %v", probeErr)
	}
	guardStatus(t, response, 200)
	// HANDLED replay must release its transaction before reading the result;
	// a one-connection pool would otherwise self-block on loadUrgeRecord.
	database.SetMaxOpenConns(1)
	request := httptest.NewRequest("POST", "/api/v1/admin/urges/"+uid+"/handle", bytes.NewBufferString(`{"expectedVersion":1}`))
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+admin)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	a.ServeHTTP(response, request)
	guardStatus(t, response, 200)
}
