package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTripAndRejection(t *testing.T) {
	hash, err := HashPassword("CampusDemo!2026")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2_sha256$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if !verifyPassword(hash, "CampusDemo!2026") {
		t.Fatal("correct password rejected")
	}
	if verifyPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
}

func TestRequestIDRules(t *testing.T) {
	valid := "checkout_20261113_7f12a9c0"
	if !validRequestID(valid) {
		t.Fatalf("%s should be valid", valid)
	}
	invalid := []string{"short", strings.Repeat("a", 129), "checkout 2026", "checkout/2026", ""}
	for _, value := range invalid {
		if validRequestID(value) {
			t.Fatalf("%q should be rejected", value)
		}
	}
}

func TestNormalizeItemsMergesSortsAndLimits(t *testing.T) {
	items, err := normalizedItemIDs([]ItemInput{
		{SkuID: "9", Quantity: 2}, {SkuID: "1", Quantity: 1}, {SkuID: "9", Quantity: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SkuID != "1" || items[0].Quantity != 1 || items[1].SkuID != "9" || items[1].Quantity != 5 {
		t.Fatalf("unexpected normalized items: %#v", items)
	}
	if _, err := normalizedItemIDs([]ItemInput{{SkuID: "1", Quantity: 100}}); err == nil {
		t.Fatal("quantity 100 must be rejected")
	}
	tooMany := make([]ItemInput, 21)
	for index := range tooMany {
		tooMany[index] = ItemInput{SkuID: toStringID(int64(index + 1)), Quantity: 1}
	}
	if _, err := normalizedItemIDs(tooMany); err == nil {
		t.Fatal("21 distinct skus must be rejected")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 13, 2, 3, 4, 0, time.UTC)
	cursor := encodeCursor(now, 42)
	parsedTime, parsedID, err := decodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !parsedTime.Equal(now) || parsedID != 42 {
		t.Fatalf("cursor roundtrip mismatch: %s %d", parsedTime, parsedID)
	}
	if _, _, err := decodeCursor("not-base64"); err == nil {
		t.Fatal("invalid cursor must be rejected")
	}
}

func TestShanghaiDateWindow(t *testing.T) {
	a := &App{tz: loadShanghai()}
	start, end, date, err := a.dateWindow("2026-11-13")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-11-13" || !start.Before(end) || end.Sub(start) != 24*time.Hour {
		t.Fatalf("unexpected window %s %s %s", start, end, date)
	}
	if _, _, _, err := a.dateWindow("2026-02-30"); err == nil {
		t.Fatal("invalid date must be rejected")
	}
}

func TestAllowedActionsRespectRole(t *testing.T) {
	customer := allowedOrderActions("PENDING_PAYMENT", "UNPAID", false)
	if !contains(customer, "PAY") || contains(customer, "CLOSE") {
		t.Fatalf("unexpected customer actions: %#v", customer)
	}
	admin := allowedOrderActions("PENDING_PAYMENT", "UNPAID", true)
	if !contains(admin, "CLOSE") || contains(admin, "PAY") {
		t.Fatalf("unexpected admin actions: %#v", admin)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestDuplicateKeyDetector(t *testing.T) {
	if !isDuplicateKey(errors.New("Error 1062 (23000): Duplicate entry")) {
		t.Fatal("duplicate key not detected")
	}
	if isDuplicateKey(errors.New("connection reset")) {
		t.Fatal("unrelated error treated as duplicate")
	}
}
