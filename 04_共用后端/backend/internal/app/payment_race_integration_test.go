//go:build integration

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"campusretail/internal/config"
	"campusretail/internal/db"
)

// These tests use real InnoDB locks and fault injection, never a merchant or
// network listener. Run serially against the disposable TEST_MYSQL_DSN only.
func paymentRaceFixture(t *testing.T) (*App, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	if !strings.Contains(dsn, "_test") {
		t.Fatal("disposable *_test database required")
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
	if _, err := database.Exec(`UPDATE inventory_lots SET qty_on_hand=50 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	a, err := New(config.Config{Env: "test", StoreID: 1, JWTSecret: "integration-test-secret-32-bytes!!", AccessTokenTTL: time.Hour, PaymentMode: "simulated"}, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close) // runs before database.Close
	p, err := a.loadPrincipal(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.issueToken(p)
	if err != nil {
		t.Fatal(err)
	}
	return a, database, token
}

func paymentRaceRequest(a *App, token, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func paymentRaceOrder(t *testing.T, a *App, token, key string) int64 {
	t.Helper()
	q := paymentRaceRequest(a, token, "/api/v1/checkout/quotes", map[string]any{"storeId": "1", "items": []map[string]any{{"skuId": "1", "quantity": 1}}, "addressId": "1", "fulfillmentType": "DELIVERY"})
	var quote struct {
		Data struct {
			QuoteID string `json:"quoteId"`
		} `json:"data"`
	}
	if q.Code != 200 {
		t.Fatalf("quote: %d %s", q.Code, q.Body)
	}
	if err := json.Unmarshal(q.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	w := paymentRaceRequest(a, token, "/api/v1/orders", map[string]any{"requestId": key, "storeId": "1", "items": []map[string]any{{"skuId": "1", "quantity": 1}}, "addressId": "1", "fulfillmentType": "DELIVERY", "quoteId": quote.Data.QuoteID, "expectedTotalCents": 850})
	var result struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if w.Code != 201 {
		t.Fatalf("order: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var id int64
	if _, err := fmt.Sscan(result.Data.OrderID, &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMySQLPaymentIntentCancellationRace(t *testing.T) {
	a, database, token := paymentRaceFixture(t)
	var clock atomic.Int64
	clock.Store(time.Now().UTC().Unix())
	a.now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	// Reproduce the independently observed insertion window. Polling the
	// trigger's sleep proves the payment INSERT has reached MySQL before cancel.
	if _, err := database.Exec(`CREATE TRIGGER payment_race_pause BEFORE INSERT ON payment_attempts FOR EACH ROW SET @payment_race_pause = SLEEP(1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec(`DROP TRIGGER IF EXISTS payment_race_pause`) })
	for _, mode := range []string{"cancel", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			id := paymentRaceOrder(t, a, token, "payment_race_order_"+mode)
			path := fmt.Sprintf("/api/v1/orders/%d", id)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- paymentRaceRequest(a, token, path+"/payment-attempt", map[string]any{"channel": "SIMULATED"})
			}()
			deadline := time.Now().Add(5 * time.Second)
			paused := false
			for time.Now().Before(deadline) {
				var n int
				if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE DB=DATABASE() AND STATE='User sleep' AND INFO LIKE '%payment_race_pause%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					paused = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !paused {
				<-done
				t.Fatal("payment INSERT never reached trigger; race not exercised")
			}
			if mode == "cancel" {
				w := paymentRaceRequest(a, token, path+"/cancel", map[string]any{})
				if w.Code != 200 {
					t.Fatalf("cancel: %d %s", w.Code, w.Body)
				}
			} else {
				clock.Add(11 * 60)
				if err := a.expireOrder(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			}
			w := <-done
			if w.Code != 201 && w.Code != 409 {
				t.Fatalf("attempt: %d %s", w.Code, w.Body)
			}
			var orderState, attemptState string
			var reserved int
			if err := database.QueryRow(`SELECT o.status,p.status,(SELECT COALESCE(SUM(quantity),0) FROM inventory_reservations r WHERE r.order_id=o.id AND r.status='RESERVED') FROM orders o JOIN payment_attempts p ON p.order_id=o.id WHERE o.id=?`, id).Scan(&orderState, &attemptState, &reserved); err != nil {
				t.Fatal(err)
			}
			if orderState != "CANCELLED" || attemptState != "CLOSED" || reserved != 0 {
				t.Fatalf("inconsistent terminal state: %s/%s reserved=%d", orderState, attemptState, reserved)
			}
		})
	}
}

func TestMySQLUncertainPaymentAndLatePrepay(t *testing.T) {
	a, database, token := paymentRaceFixture(t)
	for _, state := range []string{"PREPARING", "UNKNOWN", "FAILED"} {
		id := paymentRaceOrder(t, a, token, "uncertain_payment_"+state)
		result, err := database.Exec(`INSERT INTO payment_attempts(order_id,mchid,appid,out_trade_no,channel,amount_cents,currency,status) VALUES (?,'merchant','appid',?,'WECHAT_H5',850,'CNY',?)`, id, fmt.Sprintf("uncertain%d", id), state)
		if err != nil {
			t.Fatal(err)
		}
		attemptID, _ := result.LastInsertId()
		w := paymentRaceRequest(a, token, fmt.Sprintf("/api/v1/orders/%d/cancel", id), map[string]any{})
		if w.Code != 409 {
			t.Fatalf("%s cancel: %d", state, w.Code)
		}
		if _, err := database.Exec(`UPDATE orders SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if err := a.expireOrder(context.Background(), id); err == nil {
			t.Fatalf("%s expiry should preserve stock", state)
		}
		var reserved int
		if err := database.QueryRow(`SELECT SUM(quantity) FROM inventory_reservations WHERE order_id=? AND status='RESERVED'`, id).Scan(&reserved); err != nil || reserved != 1 {
			t.Fatalf("%s reserve=%d err=%v", state, reserved, err)
		}
		for _, terminal := range []string{"PAID", "CLOSED", "UNKNOWN"} {
			if _, err := database.Exec(`UPDATE payment_attempts SET status=? WHERE id=?`, terminal, attemptID); err != nil {
				t.Fatal(err)
			}
			applied, err := a.savePrepayResult(context.Background(), attemptID, prepayResult{PrepayID: "late", Params: map[string]any{"x": "y"}, ExpiresAt: time.Now().UTC().Add(time.Minute)})
			if err != nil || applied {
				t.Fatalf("late response overwrote %s: applied=%v err=%v", terminal, applied, err)
			}
		}
	}
}

func TestMySQLCancellationEventFailureRollsBack(t *testing.T) {
	a, database, token := paymentRaceFixture(t)
	id := paymentRaceOrder(t, a, token, "cancel_rollback_order_01")
	if _, err := database.Exec(`CREATE TRIGGER cancel_event_failure BEFORE INSERT ON order_events FOR EACH ROW BEGIN IF NEW.event_type='ORDER_CANCELLED' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected cancel event failure'; END IF; END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec(`DROP TRIGGER IF EXISTS cancel_event_failure`) })
	w := paymentRaceRequest(a, token, fmt.Sprintf("/api/v1/orders/%d/cancel", id), map[string]any{})
	if w.Code != 500 {
		t.Fatalf("expected rollback, got %d %s", w.Code, w.Body)
	}
	var state string
	var reserved int
	if err := database.QueryRow(`SELECT o.status,(SELECT SUM(quantity) FROM inventory_reservations WHERE order_id=o.id AND status='RESERVED') FROM orders o WHERE id=?`, id).Scan(&state, &reserved); err != nil {
		t.Fatal(err)
	}
	if state != "PENDING_PAYMENT" || reserved != 1 {
		t.Fatalf("partial cancellation: %s reserved=%d", state, reserved)
	}
}

func TestMySQLLatePaymentValidatesBindingWithoutReselling(t *testing.T) {
	a, database, token := paymentRaceFixture(t)
	id := paymentRaceOrder(t, a, token, "late_payment_binding_01")
	w := paymentRaceRequest(a, token, fmt.Sprintf("/api/v1/orders/%d/payment-attempt", id), map[string]any{"channel": "SIMULATED"})
	if w.Code != 201 {
		t.Fatalf("attempt: %d %s", w.Code, w.Body)
	}
	w = paymentRaceRequest(a, token, fmt.Sprintf("/api/v1/orders/%d/cancel", id), map[string]any{})
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	var trade string
	if err := database.QueryRow(`SELECT out_trade_no FROM payment_attempts WHERE order_id=?`, id).Scan(&trade); err != nil {
		t.Fatal(err)
	}
	event := paymentEvent{OrderID: toStringID(id), OutTradeNo: trade, TransactionID: fmt.Sprintf("SIM-LATE-%d", id), MchID: "SIMULATED", AppID: "SIMULATED", Currency: "CNY", AmountCents: 1}
	if err := a.confirmPayment(context.Background(), event); err == nil {
		t.Fatal("wrong-amount late callback accepted")
	}
	var state string
	if err := database.QueryRow(`SELECT status FROM orders WHERE id=?`, id).Scan(&state); err != nil || state != "CANCELLED" {
		t.Fatalf("unbound event changed state: %s %v", state, err)
	}
	event.AmountCents = 850
	if err := a.confirmPayment(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := a.confirmPayment(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var tasks, exceptions, reserved int
	if err := database.QueryRow(`SELECT o.status,(SELECT COUNT(*) FROM delivery_tasks WHERE order_id=o.id),(SELECT COUNT(*) FROM order_events WHERE order_id=o.id AND event_type='PAYMENT_EXCEPTION'),(SELECT COUNT(*) FROM inventory_reservations WHERE order_id=o.id AND status='RESERVED') FROM orders o WHERE id=?`, id).Scan(&state, &tasks, &exceptions, &reserved); err != nil {
		t.Fatal(err)
	}
	if state != "PAYMENT_EXCEPTION" || tasks != 0 || exceptions != 1 || reserved != 0 {
		t.Fatalf("late payment resold stock or duplicated exception: %s tasks=%d exceptions=%d reserved=%d", state, tasks, exceptions, reserved)
	}
}
