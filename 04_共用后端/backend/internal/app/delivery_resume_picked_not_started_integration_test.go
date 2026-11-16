//go:build integration

package app

import (
	"database/sql"
	"fmt"
	"testing"
)

// resumePickedOrder drives an order to "picked up but not started": the state the
// independent review found un-recoverable (order READY, task EXCEPTION, picked_at set).
func resumePickedOrder(t *testing.T, a *App, database *sql.DB, admin, rider, buyer, key string) (int64, int64) {
	t.Helper()
	oid, tid := guardPaidOrder(t, a, database, buyer, key)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
		map[string]any{"action": "ACCEPT", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/actions", oid),
		map[string]any{"action": "READY", "expectedVersion": resumeOrderVersion(t, database, oid)}), 200)
	guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/assign", tid),
		map[string]any{"requestId": "resume_ns_assign_" + key, "assigneeId": "4", "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
	guardStatus(t, guardRequest(a, rider, "POST", fmt.Sprintf("/api/v1/courier/tasks/%d/pickup", tid),
		map[string]any{"requestId": "resume_ns_pickup_" + key, "expectedVersion": resumeTaskVersion(t, database, tid), "checked": true}), 200)
	return oid, tid
}

func TestMySQLDeliveryResumePickedNotStarted(t *testing.T) {
	a, database, buyer := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	for _, id := range []int64{4, 5} {
		guardSQL(t, database, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) SELECT ?,?,CONCAT('rider',?),password_hash,'ACTIVE',1 FROM users WHERE id=1`, id, fmt.Sprintf("1380000000%d", id), id)
		guardSQL(t, database, `INSERT INTO staff_memberships(id,user_id,store_id,active) VALUES (?,?,1,1)`, id, id)
		guardSQL(t, database, `INSERT INTO staff_roles(membership_id,role) VALUES (?,'courier')`, id)
	}
	admin, rider := guardToken(t, a, 3), guardToken(t, a, 4)

	t.Run("courier_resume_then_start_and_complete", func(t *testing.T) {
		oid, tid := resumePickedOrder(t, a, database, admin, rider, buyer, "resume_ns_courier_order_001")
		path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
		var goods, total int64
		if err := database.QueryRow(`SELECT goods_cents,total_cents FROM orders WHERE id=?`, oid).Scan(&goods, &total); err != nil {
			t.Fatal(err)
		}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions",
			map[string]any{"requestId": "resume_ns_exc_0001", "expectedVersion": resumeTaskVersion(t, database, tid), "type": "CUSTOMER_UNREACHABLE", "note": "买家暂不在"}), 200)

		resume := map[string]any{"requestId": "resume_ns_courier_resume_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "买家已回来"}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", resume), 200)

		var taskStatus, orderStatus, deliveryStatus string
		if err := database.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, tid).Scan(&taskStatus); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT status,delivery_status FROM orders WHERE id=?`, oid).Scan(&orderStatus, &deliveryStatus); err != nil {
			t.Fatal(err)
		}
		if taskStatus != "PICKED_UP" || orderStatus != "READY" || deliveryStatus != "PICKED_UP" {
			t.Fatalf("resume state task=%s order=%s delivery=%s", taskStatus, orderStatus, deliveryStatus)
		}

		guardStatus(t, guardRequest(a, rider, "POST", path+"/start",
			map[string]any{"requestId": "resume_ns_start_0001", "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
		arrivalToken := receiptReplacementToken(t, a, database, admin, rider, tid)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/complete",
			map[string]any{"requestId": "resume_ns_complete_001", "expectedVersion": resumeTaskVersion(t, database, tid), "arrivalToken": arrivalToken}), 200)

		var g2, t2 int64
		database.QueryRow(`SELECT goods_cents,total_cents FROM orders WHERE id=?`, oid).Scan(&g2, &t2)
		if g2 != goods || t2 != total {
			t.Fatalf("amount changed %d/%d -> %d/%d", goods, total, g2, t2)
		}
	})

	t.Run("admin_resume_on_separate_task", func(t *testing.T) {
		oid, tid := resumePickedOrder(t, a, database, admin, rider, buyer, "resume_ns_admin_order_001")
		path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions",
			map[string]any{"requestId": "resume_ns_admin_exc_001", "expectedVersion": resumeTaskVersion(t, database, tid), "type": "OTHER", "note": "需要调度介入"}), 200)
		_ = oid

		guardStatus(t, guardRequest(a, admin, "POST", fmt.Sprintf("/api/v1/admin/delivery-tasks/%d/resume", tid),
			map[string]any{"requestId": "resume_ns_admin_resume_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "调度恢复"}), 200)
		var taskStatus string
		database.QueryRow(`SELECT status FROM delivery_tasks WHERE id=?`, tid).Scan(&taskStatus)
		if taskStatus != "PICKED_UP" {
			t.Fatalf("admin resume task=%s", taskStatus)
		}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/start",
			map[string]any{"requestId": "resume_ns_admin_start_001", "expectedVersion": resumeTaskVersion(t, database, tid)}), 200)
	})

	t.Run("stale_version_and_replay", func(t *testing.T) {
		_, tid := resumePickedOrder(t, a, database, admin, rider, buyer, "resume_ns_cas_order_0001")
		path := fmt.Sprintf("/api/v1/courier/tasks/%d", tid)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/exceptions",
			map[string]any{"requestId": "resume_ns_cas_exc_001", "expectedVersion": resumeTaskVersion(t, database, tid), "type": "OTHER", "note": "异常"}), 200)

		good := map[string]any{"requestId": "resume_ns_cas_resume_001", "expectedVersion": resumeTaskVersion(t, database, tid), "note": "恢复"}
		stale := map[string]any{"requestId": "resume_ns_cas_stale_001", "expectedVersion": 1, "note": "旧版本"}
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", stale), 409)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", good), 200)
		guardStatus(t, guardRequest(a, rider, "POST", path+"/resume", good), 200)
		var resumeEvents int
		database.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=(SELECT order_id FROM delivery_tasks WHERE id=?) AND event_type='DELIVERY_RESUME'`, tid).Scan(&resumeEvents)
		if resumeEvents != 1 {
			t.Fatalf("resume events=%d (replay must not duplicate)", resumeEvents)
		}
	})
}
