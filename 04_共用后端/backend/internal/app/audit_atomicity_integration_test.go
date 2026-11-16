//go:build integration

package app

import (
	"strings"
	"testing"
)

// The independent review injected a failing audit INSERT and observed the price
// change commit anyway (650 -> 773 with zero audit rows). This test locks the
// opposite behaviour: a failed audit must abort the business transaction.
func TestMySQLAuditFailureRollsBackProductUpdate(t *testing.T) {
	a, database, _ := paymentRaceFixture(t)
	a.Close()
	seedAdminForTest(t, database)
	admin := guardToken(t, a, 3)

	var before int64
	if err := database.QueryRow(`SELECT price_cents FROM skus WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"name": "测试薯片", "description": "", "active": true, "expectedVersion": 1,
		"skus": []map[string]any{{"id": "1", "specName": "70g", "priceCents": 773, "active": true}},
	}

	guardSQL(t, database, `CREATE TRIGGER audit_reject BEFORE INSERT ON audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit down'`)
	w := guardRequest(a, admin, "PATCH", "/api/v1/admin/products/1", body)
	guardSQL(t, database, `DROP TRIGGER audit_reject`)

	if w.Code != 500 {
		t.Fatalf("audit failure must reject the request, got %d %s", w.Code, w.Body)
	}
	var after int64
	if err := database.QueryRow(`SELECT price_cents FROM skus WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("price changed without audit: %d -> %d", before, after)
	}
	var audits int
	database.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='UPDATE_PRODUCT'`).Scan(&audits)
	if audits != 0 {
		t.Fatalf("expected no audit rows, got %d", audits)
	}

	// Without the fault injection the change and its trail commit together.
	guardStatus(t, guardRequest(a, admin, "PATCH", "/api/v1/admin/products/1", body), 200)
	if err := database.QueryRow(`SELECT price_cents FROM skus WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 773 {
		t.Fatalf("price=%d want 773", after)
	}
	var beforeJSON, afterJSON string
	err := database.QueryRow(`SELECT COALESCE(CAST(before_json AS CHAR),''),COALESCE(CAST(after_json AS CHAR),'')
		FROM audit_logs WHERE action='UPDATE_PRODUCT' ORDER BY id DESC LIMIT 1`).Scan(&beforeJSON, &afterJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(beforeJSON, "650") || !strings.Contains(afterJSON, "773") {
		t.Fatalf("audit before=%s after=%s", beforeJSON, afterJSON)
	}
}
