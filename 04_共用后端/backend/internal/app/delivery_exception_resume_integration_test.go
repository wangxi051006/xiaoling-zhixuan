//go:build integration

package app

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func resumeTaskVersion(t *testing.T, database *sql.DB, taskID int64) int64 {
	t.Helper()
	var version int64
	if err := database.QueryRow(`SELECT version FROM delivery_tasks WHERE id=?`, taskID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func resumeOrderVersion(t *testing.T, database *sql.DB, orderID int64) int64 {
	t.Helper()
	var version int64
	if err := database.QueryRow(`SELECT version FROM orders WHERE id=?`, orderID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// resumeDeliveringOrder drives a paid order to DELIVERING (accepted -> ready ->
// assigned -> picked up -> started) and returns the task id.
func resumeDeliveringOrder(t *testing.T, a *App, database *sql.DB, admin, rider, buyer, key string) (int64, int64) {
	t.Helper()
	oid, tid := guardPaidOrder(t, a, database, buyer, key)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
		map[string]any{"action": "ACCEPT", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
		map[string]any{"action": "READY", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/assign", tid),
		map[string]any{"requestId": "resume_assign_" + key, "assigneeId": "4", "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
	guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/pickup", tid),
		map[string]any{"requestId": "resume_pickup_" + key, "expectedVersion": resumeTaskVersion(t, database, tid), "checked": true}), 200)
	guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/start", tid),
		map[string]any{"requestId": "resume_start_" + key, "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
	return oid, tid
}

func TestMySQLDeliveryExceptionResume(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	for _, id := range []int64{4, 5} {
		guardSQL(t, database, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) SELECT ?,?,CONCAT('rider',?),password_hash,'ACTIVE',1 FROM users WHERE id=1`, id, fmt.Sprintf("1380000000%d", id), id)
		guardSQL(t, database, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (?,?,1,1)`, id, id)
		guardSQL(t, database, `INSERT INTO staff_roles(membership_id,role) VALUES (?,'courier')`, id)
	}
	admin, rider, stranger := guardToken(t, a, 3), guardToken(t, a, 4), guardToken(t, a, 5)

	t.Run("picked_up_exception_resumes_to_delivering", func(t *testing.T) {
		oid, tid := resumeDeliveringOrder(t, a, database, admin, rider, buyer, "resume_case_a_order_0001")
		path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
		adminPath := fmt.Sprintf("/api/v1/admin/delivery-tasks/%d", tid)

		var goods, total int64
		if err := database.QueryRow(`SELECT goods_cents,total_cents FROM orders WHERE id=?`, oid).Scan(&goods, &total); err != nil {
			t.Fatal(err)
		}
		var movementsBefore int
		database.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE ref_id=?`, fmt.Sprint(oid)).Scan(&movementsBefore)

		guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions",
			map[string]any{"requestId": "resume_exception_a_001", "expectedVersion": resumeTaskVersion(t, database, tid),
				"type": "CUSTOMER_UNREACHABLE", "note": "买家电话无人接听"}), 200)
		var status, reason string
		var exceptionAt sql.NullTime
		if err := database.QueryRow(`SELECT status,COALESCE(exception_reason,''),exception_at FROM delivery_tasks WHERE id=?`, tid).Scan(&status, &reason, &exceptionAt); err != nil {
			t.Fatal(err)
		}
		if status != "EXCEPTION" || reason != "CUSTOMER_UNREACHABLE" || !exceptionAt.Valid {
			t.Fatalf("exception not recorded: %s %s %v", status, reason, exceptionAt)
		}
		var exceptionEvents, exceptionAudits int
		database.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=? AND event_type='DELIVERY_EXCEPTION'`, oid).Scan(&exceptionEvents)
		database.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE object_type='DELIVERY_TASK' AND object_id=? AND action='DELIVERY_EXCEPTION'`, fmt.Sprint(tid)).Scan(&exceptionAudits)
		if exceptionEvents != 1 || exceptionAudits != 1 {
			t.Fatalf("exception evidence events=%d audits=%d", exceptionEvents, exceptionAudits)
		}

		// Completion before recovery must be refused with an actionable code.
		w := guardRequest(a, rider, "POST", path+"/complete",
			map[string]any{"requestId": "resume_early_complete_001", "expectedVersion": resumeTaskVersion(t, database, tid), "arrivalToken": "invalid-before-recovery"})
		if w.Code != 409 || !containsCode(w, "TASK_EXCEPTION") {
			t.Fatalf("complete on exception task: %d %s", w.Code, w.Body)
		}

		// Authorization, validation and CAS before a successful resume.
		guardStatus(t, guardRequest(a, stranger, "POST", adminPath+"/resume",
			map[string]any{"requestId": "resume_admin_by_courier_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "x"}), 403)
		guardStatus(t, guardRequest(a, stranger, "POST", path+"/resume",
			map[string]any{"requestId": "resume_foreign_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "x"}), 403)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume",
			map[string]any{"requestId": "resume_nonote_001", "expectedVersion": resumeTaskVersion(t, database, tid)}), 422)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume",
			map[string]any{"requestId": "resume_stale_001", "expectedVersion": 1, "note": "x"}), 409)

		resume := map[string]any{"requestId": "resume_success_a_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "联系上买家，继续配送"}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", resume), 200)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", resume), 200)

		var resumedVersion int64
		var taskStatus, deliveryStatus string
		var resolvedBy sql.NullInt64
		if err := database.QueryRow(`SELECT status,version,exception_resolved_by FROM delivery_tasks WHERE id=?`, tid).Scan(&taskStatus, &resumedVersion, &resolvedBy); err != nil {
			t.Fatal(err)
		}
		database.QueryRow(`SELECT delivery_status FROM orders WHERE id=?`, oid).Scan(&deliveryStatus)
		if taskStatus != "DELIVERING" || deliveryStatus != "DELIVERING" || !resolvedBy.Valid || resolvedBy.Int64 != 4 {
			t.Fatalf("resume state task=%s delivery=%s resolvedBy=%v", taskStatus, deliveryStatus, resolvedBy)
		}
		var resumeEvents int
		database.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=? AND event_type='DELIVERY_RESUME'`, oid).Scan(&resumeEvents)
		if resumeEvents != 1 {
			t.Fatalf("resume events=%d (idempotent replay must not duplicate)", resumeEvents)
		}

		// A resumed task can be signed normally and the money/stock did not move.
		arrivalToken := receiptReplacementToken(t, a, database, admin, rider, tid)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/complete",
			map[string]any{"requestId": "resume_complete_001", "expectedVersion": resumedVersion, "arrivalToken": arrivalToken}), 200)
		var orderStatus, payStatus string
		var goodsAfter, totalAfter int64
		database.QueryRow(`SELECT status,payment_status,goods_cents,total_cents FROM orders WHERE id=?`, oid).Scan(&orderStatus, &payStatus, &goodsAfter, &totalAfter)
		if orderStatus != "COMPLETED" || payStatus != "PAID" || goodsAfter != goods || totalAfter != total {
			t.Fatalf("post-complete state=%s pay=%s goods=%d->%d total=%d->%d", orderStatus, payStatus, goods, goodsAfter, total, totalAfter)
		}
		var movementsAfter int
		database.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE ref_id=?`, fmt.Sprint(oid)).Scan(&movementsAfter)
		if movementsAfter != movementsBefore {
			t.Fatalf("resume/complete changed stock movements: %d -> %d", movementsBefore, movementsAfter)
		}
	})

	t.Run("unpicked_exception_resumes_to_accepted", func(t *testing.T) {
		oid, tid := guardPaidOrder(t, a, database, buyer, "resume_b_order_001")
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
			map[string]any{"action": "ACCEPT", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
			map[string]any{"action": "READY", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
		path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/assign", tid),
			map[string]any{"requestId": "resume_b_assign_001", "assigneeId": "4", "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions",
			map[string]any{"requestId": "resume_b_exception_001", "expectedVersion": resumeTaskVersion(t, database, tid),
				"type": "ADDRESS_ISSUE", "note": "门禁无法进入"}), 200)

		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume",
			map[string]any{"requestId": "resume_b_resume_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "商家重新联系买家"}), 200)
		var taskStatus, deliveryStatus string
		database.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, tid).Scan(&taskStatus)
		database.QueryRow(`SELECT delivery_status FROM orders WHERE id=?`, oid).Scan(&deliveryStatus)
		if taskStatus != "ACCEPTED" || deliveryStatus != "ASSIGNED" {
			t.Fatalf("unpicked resume task=%s delivery=%s", taskStatus, deliveryStatus)
		}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/pickup",
			map[string]any{"requestId": "resume_b_pickup_001", "expectedVersion": resumeTaskVersion(t, database, tid), "checked": true}), 200)
	})
}

func containsCode(w *httptest.ResponseRecorder, code string) bool {
	return strings.Contains(w.Body.String(), `"`+code+`"`)
}
