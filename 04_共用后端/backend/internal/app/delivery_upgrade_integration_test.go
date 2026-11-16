//go:build integration

package app

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const courierBatchPath = "/api/v1/courier/tasks/batch-actions"

func upgradeFixture(t *testing.T) (*App, *sql.DB, string, string, string, string) {
	t.Helper()
	a, d, buyer, admin := merchantFixture(t)
	for _, id := range []int64{4, 5} {
		guardSQL(t, d, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) SELECT ?,?,CONCAT('rider',?),password_hash,'ACTIVE',1 FROM users WHERE id=1`, id, fmt.Sprintf("1380000000%d", id), id)
		guardSQL(t, d, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (?,?,1,1)`, id, id)
		guardSQL(t, d, `INSERT INTO staff_roles(membership_id,role) VALUES (?,'courier')`, id)
	}
	return a, d, buyer, admin, guardToken(t, a, 4), guardToken(t, a, 5)
}

func upgradePoint(t *testing.T, a *App, admin string, buildingID string) map[string]any {
	t.Helper()
	return guardStatus(t, guardRequest(a, admin, "POST", "/api/v1/admin/delivery-points", map[string]any{"buildingId": buildingID, "name": "测试楼入口取货架", "latitude": 31.2304, "longitude": 121.4737, "radiusMeters": 80, "active": true}), 200)
}

func upgradeArrival(t *testing.T, a *App, rider string, ids ...int64) string {
	t.Helper()
	tasks := []string{}
	for _, id := range ids {
		tasks = append(tasks, toStringID(id))
	}
	data := guardStatus(t, guardRequest(a, rider, "POST", "/api/v1/courier/arrivals", map[string]any{"taskIds": tasks, "latitude": 31.2304, "longitude": 121.4737, "accuracy": 10, "observedAt": a.now().UTC().Format(time.RFC3339Nano)}), 200)
	return data["arrivalToken"].(string)
}

func upgradeBatch(t *testing.T, d *sql.DB, key, action string, ids ...int64) map[string]any {
	t.Helper()
	items := []map[string]any{}
	for _, id := range ids {
		items = append(items, map[string]any{"taskId": toStringID(id), "expectedVersion": resumeTaskVersion(t, d, id)})
	}
	return map[string]any{"requestId": key, "action": action, "items": items}
}

func TestMySQLCourierUpgradeBatchRecovery(t *testing.T) {
	a, d, buyer, admin, rider, other := upgradeFixture(t)
	upgradePoint(t, a, admin, "1")
	orders, tasks := []int64{}, []int64{}
	for i := 0; i < 4; i++ {
		oid, tid := guardPaidOrder(t, a, d, buyer, fmt.Sprintf("upgrade_batch_order_%05d", i))
		orders = append(orders, oid)
		tasks = append(tasks, tid)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid), map[string]any{"action": "ACCEPT", "expectedVersion": resumeOrderVersion(t, d, oid)}), 200)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid), map[string]any{"action": "READY", "expectedVersion": resumeOrderVersion(t, d, oid)}), 200)
	}
	ledger := batchLedger(t, d)
	claim := upgradeBatch(t, d, "upgrade_claim_batch_00001", "CLAIM", tasks...)
	data := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, claim), 200)
	if data["successCount"] != float64(3) || data["failedCount"] != float64(1) {
		t.Fatalf("capacity not enforced: %#v", data)
	}
	var wg sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 6)
	for i := range responses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); responses[i] = guardRequest(a, rider, "POST", courierBatchPath, claim) }(i)
	}
	wg.Wait()
	for _, w := range responses {
		guardStatus(t, w, 200)
	}
	changed := upgradeBatch(t, d, "upgrade_claim_batch_00001", "PICKUP", tasks[:3]...)
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, changed), 409)
	guardStatus(t, guardRequest(a, other, "GET", courierBatchPath+"/"+data["batchId"].(string), nil), 404)
	guardStatus(t, guardRequest(a, rider, "GET", courierBatchPath+"?requestId=upgrade_claim_batch_00001", nil), 200)
	pickup := upgradeBatch(t, d, "upgrade_pickup_batch_0001", "PICKUP", tasks[:3]...)
	pickup["checked"] = true
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, pickup), 200)
	guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/arrival", tasks[0]), map[string]any{"latitude": 31.2304, "longitude": 121.4737, "accuracy": 10, "observedAt": a.now().UTC().Format(time.RFC3339Nano)}), 409)
	for _, tid := range tasks[:3] {
		guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/start", tid), map[string]any{"requestId": fmt.Sprintf("upgrade_start_task_%06d", tid), "expectedVersion": resumeTaskVersion(t, d, tid)}), 200)
	}
	arrival := upgradeArrival(t, a, rider, tasks[:3]...)
	complete := upgradeBatch(t, d, "upgrade_complete_batch_001", "COMPLETE", tasks[:3]...)
	complete["arrivalToken"] = arrival
	guardSQL(t, d, fmt.Sprintf(`CREATE TRIGGER upgrade_reject_notification BEFORE INSERT ON notifications FOR EACH ROW BEGIN IF NEW.order_id=%d THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='notification fault'; END IF; END`, orders[1]))
	w := guardRequest(a, rider, "POST", courierBatchPath, complete)
	guardSQL(t, d, `DROP TRIGGER upgrade_reject_notification`)
	partial := guardStatus(t, w, 500)
	if partial["successCount"] != float64(1) || partial["pendingCount"] != float64(2) {
		t.Fatalf("unexpected partial: %#v", partial)
	}
	var state string
	if err := d.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, tasks[1]).Scan(&state); err != nil || state != "DELIVERING" {
		t.Fatalf("notification failure must roll back delivery: %s %v", state, err)
	}
	batchID := partial["batchId"].(string)
	now := a.now().UTC().Add(4 * time.Minute)
	a.now = func() time.Time { return now }
	expiredResponse := guardRequest(a, rider, "POST", courierBatchPath+"/"+batchID+"/resume", map[string]any{})
	expired := guardStatus(t, expiredResponse, 409)
	if !strings.Contains(expiredResponse.Body.String(), "ARRIVAL_EXPIRED") || expired["successCount"] != float64(1) {
		t.Fatalf("expiry lost progress: %s", expiredResponse.Body)
	}
	fresh := upgradeArrival(t, a, rider, tasks[1:3]...)
	finished := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath+"/"+batchID+"/resume", map[string]any{"arrivalToken": fresh}), 200)
	if finished["successCount"] != float64(3) || finished["pendingCount"] != float64(0) {
		t.Fatalf("recovery failed: %#v", finished)
	}
	// Successful replay ignores the expired proof and never notifies twice.
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, complete), 200)
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath+"/"+batchID+"/resume", map[string]any{"arrivalToken": arrival}), 200)
	var notifications, events, active int
	if err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE event_key LIKE 'DELIVERED:%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM order_events WHERE event_type='DELIVERY_COMPLETE'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE courier_id=4 AND store_id=1`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if notifications != 3 || events != 3 || active != 0 {
		t.Fatalf("duplicates/capacity: inbox=%d events=%d active=%d", notifications, events, active)
	}
	if after := batchLedger(t, d); ledger != after {
		t.Fatalf("delivery changed payment/stock ledger %s -> %s", ledger, after)
	}
}

func TestMySQLCourierUpgradePointGuards(t *testing.T) {
	a, d, buyer, admin, rider, other := upgradeFixture(t)
	_, tid := resumeDeliveringOrder(t, a, d, admin, rider, buyer, "upgrade_legacy_point_0001")
	arrivalBody := map[string]any{"latitude": 31.2304, "longitude": 121.4737, "accuracy": 10, "observedAt": a.now().UTC().Format(time.RFC3339Nano)}
	path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
	w := guardRequest(a, rider, "POST", path+"/arrival", arrivalBody)
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "DELIVERY_POINT_REQUIRED") {
		t.Fatal(w.Body.String())
	}
	point := upgradePoint(t, a, admin, "1")
	guardStatus(t, guardRequest(a, other, "POST", path+"/arrival", arrivalBody), 403)
	arrivalBody["latitude"] = 31.24
	w = guardRequest(a, rider, "POST", path+"/arrival", arrivalBody)
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "OUTSIDE_DELIVERY_POINT") {
		t.Fatal(w.Body.String())
	}
	arrivalBody["latitude"] = 31.2304
	arrivalBody["accuracy"] = 100
	w = guardRequest(a, rider, "POST", path+"/arrival", arrivalBody)
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "LOCATION_INACCURATE") {
		t.Fatal(w.Body.String())
	}
	arrivalBody["accuracy"] = 10
	arrivalBody["observedAt"] = a.now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	w = guardRequest(a, rider, "POST", path+"/arrival", arrivalBody)
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "LOCATION_STALE") {
		t.Fatal(w.Body.String())
	}
	token := upgradeArrival(t, a, rider, tid)
	var frozen string
	if err := d.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(o.address_json,'$.deliveryPoint.id')) FROM orders o JOIN delivery_tasks t ON t.order_id=o.id WHERE t.id=?`, tid).Scan(&frozen); err != nil || frozen != point["id"] {
		t.Fatalf("historical binding not persisted: %s %v", frozen, err)
	}
	pointBody := map[string]any{"buildingId": "1", "name": "新配置取货架", "latitude": 31.2304, "longitude": 121.4737, "radiusMeters": 80, "active": true, "version": 1, "expectedVersion": 1}
	guardStatus(t, guardRequest(a, admin, "PUT", "/api/v1/admin/delivery-points/"+point["id"].(string), pointBody), 200)
	guardStatus(t, guardRequest(a, admin, "PUT", "/api/v1/admin/delivery-points/"+point["id"].(string), pointBody), 409)
	_, second := resumeDeliveringOrder(t, a, d, admin, rider, buyer, "upgrade_new_point_00001")
	mixed := map[string]any{"taskIds": []string{toStringID(tid), toStringID(second)}, "latitude": 31.2304, "longitude": 121.4737, "accuracy": 10, "observedAt": a.now().UTC().Format(time.RFC3339Nano)}
	w = guardRequest(a, rider, "POST", "/api/v1/courier/arrivals", mixed)
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "MIXED_DELIVERY_POINTS") {
		t.Fatal(w.Body.String())
	}
	complete := map[string]any{"requestId": "upgrade_single_complete_001", "expectedVersion": resumeTaskVersion(t, d, tid), "arrivalToken": token}
	guardStatus(t, guardRequest(a, rider, "POST", path+"/complete", complete), 200)
	now := a.now().Add(4 * time.Minute)
	a.now = func() time.Time { return now }
	guardStatus(t, guardRequest(a, rider, "POST", path+"/complete", complete), 200)
	// A token for the first task cannot complete the second task.
	newToken := upgradeArrival(t, a, rider, second)
	claims, e := a.parseArrival(newToken)
	if e != nil {
		t.Fatal(e)
	}
	claims.Tasks[0].TaskID = tid
	w = guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/complete", second), map[string]any{"requestId": "upgrade_wrong_task_token_001", "expectedVersion": resumeTaskVersion(t, d, second), "arrivalToken": a.signArrival(*claims)})
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ARRIVAL_TASK_MISMATCH") {
		t.Fatal(w.Body.String())
	}
	// A valid proof loses authority immediately when the assignment generation changes.
	guardSQL(t, d, `UPDATE delivery_tasks SET assignment_generation=assignment_generation+1 WHERE id=?`, second)
	w = guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/complete", second), map[string]any{"requestId": "upgrade_old_generation_001", "expectedVersion": resumeTaskVersion(t, d, second), "arrivalToken": newToken})
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ARRIVAL_TASK_MISMATCH") {
		t.Fatal(w.Body.String())
	}
}

func TestMySQLCourierUpgradeReassignmentAndBounds(t *testing.T) {
	a, d, buyer, admin, rider, other := upgradeFixture(t)
	_, tid := guardPaidOrder(t, a, d, buyer, "upgrade_reassignment_0001")
	claim := upgradeBatch(t, d, "upgrade_claim_reassign_001", "CLAIM", tid)
	batch := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, claim), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/reassign", tid), map[string]any{"requestId": "upgrade_admin_reassign_001", "expectedVersion": resumeTaskVersion(t, d, tid), "assigneeId": "5"}), 200)
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, claim), 403)
	guardStatus(t, guardRequest(a, other, "POST", courierBatchPath+"/"+batch["batchId"].(string)+"/resume", map[string]any{}), 404)
	large := upgradeBatch(t, d, "upgrade_oversized_batch_001", "CLAIM", tid)
	items := []map[string]any{}
	for i := 0; i < 21; i++ {
		items = append(items, map[string]any{"taskId": fmt.Sprint(i + 1), "expectedVersion": 1})
	}
	large["items"] = items
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, large), 422)
	// Turning off the role revokes both new work and persisted replay.
	guardSQL(t, d, `DELETE FROM staff_roles WHERE membership_id=4 AND role='courier'`)
	guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, claim), 403)
}

func TestMySQLCourierUpgradeConcurrentClaims(t *testing.T) {
	t.Run("different_couriers_same_task", func(t *testing.T) {
		a, d, buyer, _, rider, other := upgradeFixture(t)
		_, task := guardPaidOrder(t, a, d, buyer, "upgrade_contested_order_001")
		bodies := []map[string]any{
			upgradeBatch(t, d, "upgrade_contested_rider4_001", "CLAIM", task),
			upgradeBatch(t, d, "upgrade_contested_rider5_001", "CLAIM", task),
		}
		tokens := []string{rider, other}
		start := make(chan struct{})
		responses := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i := range responses {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				responses[i] = guardRequest(a, tokens[i], "POST", courierBatchPath, bodies[i])
			}(i)
		}
		close(start)
		wg.Wait()
		success, failed := 0, 0
		for _, response := range responses {
			result := guardStatus(t, response, 200)
			success += int(result["successCount"].(float64))
			failed += int(result["failedCount"].(float64))
		}
		var assigned, count, events int
		if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_tasks WHERE id=? AND assignee_id IN (4,5) AND status='ACCEPTED'`, task).Scan(&assigned); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT SUM(active_task_count) FROM courier_capacities WHERE store_id=1`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT COUNT(*) FROM order_events WHERE event_type='DELIVERY_CLAIM'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if success != 1 || failed != 1 || assigned != 1 || count != 1 || events != 1 {
			t.Fatalf("contested claim double-applied: success=%d failed=%d assigned=%d capacity=%d events=%d", success, failed, assigned, count, events)
		}
	})
	t.Run("different_batches_same_courier_capacity", func(t *testing.T) {
		a, d, buyer, _, rider, _ := upgradeFixture(t)
		tasks := []int64{}
		for i := 0; i < 6; i++ {
			_, task := guardPaidOrder(t, a, d, buyer, fmt.Sprintf("upgrade_capacity_order_%03d", i))
			tasks = append(tasks, task)
		}
		bodies := []map[string]any{
			upgradeBatch(t, d, "upgrade_capacity_batch_A_001", "CLAIM", tasks[:3]...),
			upgradeBatch(t, d, "upgrade_capacity_batch_B_001", "CLAIM", tasks[3:]...),
		}
		start := make(chan struct{})
		responses := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i := range responses {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				responses[i] = guardRequest(a, rider, "POST", courierBatchPath, bodies[i])
			}(i)
		}
		close(start)
		wg.Wait()
		success, failed := 0, 0
		for _, response := range responses {
			result := guardStatus(t, response, 200)
			success += int(result["successCount"].(float64))
			failed += int(result["failedCount"].(float64))
		}
		var assigned, count, events int
		if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_tasks WHERE assignee_id=4 AND status='ACCEPTED'`).Scan(&assigned); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT COUNT(*) FROM order_events WHERE event_type='DELIVERY_CLAIM'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if success != 3 || failed != 3 || assigned != 3 || count != 3 || events != 3 {
			t.Fatalf("capacity exceeded under parallel batches: success=%d failed=%d assigned=%d capacity=%d events=%d", success, failed, assigned, count, events)
		}
	})
}

func TestMySQLCourierUpgradeCannotChangeBatchPoint(t *testing.T) {
	a, d, buyer, admin, rider, _ := upgradeFixture(t)
	point := upgradePoint(t, a, admin, "1")
	_, first := resumeDeliveringOrder(t, a, d, admin, rider, buyer, "upgrade_point_scope_first")
	firstToken := upgradeArrival(t, a, rider, first)
	guardStatus(t, guardRequest(a, admin, "PUT", "/api/v1/admin/delivery-points/"+point["id"].(string), map[string]any{"buildingId": "1", "name": "另一配置取货架", "latitude": 31.2304, "longitude": 121.4737, "radiusMeters": 80, "active": true, "version": 1}), 200)
	_, second := resumeDeliveringOrder(t, a, d, admin, rider, buyer, "upgrade_point_scope_second")
	secondToken := upgradeArrival(t, a, rider, second)
	body := upgradeBatch(t, d, "upgrade_point_scope_batch", "COMPLETE", first, second)
	body["arrivalToken"] = firstToken
	partial := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, body), 409)
	if partial["successCount"] != float64(1) || partial["pendingCount"] != float64(1) {
		t.Fatalf("unexpected partial: %#v", partial)
	}
	w := guardRequest(a, rider, "POST", courierBatchPath+"/"+partial["batchId"].(string)+"/resume", map[string]any{"arrivalToken": secondToken})
	guardStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ARRIVAL_POINT_MISMATCH") {
		t.Fatal(w.Body.String())
	}
	var status string
	if err := d.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, second).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "DELIVERING" {
		t.Fatalf("cross-point recovery changed task to %s", status)
	}
	var notifications int
	if err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE event_key LIKE 'DELIVERED:%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("cross-point recovery sent extra notification: %d", notifications)
	}
}
