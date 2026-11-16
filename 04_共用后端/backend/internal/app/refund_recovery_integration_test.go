//go:build integration
// +build integration

package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"campusretail/internal/db"
)

func refundTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
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
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	cleanDatabase(t, database)
	seedIntegrationData(t, database)
	return database
}

func refundSQL(t *testing.T, database *sql.DB, query string, args ...any) int64 {
	t.Helper()
	result, err := database.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func refundOrderFixture(t *testing.T, database *sql.DB, key string, quantity int) (int64, int64) {
	t.Helper()
	order := refundSQL(t, database, `INSERT INTO orders(order_no,store_id,buyer_id,request_id,request_hash,status,payment_status,
		goods_cents,total_cents,address_json,expires_at,paid_at,customer_order_ordinal,is_first_order,is_demo)
		VALUES (?,1,1,?,REPEAT('a',64),'PAID','PAID',?,?,'{}',UTC_TIMESTAMP(6)+INTERVAL 1 HOUR,UTC_TIMESTAMP(6),1,0,1)`, key, key, quantity*650, quantity*650)
	item := refundSQL(t, database, `INSERT INTO order_items(order_id,sku_id,product_name,spec_name,unit_price_cents,quantity,goods_cents)
		VALUES (?,1,'测试薯片','70g',650,?,?)`, order, quantity, quantity*650)
	return order, item
}

func refundLegacyFixture(t *testing.T, database *sql.DB, order, item int64, key, status string, quantity int, amount int64) int64 {
	t.Helper()
	outNo := "U" + sha256Hex([]byte(key))[:24]
	id := refundSQL(t, database, `INSERT INTO refunds(order_id,after_sale_id,out_refund_no,amount_cents,status,reason,requested_by)
		VALUES (?,?,?,?,?,'legacy',1)`, order, key, outNo, amount, status)
	refundSQL(t, database, `INSERT INTO refund_items(refund_id,order_item_id,quantity,amount_cents) VALUES (?,?,?,?)`, id, item, quantity, quantity*650)
	if status == "SUCCESS" {
		refundSQL(t, database, `UPDATE order_items SET refunded_quantity=refunded_quantity+? WHERE id=?`, quantity, item)
		refundSQL(t, database, `UPDATE orders SET payment_status='REFUNDED' WHERE id=?`, order)
		refundSQL(t, database, `INSERT INTO refund_cash_ledger(refund_id,amount_cents,occurred_at,source) VALUES (?,?,UTC_TIMESTAMP(6),'SIMULATED')`, id, amount)
	}
	return id
}

func refundCounter(t *testing.T, database *sql.DB, item int64) (int, int) {
	t.Helper()
	var refunded, pending int
	if err := database.QueryRow(`SELECT refunded_quantity,pending_refund_quantity FROM order_items WHERE id=?`, item).Scan(&refunded, &pending); err != nil {
		t.Fatal(err)
	}
	return refunded, pending
}

func TestMySQLRefundLegacyRecoveryAndInterruptedMigration(t *testing.T) {
	database := refundTestDatabase(t)
	type legacy struct {
		order, item, refund int64
		key, status         string
	}
	var valid []legacy
	for _, status := range []string{"REQUESTED", "PROCESSING", "UNKNOWN", "SUCCESS"} {
		key := "legacy_refund_" + strings.ToLower(status) + "_00001"
		o, i := refundOrderFixture(t, database, "order_"+strings.ToLower(status), 1)
		r := refundLegacyFixture(t, database, o, i, key, status, 1, 650)
		valid = append(valid, legacy{o, i, r, key, status})
	}
	badOrder, badItem := refundOrderFixture(t, database, "legacy_bad_master", 2)
	badRefund := refundLegacyFixture(t, database, badOrder, badItem, "legacy_bad_master_00001", "REQUESTED", 1, 1300)
	overOrder, overItem := refundOrderFixture(t, database, "legacy_overallocated", 1)
	refundLegacyFixture(t, database, overOrder, overItem, "legacy_overallocated_00001", "REQUESTED", 1, 650)
	refundLegacyFixture(t, database, overOrder, overItem, "legacy_overallocated_00002", "PROCESSING", 1, 650)
	// Preserve a historical restock without a request key; it must still count
	// toward the cumulative limit after the old UNIQUE(refund_item_id) is removed.
	oldItem := firstItemID(t, database, valid[3].refund)
	oldRestock := refundSQL(t, database, `INSERT INTO refund_restocks(refund_item_id,lot_id,quantity) VALUES (?,1,1)`, oldItem)
	// Simulate the first ALTER of 0003 committed, the remaining ALTER absent,
	// plus partially completed 0004 DDL. Do not drop populated counters/records.
	refundSQL(t, database, `ALTER TABLE refunds DROP INDEX idx_refund_after_sale,DROP COLUMN request_hash`)
	refundSQL(t, database, `DELETE FROM schema_migrations WHERE version IN ('0003_refund_fix.sql','0004_refund_recovery.sql')`)
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	a := &App{db: database, tz: loadShanghai(), now: time.Now}
	for _, v := range valid {
		refunded, pending := refundCounter(t, database, v.item)
		if v.status == "SUCCESS" {
			if refunded != 1 || pending != 0 {
				t.Fatalf("successful legacy counters=%d/%d", refunded, pending)
			}
		} else if refunded != 0 || pending != 1 {
			t.Fatalf("%s legacy counters=%d/%d", v.status, refunded, pending)
		}
		items := []refundCreateItem{{OrderItemID: fmt.Sprint(v.item), Quantity: 1}}
		got, apiErr := a.createRefund(context.Background(), v.order, v.key, "legacy", items, "U", 1, 0, false, "")
		if apiErr != nil || got.ID != fmt.Sprint(v.refund) {
			t.Fatalf("%s legacy replay: %#v %#v", v.status, got, apiErr)
		}
		if _, apiErr := a.createRefund(context.Background(), v.order, v.key, "changed", items, "U", 1, 0, false, ""); apiErr == nil || apiErr.code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("legacy changed request accepted: %#v", apiErr)
		}
		if _, apiErr := a.createRefund(context.Background(), v.order, "extra_refund_"+v.status+"_00001", "legacy", items, "U", 1, 0, false, ""); apiErr == nil {
			t.Fatal("legacy entitlement reserved twice")
		}
		if err := a.confirmRefund(context.Background(), v.refund, "LEGACY-"+fmt.Sprint(v.refund), time.Now().UTC(), "SIMULATED"); err != nil {
			t.Fatalf("%s legacy confirmation: %v", v.status, err)
		}
		refunded, pending = refundCounter(t, database, v.item)
		if refunded != 1 || pending != 0 {
			t.Fatalf("confirmation counters=%d/%d", refunded, pending)
		}
	}
	for _, o := range []int64{badOrder, overOrder} {
		var reason string
		if err := database.QueryRow(`SELECT reason FROM refund_recovery_blocks WHERE order_id=?`, o).Scan(&reason); err != nil || reason == "" {
			t.Fatalf("inconsistent history was not isolated: %v", err)
		}
	}
	if refunded, pending := refundCounter(t, database, badItem); refunded != 0 || pending != 0 {
		t.Fatal("bad historical counters were silently rewritten")
	}
	if err := a.confirmRefund(context.Background(), badRefund, "BAD", time.Now(), "SIMULATED"); err == nil {
		t.Fatal("isolated refund confirmed")
	}
	if _, apiErr := a.createRefund(context.Background(), badOrder, "isolated_new_refund_0001", "new", []refundCreateItem{{OrderItemID: fmt.Sprint(badItem), Quantity: 1}}, "U", 1, 0, false, ""); apiErr == nil || apiErr.code != "REFUND_REQUIRES_RECONCILIATION" {
		t.Fatalf("isolated order accepted refund: %#v", apiErr)
	}
	var preserved int
	if err := database.QueryRow(`SELECT COUNT(*) FROM refund_restocks WHERE id=? AND request_id IS NULL AND quantity=1`, oldRestock).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("legacy restock changed: %v", err)
	}
	// A normal later startup skips recovery, preserving newer requests. Even a
	// replay of partially recorded 0004 must retain existing quarantine blocks.
	newOrder, newItem := refundOrderFixture(t, database, "post_recovery_order", 2)
	if _, apiErr := a.createRefund(context.Background(), newOrder, "post_recovery_refund_0001", "new", []refundCreateItem{{OrderItemID: fmt.Sprint(newItem), Quantity: 1}}, "U", 1, 0, false, ""); apiErr != nil {
		t.Fatal(apiErr)
	}
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	if _, pending := refundCounter(t, database, newItem); pending != 1 {
		t.Fatal("startup reset new refund counters")
	}
	refundSQL(t, database, `UPDATE refunds SET amount_cents=650 WHERE id=?`, badRefund)
	refundSQL(t, database, `DELETE FROM schema_migrations WHERE version='0004_refund_recovery.sql'`)
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	var blocked int
	if err := database.QueryRow(`SELECT COUNT(*) FROM refund_recovery_blocks WHERE order_id=?`, badOrder).Scan(&blocked); err != nil || blocked != 1 {
		t.Fatal("migration retry automatically cleared isolation")
	}
}
