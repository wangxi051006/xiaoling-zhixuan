package app

import (
	"math"
	"testing"
)

func TestNormalizeRefundCanonicalLines(t *testing.T) {
	a, err := normalizeRefundItems([]refundCreateItem{{OrderItemID: "20", Quantity: 1}, {OrderItemID: "3", Quantity: 2}, {OrderItemID: "20", Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalizeRefundItems([]refundCreateItem{{OrderItemID: "3", Quantity: 2}, {OrderItemID: "20", Quantity: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || a[0].OrderItemID != "3" || a[1].Quantity != 3 || refundRequestHash(42, "return", a) != refundRequestHash(42, "return", b) {
		t.Fatalf("equivalent refund lines differ: %#v %#v", a, b)
	}
	if refundRequestHash(42, "changed", a) == refundRequestHash(42, "return", a) {
		t.Fatal("reason is not bound to fingerprint")
	}
	if refundRequestHash(43, "return", a) == refundRequestHash(42, "return", a) {
		t.Fatal("order is not bound to fingerprint")
	}
}

func TestNormalizeRefundRejectsOverflow(t *testing.T) {
	if _, err := normalizeRefundItems([]refundCreateItem{{OrderItemID: "1", Quantity: math.MaxInt}, {OrderItemID: "1", Quantity: 1}}); err == nil {
		t.Fatal("quantity overflow accepted")
	}
	if _, err := normalizeRefundItems([]refundCreateItem{{OrderItemID: "1", Quantity: -1}}); err == nil {
		t.Fatal("negative quantity accepted")
	}
}
