//go:build integration

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMySQLDeliveryInboxTransactionAndStream(t *testing.T) {
	a, d, buyer, admin := merchantFixture(t)
	orderID, taskID := guardPaidOrder(t, a, d, buyer, "notification_order_00001")
	guardSQL(t, d, `UPDATE orders SET address_json=JSON_SET(address_json,'$.deliveryPoint',JSON_OBJECT('name','测试楼取货架')) WHERE id=?`, orderID)
	// Verify the message is invisible before the same transaction commits.
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.enqueueDeliveryNotification(context.Background(), tx, orderID, taskID); err != nil {
		t.Fatal(err)
	}
	inbox := guardStatus(t, guardRequest(a, buyer, "GET", "/api/v1/notifications", nil), 200)
	if len(inbox["items"].([]any)) != 0 {
		t.Fatal("uncommitted delivery leaked a notification")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE event_key IS NOT NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rollback left inbox rows")
	}
	// A second server instance receives the committed message from MySQL.
	otherInstance, err := New(a.cfg, d, a.log)
	if err != nil {
		t.Fatal(err)
	}
	otherInstance.Close()
	server := httptest.NewServer(otherInstance)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/notifications/stream?afterId=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+buyer)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status=%d", response.StatusCode)
	}
	received := make(chan userNotification, 1)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var n userNotification
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &n) == nil {
					received <- n
					return
				}
			}
		}
	}()
	tx, err = d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.enqueueDeliveryNotification(context.Background(), tx, orderID, taskID); err != nil {
		t.Fatal(err)
	}
	committedAt := time.Now()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var notification userNotification
	select {
	case notification = <-received:
	case <-ctx.Done():
		t.Fatal("no online message after commit")
	}
	elapsed := time.Since(committedAt)
	if elapsed > 2*time.Second {
		t.Fatalf("local online delivery too slow: %s", elapsed)
	}
	if notification.OrderID != toStringID(orderID) || !strings.Contains(notification.Body, "测试楼取货架") {
		t.Fatal(notification)
	}
	response.Body.Close()
	tx, err = d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.enqueueDeliveryNotification(context.Background(), tx, orderID, taskID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	inbox = guardStatus(t, guardRequest(a, buyer, "GET", "/api/v1/notifications", nil), 200)
	if len(inbox["items"].([]any)) != 1 || inbox["unreadCount"] != float64(1) {
		t.Fatalf("duplicate notification %#v", inbox)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM outbox WHERE topic='DELIVERY_NOTIFICATION'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox duplicates %d", count)
	}
	otherBuyer := guardToken(t, a, 2)
	otherInbox := guardStatus(t, guardRequest(a, otherBuyer, "GET", "/api/v1/notifications", nil), 200)
	if len(otherInbox["items"].([]any)) != 0 {
		t.Fatal("cross-user inbox leak")
	}
	guardStatus(t, guardRequest(a, otherBuyer, "POST", "/api/v1/notifications/"+notification.ID+"/read", nil), 404)
	guardStatus(t, guardRequest(a, buyer, "POST", "/api/v1/notifications/"+notification.ID+"/read", nil), 200)
	guardStatus(t, guardRequest(a, buyer, "POST", "/api/v1/notifications/"+notification.ID+"/read", nil), 200)
	inbox = guardStatus(t, guardRequest(a, buyer, "GET", "/api/v1/notifications?afterId="+notification.ID, nil), 200)
	if len(inbox["items"].([]any)) != 0 || inbox["unreadCount"] != float64(0) {
		t.Fatal("cursor/read state wrong")
	}
	guardStatus(t, guardRequest(a, buyer, "GET", "/api/v1/orders/"+toStringID(orderID)+"/receipt-code", nil), 410)
	guardStatus(t, guardRequest(a, admin, "GET", "/api/v1/notifications?afterId=-1", nil), 422)
	t.Logf("second App SSE notification after commit: %s; transactional rollback, duplicate suppression, offline inbox, per-user isolation passed", elapsed)
}
