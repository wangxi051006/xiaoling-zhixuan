package app

import (
	"math"
	"strings"
	"testing"
	"time"

	"campusretail/internal/config"
)

func TestArrivalLocationGuards(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	point := &deliveryPoint{Latitude: 31.2304, Longitude: 121.4737, RadiusMeters: 80}
	valid := arrivalRequest{Latitude: &point.Latitude, Longitude: &point.Longitude, Accuracy: 12, ObservedAt: now}
	if err := validateArrivalLocation(now, valid, point); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, code string
		change     func(*arrivalRequest)
	}{
		{"missing", "INVALID_LOCATION", func(r *arrivalRequest) { r.Latitude = nil }},
		{"nonfinite", "INVALID_LOCATION", func(r *arrivalRequest) { v := math.NaN(); r.Latitude = &v }},
		{"stale", "LOCATION_STALE", func(r *arrivalRequest) { r.ObservedAt = now.Add(-91 * time.Second) }},
		{"future", "LOCATION_STALE", func(r *arrivalRequest) { r.ObservedAt = now.Add(16 * time.Second) }},
		{"no_accuracy", "LOCATION_INACCURATE", func(r *arrivalRequest) { r.Accuracy = 0 }},
		{"inaccurate", "LOCATION_INACCURATE", func(r *arrivalRequest) { r.Accuracy = 51 }},
		{"outside", "OUTSIDE_DELIVERY_POINT", func(r *arrivalRequest) { v := 31.24; r.Latitude = &v }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.change(&req)
			err := validateArrivalLocation(now, req, point)
			if err == nil || err.code != test.code {
				t.Fatalf("got %v, want %s", err, test.code)
			}
		})
	}
	if distanceMeters(0, 0, 0, 1) < 111000 || distanceMeters(0, 0, 0, 1) > 112000 {
		t.Fatal("distance must use meters")
	}
}

func TestArrivalProofIntegrityAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a := &App{cfg: config.Config{JWTSecret: "arrival-unit-secret"}, now: func() time.Time { return now }}
	claims := arrivalClaims{Type: "courier-arrival-v1", StoreID: 1, CourierID: 4, PointHash: "point", Tasks: []arrivalBinding{{TaskID: 10, Generation: 2}}, ExpiresAt: now.Add(3 * time.Minute).Unix()}
	token := a.signArrival(claims)
	if parsed, err := a.parseArrival(token); err != nil || parsed.Tasks[0].Generation != 2 {
		t.Fatalf("parse: %#v %v", parsed, err)
	}
	parts := strings.Split(token, ".")
	parts[0] = "a" + parts[0]
	if _, err := a.parseArrival(strings.Join(parts, ".")); err == nil || err.code != "ARRIVAL_INVALID" {
		t.Fatal("tampering accepted")
	}
	now = now.Add(3 * time.Minute)
	if _, err := a.parseArrival(token); err == nil || err.code != "ARRIVAL_EXPIRED" {
		t.Fatal("expired proof accepted")
	}
	if _, err := a.parseArrival(""); err == nil || err.code != "ARRIVAL_REQUIRED" {
		t.Fatal("empty proof accepted")
	}
}

func TestCourierBatchBounds(t *testing.T) {
	req := courierBatchRequest{RequestID: "courier_batch_unit_00001", Action: "CLAIM", Items: []courierBatchItem{{"2", 1}, {"1", 1}}}
	if err := normalizeCourierBatch(&req); err != nil {
		t.Fatal(err)
	}
	if req.Items[0].TaskID != "1" {
		t.Fatal("items must normalize numerically")
	}
	req.Items = append(req.Items, courierBatchItem{"1", 1})
	if err := normalizeCourierBatch(&req); err == nil || err.code != "DUPLICATE_TASK" {
		t.Fatal("duplicate accepted")
	}
	req.Items = make([]courierBatchItem, 21)
	if err := normalizeCourierBatch(&req); err == nil || err.code != "INVALID_BATCH_SIZE" {
		t.Fatal("oversized batch accepted")
	}
}
