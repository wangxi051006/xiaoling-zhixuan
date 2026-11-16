//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This is a cross-runtime regression: a real Go/MySQL server must produce the
// response consumed by the shipped frontend, rather than a handwritten mock.
// Node is a test dependency only and never handles authoritative business data.
func TestMySQLMerchantBatchFrontendContract(t *testing.T) {
	node := os.Getenv("CAMPUS_TEST_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Fatal("Node is required for the frontend contract test; set CAMPUS_TEST_NODE to its executable")
		}
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"01_商家端", "02_用户端", "03_骑手端"} {
		t.Run(client, func(t *testing.T) {
			a, d, buyer, admin := merchantFixture(t)
			paid, _ := guardPaidOrder(t, a, d, buyer, "contract_paid_fixture_0001")
			unpaid := paymentRaceOrder(t, a, buyer, "contract_unpaid_fixture_01")
			faultFirst, _ := guardPaidOrder(t, a, d, buyer, "contract_fault_first_0001")
			faultSecond, _ := guardPaidOrder(t, a, d, buyer, "contract_fault_second_001")
			partialBody := batchPayload(t, d, "contract_partial_request_001", "ACCEPT", faultFirst, faultSecond)
			guardSQL(t, d, fmt.Sprintf("CREATE TRIGGER contract_audit_fault BEFORE INSERT ON audit_logs FOR EACH ROW BEGIN IF NEW.object_id='%d' AND NEW.action='BATCH_ACCEPT' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='contract audit failure'; END IF; END", faultSecond))
			t.Cleanup(func() { d.Exec(`DROP TRIGGER IF EXISTS contract_audit_fault`) })
			partial := guardStatus(t, guardRequest(a, admin, "POST", merchantBatchPath, partialBody), 503)
			if partial["pendingCount"] != float64(1) || partial["completedCount"] != float64(1) {
				t.Fatalf("expected real partial progress, got %#v", partial)
			}
			guardSQL(t, d, `DROP TRIGGER contract_audit_fault`)
			initial := batchPayload(t, d, "contract_initial_versions_01", "ACCEPT", paid, unpaid)["items"].([]map[string]any)
			before := batchLedger(t, d)
			server := httptest.NewServer(a)
			defer server.Close()
			config, err := json.Marshal(map[string]any{
				"baseURL": server.URL, "token": admin, "paid": initial[0], "unpaid": initial[1],
				"partialData":  partial,
				"partialDraft": map[string]any{"path": "/admin/orders/batch-actions", "requestId": partialBody["requestId"], "payload": map[string]any{"action": "ACCEPT", "items": partialBody["items"]}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, filepath.Join(root, "scripts", "tests", "merchant-batch-contract.mjs"), filepath.Join(root, client, "web", "src", "batch.js"))
			cmd.Stdin = bytes.NewReader(config)
			output, err := cmd.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatalf("actual backend/frontend contract failed: %v", err)
			}
			if after := batchLedger(t, d); after != before {
				t.Fatalf("contract actions changed stock/money/first-order ledger: %s -> %s", before, after)
			}
		})
	}
}
