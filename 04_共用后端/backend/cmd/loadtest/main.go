package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type result struct {
	status int
	ms     int64
	err    string
	body   []byte
}

func (c result) data() any {
	var parsed map[string]any
	_ = json.Unmarshal(c.body, &parsed)
	return parsed
}

type summary struct {
	name    string
	total   int
	errors  int
	min     int64
	max     int64
	p50     int64
	p95     int64
	p99     int64
	status  map[int]int
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	base := os.Getenv("LOADTEST_BASE")
	if base == "" {
		base = "http://127.0.0.1:18081"
	}
	verify := filepath.Join(root, "..", "verification")
	_ = os.MkdirAll(verify, 0o755)
	logFile, err := os.Create(filepath.Join(verify, "loadtest.log"))
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	log := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Fprintln(logFile, line)
		fmt.Fprintln(os.Stdout, line)
	}
	log("loadtest start %s base=%s", time.Now().Format(time.RFC3339), base)
	buyer := login(base, "13900000001", "CampusDemo!2026", log)
	admin := login(base, "13800000001", "CampusDemo!2026", log)
	quoteBody := map[string]any{
		"storeId": "1", "addressId": "1", "fulfillmentType": "DELIVERY",
		"items": []map[string]any{{"skuId": "1", "quantity": 1}},
	}
	scenarios := []summary{
		run(log, base, "healthz", 25, 500, func() result {
			return request(base, "GET", "/healthz", "", nil)
		}),
		run(log, base, "products", 25, 500, func() result {
			return request(base, "GET", "/api/v1/products", "", nil)
		}),
		run(log, base, "checkout-quotes", 25, 300, func() result {
			return request(base, "POST", "/api/v1/checkout/quotes", buyer, quoteBody)
		}),
		run(log, base, "orders-list", 25, 300, func() result {
			return request(base, "GET", "/api/v1/orders?limit=20", buyer, nil)
		}),
		run(log, base, "admin-workbench", 25, 300, func() result {
			return request(base, "GET", "/api/v1/admin/workbench", admin, nil)
		}),
	}
	log("")
	log("scenario                   total errs min ms p50 ms p95 ms p99 ms max ms")
	for _, item := range scenarios {
		log("%-26s %5d %4d %6d %6d %6d %6d %6d",
			item.name, item.total, item.errors, item.min, item.p50, item.p95, item.p99, item.max)
		for status, count := range item.status {
			log("  status %d: %d", status, count)
		}
	}
	log("loadtest done %s", time.Now().Format(time.RFC3339))
}

func run(log func(string, ...any), base, name string, concurrency, total int, fn func() result) summary {
	start := time.Now()
	results := make([]result, total)
	var group sync.WaitGroup
	guard := make(chan struct{}, concurrency)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			guard <- struct{}{}
			results[index] = fn()
			<-guard
		}(index)
	}
	group.Wait()
	elapsed := time.Since(start)
	durations := make([]int64, 0, total)
	status := map[int]int{}
	errors := 0
	var min int64 = 1 << 62
	var max int64
	for _, item := range results {
		if item.err != "" {
			errors++
			continue
		}
		status[item.status]++
		durations = append(durations, item.ms)
		if item.ms < min {
			min = item.ms
		}
		if item.ms > max {
			max = item.ms
		}
	}
	if len(durations) == 0 {
		min = 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pct := func(p float64) int64 {
		if len(durations) == 0 {
			return 0
		}
		index := int(float64(len(durations)-1) * p)
		return durations[index]
	}
	log("%s: %d requests in %s (%.0f rps)", name, total, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds())
	return summary{name: name, total: total, errors: errors, min: min, max: max, p50: pct(0.5), p95: pct(0.95), p99: pct(0.99), status: status}
}

func login(base, phone, password string, log func(string, ...any)) string {
	response := request(base, "POST", "/api/v1/auth/login", "", map[string]any{"phone": phone, "password": password})
	outer, _ := response.data().(map[string]any)
	inner, _ := outer["data"].(map[string]any)
	token, _ := inner["accessToken"].(string)
	if response.err != "" || response.status != 200 || token == "" {
		log("login failed for %s: %d %s body=%s", phone, response.status, response.err, string(response.body))
		os.Exit(1)
	}
	return token
}

type callResult struct {
	status int
	body   []byte
	err    string
}

func (c callResult) data() any {
	var parsed map[string]any
	_ = json.Unmarshal(c.body, &parsed)
	return parsed
}

func request(base, method, path, token string, body map[string]any) result {
	start := time.Now()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return result{err: err.Error(), ms: time.Since(start).Milliseconds()}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return result{err: err.Error(), ms: time.Since(start).Milliseconds()}
	}
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return result{status: response.StatusCode, ms: time.Since(start).Milliseconds(), body: payload}
}
