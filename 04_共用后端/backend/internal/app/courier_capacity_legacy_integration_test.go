//go:build integration

package app

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Model rows imported by an older seed: assigned tasks exist but no capacity
// row has ever been created. Orders/payments still use the real application flow.
func legacyCapacityTask(t *testing.T, a *App, d *sql.DB, buyer, key, state string) int64 {
	t.Helper()
	orderID, taskID := guardPaidOrder(t, a, d, buyer, key)
	guardSQL(t, d, `UPDATE delivery_tasks SET assignee_id=4,status=?,assignment_generation=1 WHERE id=?`, state, taskID)
	if state == "DELIVERING" {
		guardSQL(t, d, `UPDATE delivery_tasks SET picked_at=UTC_TIMESTAMP(6),started_at=UTC_TIMESTAMP(6) WHERE id=?`, taskID)
		guardSQL(t, d, `UPDATE orders SET status='DELIVERING',delivery_status='DELIVERING' WHERE id=?`, orderID)
	}
	return taskID
}

func TestMySQLCourierLegacyCapacityConcurrentClaims(t *testing.T) {
	a, d, buyer, _, rider, _ := upgradeFixture(t)
	legacyCapacityTask(t, a, d, buyer, "legacy_capacity_delivering", "DELIVERING")
	legacyCapacityTask(t, a, d, buyer, "legacy_capacity_exception", "EXCEPTION")
	legacyCapacityTask(t, a, d, buyer, "legacy_capacity_completed", "COMPLETED")
	legacyCapacityTask(t, a, d, buyer, "legacy_capacity_cancelled", "CANCELLED")
	capacity := guardStatus(t, guardRequest(a, rider, "GET", "/api/v1/courier/capacity", nil), 200)
	if capacity["activeTaskCount"] != float64(2) || capacity["taskLimit"] != float64(3) || capacity["online"] != true {
		t.Fatalf("legacy capacity must include exception and exclude terminal tasks: %#v", capacity)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("read-only capacity should not create rows: rows=%d error=%v", rows, err)
	}
	tasks := []int64{}
	for i := 0; i < 2; i++ {
		_, task := guardPaidOrder(t, a, d, buyer, fmt.Sprintf("legacy_capacity_open_%04d", i))
		tasks = append(tasks, task)
	}
	bodies := []map[string]any{
		upgradeBatch(t, d, "legacy_capacity_parallel_A", "CLAIM", tasks[0]),
		upgradeBatch(t, d, "legacy_capacity_parallel_B", "CLAIM", tasks[1]),
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
	var stored, actual int
	if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_tasks WHERE store_id=1 AND assignee_id=4 AND status IN ('ACCEPTED','PICKED_UP','DELIVERING','EXCEPTION')`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if success != 1 || failed != 1 || stored != 3 || actual != 3 {
		t.Fatalf("old tasks were omitted during parallel initialization: success=%d failed=%d stored=%d actual=%d", success, failed, stored, actual)
	}
	// A row incorrectly initialized by a previous version must not permit a new
	// task either: the serialized mutation checks actual active tasks again.
	guardSQL(t, d, `UPDATE courier_capacities SET active_task_count=0 WHERE store_id=1 AND courier_id=4`)
	_, another := guardPaidOrder(t, a, d, buyer, "legacy_capacity_stale_candidate")
	result := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, upgradeBatch(t, d, "legacy_capacity_stale_reject", "CLAIM", another)), 200)
	if result["successCount"] != float64(0) || result["failedCount"] != float64(1) {
		t.Fatalf("stale row admitted extra task: %#v", result)
	}
	capacity = guardStatus(t, guardRequest(a, rider, "GET", "/api/v1/courier/capacity", nil), 200)
	if capacity["activeTaskCount"] != float64(3) {
		t.Fatalf("capacity display trusted stale row: %#v", capacity)
	}
}

func TestMySQLCourierLegacyCapacityComplete(t *testing.T) {
	a, d, buyer, admin, rider, _ := upgradeFixture(t)
	upgradePoint(t, a, admin, "1")
	task := legacyCapacityTask(t, a, d, buyer, "legacy_capacity_complete_task", "DELIVERING")
	capacity := guardStatus(t, guardRequest(a, rider, "GET", "/api/v1/courier/capacity", nil), 200)
	if capacity["activeTaskCount"] != float64(1) {
		t.Fatalf("missing capacity row hid task: %#v", capacity)
	}
	arrival := upgradeArrival(t, a, rider, task)
	body := map[string]any{"requestId": "legacy_capacity_complete_once", "expectedVersion": resumeTaskVersion(t, d, task), "arrivalToken": arrival}
	path := fmt.Sprintf("/api/v1/courier/tasks/%d/complete", task)
	guardStatus(t, guardRequest(a, rider, "POST", path, body), 200)
	guardStatus(t, guardRequest(a, rider, "POST", path, body), 200)
	var active, notifications int
	if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE event_key LIKE 'DELIVERED:%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if active != 0 || notifications != 1 {
		t.Fatalf("legacy completion double-subtracted or notified: active=%d notifications=%d", active, notifications)
	}
}

func TestMySQLCourierSeedCapacity(t *testing.T) {
	d := refundTestDatabase(t) // validates the dedicated *_test DSN before cleaning
	cleanDatabase(t, d)
	goBinary := "go"
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	goPath := filepath.Join(runtime.GOROOT(), "bin", goBinary)
	migrations, err := filepath.Abs("../../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		if run == 1 {
			guardSQL(t, d, `UPDATE courier_capacities SET active_task_count=0 WHERE store_id=1 AND courier_id=4`)
		}
		cmd := exec.Command(goPath, "run", "../../cmd/seed")
		cmd.Env = append(os.Environ(), "APP_ENV=test", "MYSQL_DSN="+os.Getenv("TEST_MYSQL_DSN"), "MIGRATIONS_DIR="+migrations, "STORE_ID=1", "PAYMENT_MODE=simulated")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed run %d failed: %v\n%s", run+1, err, output)
		}
		var stored, actual, total int
		if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_tasks WHERE store_id=1 AND assignee_id=4 AND status IN ('ACCEPTED','PICKED_UP','DELIVERING','EXCEPTION')`).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_tasks WHERE store_id=1 AND assignee_id=4`).Scan(&total); err != nil {
			t.Fatal(err)
		}
		if stored != 1 || actual != 1 || total != 5 {
			t.Fatalf("seed run %d capacity mismatch: stored=%d actual=%d total=%d", run+1, stored, actual, total)
		}
	}
}

func TestMySQLCourierLegacyCapacityOverloadRecovery(t *testing.T) {
	a, d, buyer, admin, rider, _ := upgradeFixture(t)
	upgradePoint(t, a, admin, "1")
	tasks := []int64{}
	for i := 0; i < 5; i++ {
		tasks = append(tasks, legacyCapacityTask(t, a, d, buyer, fmt.Sprintf("legacy_overloaded_task_%03d", i), "DELIVERING"))
	}
	capacity := guardStatus(t, guardRequest(a, rider, "GET", "/api/v1/courier/capacity", nil), 200)
	if capacity["activeTaskCount"] != float64(5) || capacity["taskLimit"] != float64(3) {
		t.Fatalf("overload must remain visible: %#v", capacity)
	}
	_, candidate := guardPaidOrder(t, a, d, buyer, "legacy_overload_new_candidate")
	result := guardStatus(t, guardRequest(a, rider, "POST", courierBatchPath, upgradeBatch(t, d, "legacy_overload_claim_rejected", "CLAIM", candidate)), 200)
	if result["successCount"] != float64(0) || result["failedCount"] != float64(1) {
		t.Fatalf("overloaded courier accepted another task: %#v", result)
	}
	arrival := upgradeArrival(t, a, rider, tasks[:2]...)
	for i, task := range tasks[:2] {
		guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/complete", task), map[string]any{"requestId": fmt.Sprintf("legacy_overload_complete_%03d", i), "expectedVersion": resumeTaskVersion(t, d, task), "arrivalToken": arrival}), 200)
		var stored int
		if err := d.QueryRow(`SELECT active_task_count FROM courier_capacities WHERE store_id=1 AND courier_id=4`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != 4-i {
			t.Fatalf("overload release blocked or miscounted: got=%d want=%d", stored, 4-i)
		}
		capacity = guardStatus(t, guardRequest(a, rider, "GET", "/api/v1/courier/capacity", nil), 200)
		if capacity["activeTaskCount"] != float64(4-i) {
			t.Fatalf("overload display did not follow release: %#v", capacity)
		}
	}
	// Re-applying the migration is safe, including after its new constraint was
	// already added, and the lower-bound invariants remain enforced by MySQL.
	body, err := os.ReadFile("../../../db/migrations/0012_courier_capacity_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(string(body)); err != nil {
		t.Fatalf("capacity migration not restart safe: %v", err)
	}
	if _, err := d.Exec(`UPDATE courier_capacities SET active_task_count=-1 WHERE store_id=1 AND courier_id=4`); err == nil {
		t.Fatal("negative capacity count accepted")
	}
	if _, err := d.Exec(`UPDATE courier_capacities SET task_limit=0 WHERE store_id=1 AND courier_id=4`); err == nil {
		t.Fatal("nonpositive task limit accepted")
	}
}
