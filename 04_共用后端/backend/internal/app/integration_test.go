//go:build integration
// +build integration

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"campusretail/internal/config"
	"campusretail/internal/db"
)

type httpResult struct {
	status int
	body   map[string]any
}

func TestMySQLOrderConcurrency(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; skipping real MySQL integration test")
	}
	if !strings.Contains(dsn, "_test") {
		t.Fatal("TEST_MYSQL_DSN must point to a disposable *_test database")
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database, "../../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	cleanDatabase(t, database)
	seedIntegrationData(t, database)
	cfg := config.Config{
		Env: "test", StoreID: 1, JWTSecret: "integration-test-secret-32-bytes!!",
		AccessTokenTTL: time.Hour, PaymentMode: "simulated", AUTO_MIGRATE: false,
	}
	a, err := New(cfg, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := httptest.NewServer(a)
	defer server.Close()

	token := loginForTest(t, server.URL, "13900000001")
	quote := quoteForTest(t, server.URL, token, "1", "1", 1)
	requestID := "integration_same_request_0001"
	body := map[string]any{
		"requestId": requestID, "storeId": "1", "items": []map[string]any{{"skuId": "1", "quantity": 1}},
		"addressId": "1", "fulfillmentType": "DELIVERY", "quoteId": quote, "expectedTotalCents": 850,
	}
	const workers = 12
	results := make([]httpResult, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index] = postJSON(server.URL+"/api/v1/orders", token, body)
		}(index)
	}
	group.Wait()
	orderIDs := map[string]bool{}
	created := 0
	for _, result := range results {
		if result.status == 201 {
			created++
		}
		data, _ := result.body["data"].(map[string]any)
		if id, ok := data["orderId"].(string); ok {
			orderIDs[id] = true
		}
		if result.status >= 500 {
			t.Fatalf("unexpected 5xx: %d %#v", result.status, result.body)
		}
	}
	if created != 1 || len(orderIDs) != 1 {
		t.Fatalf("same request must produce exactly one order, created=%d ids=%v", created, orderIDs)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM orders WHERE buyer_id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 order, got %d (%v)", count, err)
	}

	// A second customer with two distinct requests must get ordinals 1 and 2;
	// only one row may be the immutable first-order snapshot.
	token2 := loginForTest(t, server.URL, "13900000002")
	quoteA := quoteForTest(t, server.URL, token2, "2", "2", 1)
	quoteB := quoteForTest(t, server.URL, token2, "2", "2", 1)
	bodyA := map[string]any{"requestId": "integration_first_order_A", "storeId": "1",
		"items": []map[string]any{{"skuId": "2", "quantity": 1}}, "addressId": "2", "fulfillmentType": "DELIVERY",
		"quoteId": quoteA, "expectedTotalCents": 900}
	bodyB := map[string]any{"requestId": "integration_first_order_B", "storeId": "1",
		"items": []map[string]any{{"skuId": "2", "quantity": 1}}, "addressId": "2", "fulfillmentType": "DELIVERY",
		"quoteId": quoteB, "expectedTotalCents": 900}
	var wg sync.WaitGroup
	resultsA := make([]httpResult, 2)
	wg.Add(2)
	go func() { defer wg.Done(); resultsA[0] = postJSON(server.URL+"/api/v1/orders", token2, bodyA) }()
	go func() { defer wg.Done(); resultsA[1] = postJSON(server.URL+"/api/v1/orders", token2, bodyB) }()
	wg.Wait()
	for _, result := range resultsA {
		if result.status != 201 {
			t.Fatalf("expected both distinct orders to succeed, got %d %#v", result.status, result.body)
		}
	}
	var firstCount int
	rows, err := database.Query(`SELECT customer_order_ordinal,is_first_order FROM orders WHERE buyer_id=2 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var ordinal, first int
		if err := rows.Scan(&ordinal, &first); err != nil {
			t.Fatal(err)
		}
		seen[ordinal] = true
		firstCount += first
	}
	if !seen[1] || !seen[2] || firstCount != 1 {
		t.Fatalf("expected ordinals 1/2 and exactly one first order, seen=%v first=%d", seen, firstCount)
	}
}

func cleanDatabase(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "schema_migrations" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS=1`)
	if _, err := conn.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS=0`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if _, err := conn.ExecContext(context.Background(), "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS=1`); err != nil {
		t.Fatal(err)
	}
}

func seedIntegrationData(t *testing.T, database *sql.DB) {
	t.Helper()
	hash, err := HashPassword("CampusDemo!2026")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO stores(id,name) VALUES (1,'测试店')`,
		`INSERT INTO buildings(id,store_id,name,delivery_fee_cents,active) VALUES (1,1,'测试楼',200,1),(2,1,'测试楼2',200,1)`,
		`INSERT INTO categories(id,store_id,name,sort_order,active) VALUES (1,1,'测试分类',1,1)`,
		`INSERT INTO products(id,store_id,category_id,name,description,active,is_demo) VALUES (1,1,1,'测试薯片','',1,1)`,
		`INSERT INTO skus(id,store_id,product_id,spec_name,price_cents,active) VALUES (1,1,1,'70g',650,1),(2,1,1,'140g',700,1)`,
		`INSERT INTO inventory_lots(id,store_id,sku_id,batch_no,expiry_date,qty_on_hand,qty_reserved,unit_cost_cents) VALUES
			(1,1,1,'T1','2027-12-31',1,0,300),(2,1,2,'T2','2027-12-31',2,0,300)`,
		`INSERT INTO users(id,phone,nickname,password_hash,status,is_demo) VALUES
			(1,'13900000001','并发一',?, 'ACTIVE',1),(2,'13900000002','首单二',?, 'ACTIVE',1)`,
		`INSERT INTO addresses(id,owner_id,store_id,building_id,building_name_snapshot,contact_name,phone,detail) VALUES
			(1,1,1,1,'测试楼','并发一','13900000001','101'),(2,2,1,2,'测试楼2','首单二','13900000002','202')`,
	}
	for _, statement := range statements {
		var err error
		if strings.Contains(statement, "password_hash") {
			_, err = database.Exec(statement, hash, hash)
		} else {
			_, err = database.Exec(statement)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func loginForTest(t *testing.T, baseURL, phone string) string {
	t.Helper()
	result := postJSON(baseURL+"/api/v1/auth/login", "", map[string]any{"phone": phone, "password": "CampusDemo!2026"})
	if result.status != 200 {
		t.Fatalf("login failed: %d %#v", result.status, result.body)
	}
	data, _ := result.body["data"].(map[string]any)
	token, _ := data["accessToken"].(string)
	if token == "" {
		t.Fatalf("login returned no token: %#v", result.body)
	}
	return token
}

func quoteForTest(t *testing.T, baseURL, token, skuID, addressID string, quantity int) string {
	t.Helper()
	result := postJSON(baseURL+"/api/v1/checkout/quotes", token, map[string]any{
		"storeId": "1", "addressId": addressID, "fulfillmentType": "DELIVERY",
		"items": []map[string]any{{"skuId": skuID, "quantity": quantity}},
	})
	if result.status != 200 {
		t.Fatalf("quote failed: %d %#v", result.status, result.body)
	}
	data, _ := result.body["data"].(map[string]any)
	quoteID, _ := data["quoteId"].(string)
	if quoteID == "" {
		t.Fatalf("quote returned no id: %#v", result.body)
	}
	return quoteID
}

func postJSON(url, token string, body any) httpResult {
	payload, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return httpResult{status: 0, body: map[string]any{"error": err.Error()}}
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return httpResult{status: response.StatusCode, body: parsed}
}
