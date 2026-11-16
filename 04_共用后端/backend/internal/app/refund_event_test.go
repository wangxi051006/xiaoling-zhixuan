package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRefundEventRequiresExactChannelEvidence(t *testing.T) {
	base := `{"outRefundNo":"U123","providerRefundId":"503000001234","amountCents":%s,"succeededAt":"2026-11-13T00:30:00+08:00"}`
	event, err := parseRefundSuccessEvent([]byte(fmt.Sprintf(base, "9007199254740993")))
	if err != nil || event.AmountCents != 9007199254740993 || event.SucceededAt.Format(time.RFC3339) != "2026-11-12T16:30:00Z" {
		t.Fatalf("integer/time evidence changed: %#v %v", event, err)
	}
	valid := fmt.Sprintf(base, "650")
	for name, payload := range map[string]string{
		"fraction": fmt.Sprintf(base, "650.9"), "exponent": fmt.Sprintf(base, "6.5e2"), "zero": fmt.Sprintf(base, "0"), "negative": fmt.Sprintf(base, "-1"), "overflow": fmt.Sprintf(base, "9223372036854775808"),
		"missing_amount":    strings.ReplaceAll(valid, `"amountCents":650,`, ""),
		"missing_provider":  strings.ReplaceAll(valid, `"providerRefundId":"503000001234",`, ""),
		"missing_time":      `{"outRefundNo":"U123","providerRefundId":"503000001234","amountCents":650,"paidAt":"2026-11-13T00:30:00+08:00"}`,
		"zero_time":         strings.ReplaceAll(valid, "2026-11-13T00:30:00+08:00", "0001-01-01T00:00:00Z"),
		"bad_time":          strings.ReplaceAll(valid, "2026-11-13T00:30:00+08:00", "2026-11-13 00:30:00"),
		"missing_reference": strings.ReplaceAll(valid, `"outRefundNo":"U123",`, ""),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRefundSuccessEvent([]byte(payload)); err == nil {
				t.Fatal("incomplete/invalid channel evidence accepted")
			}
			// No database is configured: invalid legacy events must stop before
			// any lookup, even if the generic event offers fallback ID/time.
			a := &App{}
			if err := a.confirmRefundEvent(context.Background(), paymentEvent{TransactionID: "fallback", PaidAt: time.Now()}, []byte(payload)); err == nil {
				t.Fatal("legacy event guessed channel evidence")
			}
		})
	}
}

func TestWechatSuccessTimeRejectsMissingOrInvalid(t *testing.T) {
	for _, raw := range []string{"", "not-a-time", "2026-11-13T00:30:00", "0001-01-01T00:00:00Z"} {
		if _, err := parseWechatSuccessTime(raw); err == nil {
			t.Fatalf("accepted invalid channel time %q", raw)
		}
	}
	got, err := parseWechatSuccessTime("2026-11-13T00:30:00+08:00")
	if err != nil || got.Format(time.RFC3339) != "2026-11-12T16:30:00Z" {
		t.Fatalf("channel time lost: %v %v", got, err)
	}
}
