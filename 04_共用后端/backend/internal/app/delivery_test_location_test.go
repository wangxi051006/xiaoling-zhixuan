//go:build integration

package app

import (
	"database/sql"
	"testing"
	"time"
)

// Test-only coordinates supplied through the same authenticated merchant/rider
// APIs as the application. No production geolocation bypass is introduced.
func receiptReplacementToken(t *testing.T, a *App, d *sql.DB, admin, rider string, taskID int64) string {
	t.Helper()
	var building string
	if err := d.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(o.address_json,'$.buildingId')) FROM orders o JOIN delivery_tasks t ON t.order_id=o.id WHERE t.id=?`, taskID).Scan(&building); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM delivery_points WHERE store_id=1 AND building_id=?`, building).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		guardStatus(t, guardRequest(a, admin, "POST", "/api/v1/admin/delivery-points", map[string]any{"buildingId": building, "name": "集成测试交付点", "latitude": 31.2, "longitude": 121.4, "radiusMeters": 100, "active": true}), 200)
	}
	data := guardStatus(t, guardRequest(a, rider, "POST", "/api/v1/courier/arrivals", map[string]any{"taskIds": []string{toStringID(taskID)}, "latitude": 31.2, "longitude": 121.4, "accuracy": 10, "observedAt": time.Now().UTC()}), 200)
	return data["arrivalToken"].(string)
}
