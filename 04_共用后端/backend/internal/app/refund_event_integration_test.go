//go:build integration
// +build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMySQLRefundChannelEvidenceAndReplay(t *testing.T) {
	database := refundTestDatabase(t)
	a := &App{db: database, tz: loadShanghai(), now: time.Now}
	order, item := refundOrderFixture(t, database, "refund_channel_evidence", 1)
	ref, apiErr := a.createRefund(context.Background(), order, "refund_channel_evidence01", "return", []refundCreateItem{{OrderItemID: fmt.Sprint(item), Quantity: 1}}, "U", 1, 0, false, "")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	var refundID int64
	fmt.Sscan(ref.ID, &refundID)
	// Old inbox events lacked channel time. They remain retryable/visible for
	// reconciliation and cannot silently assign the worker's current date.
	oldPayload := []byte(fmt.Sprintf(`{"refundId":%q,"outRefundNo":%q,"providerRefundId":"503000001234","amountCents":650,"eventType":"REFUND.SUCCESS"}`, ref.ID, ref.OutRefundNo))
	inbox := refundSQL(t, database, `INSERT INTO payment_inbox(provider,event_id,event_type,payload_digest,payload_json) VALUES ('WECHAT_REFUND','legacy-channel-evidence','REFUND.SUCCESS',?,?)`, sha256Hex(oldPayload), oldPayload)
	if err := a.processInboxEvent(context.Background(), inbox); err == nil {
		t.Fatal("legacy inbox without success time confirmed")
	}
	var state, lastError string
	if err := database.QueryRow(`SELECT state,last_error FROM payment_inbox WHERE id=?`, inbox).Scan(&state, &lastError); err != nil {
		t.Fatal(err)
	}
	if state != "RECEIVED" || !strings.Contains(lastError, "reconciliation") {
		t.Fatalf("legacy evidence was not retained for reconciliation: %s %s", state, lastError)
	}
	if refunded, pending := refundCounter(t, database, item); refunded != 0 || pending != 1 {
		t.Fatal("bad callback changed quantity counters")
	}
	succeeded := time.Date(2026, 11, 12, 16, 30, 0, 0, time.UTC)
	event := refundSuccessEvent{RefundID: ref.ID, OutRefundNo: ref.OutRefundNo, ProviderRefundID: "503000001234", AmountCents: 650, SucceededAt: succeeded}
	confirm := func(e refundSuccessEvent) error {
		payload, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return a.confirmRefundEvent(context.Background(), paymentEvent{}, payload)
	}
	wrong := event
	wrong.AmountCents = 649
	if err := confirm(wrong); err == nil {
		t.Fatal("mismatched first refund amount accepted")
	}
	if err := confirm(event); err != nil {
		t.Fatal(err)
	}
	if err := confirm(event); err != nil {
		t.Fatalf("exact successful replay rejected: %v", err)
	}
	for name, changed := range map[string]refundSuccessEvent{
		"amount":   {RefundID: ref.ID, OutRefundNo: ref.OutRefundNo, ProviderRefundID: event.ProviderRefundID, AmountCents: 649, SucceededAt: succeeded},
		"provider": {RefundID: ref.ID, OutRefundNo: ref.OutRefundNo, ProviderRefundID: "different-provider", AmountCents: 650, SucceededAt: succeeded},
		"time":     {RefundID: ref.ID, OutRefundNo: ref.OutRefundNo, ProviderRefundID: event.ProviderRefundID, AmountCents: 650, SucceededAt: succeeded.Add(time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := confirm(changed); err == nil {
				t.Fatal("conflicting successful callback replay accepted")
			}
		})
	}
	if err := a.confirmRefundAmount(context.Background(), refundID, event.ProviderRefundID, 0, succeeded, "WECHAT"); err == nil {
		t.Fatal("zero amount bypassed successful replay verification")
	}
	var count, amount int64
	var occurred, storedSucceeded time.Time
	var provider, source string
	if err := database.QueryRow(`SELECT COUNT(*),SUM(amount_cents) FROM refund_cash_ledger WHERE refund_id=?`, refundID).Scan(&count, &amount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT occurred_at,source FROM refund_cash_ledger WHERE refund_id=?`, refundID).Scan(&occurred, &source); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT provider_refund_id,succeeded_at FROM refunds WHERE id=?`, refundID).Scan(&provider, &storedSucceeded); err != nil {
		t.Fatal(err)
	}
	if count != 1 || amount != 650 || source != "WECHAT" || provider != event.ProviderRefundID || !occurred.Equal(succeeded) || !storedSucceeded.Equal(succeeded) {
		t.Fatalf("channel evidence lost count=%d amount=%d source=%s occurred=%v succeeded=%v", count, amount, source, occurred, storedSucceeded)
	}
}
