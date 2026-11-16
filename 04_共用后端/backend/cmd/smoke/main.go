package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

type apiResponse struct {
	Status  int
	Body    map[string]any
	RawBody string
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	verify := filepath.Join(root, "..", "verification")
	_ = os.MkdirAll(verify, 0o755)
	logFile, err := os.Create(filepath.Join(verify, "smoke.log"))
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	log := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Fprintln(logFile, line)
		fmt.Fprintln(os.Stdout, line)
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	serverLog, _ := os.Create(filepath.Join(verify, "server-smoke.log"))
	defer serverLog.Close()
	command := exec.Command(goBinary, "run", "./cmd/server")
	command.Dir = root
	command.Env = append(os.Environ(),
		"APP_ENV=local", "HTTP_ADDR=127.0.0.1:18080", "STORE_ID=1", "PAYMENT_MODE=simulated",
		"AUTO_MIGRATE=true", "MIGRATIONS_DIR=../db/migrations",
		"MYSQL_DSN=campus:CampusLocal!2026@tcp(127.0.0.1:3307)/campus_retail?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true",
		"GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOPROXY=https://goproxy.cn,direct", "GOSUMDB=sum.golang.google.cn")
	command.Stdout = serverLog
	command.Stderr = serverLog
	log("smoke start %s", time.Now().Format(time.RFC3339))
	if err := command.Start(); err != nil {
		log("server start failed: %v", err)
		os.Exit(1)
	}
	defer func() { _ = command.Process.Kill() }()
	base := "http://127.0.0.1:18080"
	ready := false
	for attempt := 0; attempt < 60; attempt++ {
		response, err := http.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(time.Second)
	}
	if !ready {
		log("server did not become ready")
		os.Exit(1)
	}
	log("healthz OK")
	buyer := login(base, "13900000001", "CampusDemo!2026", log)
	admin := login(base, "13800000001", "CampusDemo!2026", log)
	courier := login(base, "13800000004", "CampusDemo!2026", log)
	list := call(base, "GET", "/api/v1/orders?limit=50", buyer, nil, log)
	items, _ := list.Body["data"].(map[string]any)["items"].([]any)
	var order map[string]any
	for _, item := range items {
		candidate, _ := item.(map[string]any)
		if candidate["orderNo"] == "DEMO2026111303" {
			order = candidate
			break
		}
	}
	if order == nil {
		log("active demo order not found: %s", list.RawBody)
		os.Exit(1)
	}
	orderID, _ := order["id"].(string)
	detail := call(base, "GET", "/api/v1/orders/"+orderID, buyer, nil, log)
	urgeSummary, _ := detail.Body["data"].(map[string]any)["urge"].(map[string]any)
	if urgeSummary == nil || urgeSummary["allowed"] != true {
		log("urge should be allowed on seeded delivering order: %s", detail.RawBody)
		os.Exit(1)
	}
	created := call(base, "POST", "/api/v1/orders/"+orderID+"/urges", buyer, map[string]any{
		"requestId": "smoke_urge_20261113_0001", "reason": "本地闭环冒烟：请尽快处理",
	}, log)
	urge, _ := created.Body["data"].(map[string]any)["urge"].(map[string]any)
	if created.Status != 201 || urge == nil {
		log("create urge failed: status=%d %s", created.Status, created.RawBody)
		os.Exit(1)
	}
	urgeID, _ := urge["id"].(string)
	log("urge created id=%s status=%v", urgeID, urge["status"])
	workbench := call(base, "GET", "/api/v1/admin/workbench", admin, nil, log)
	pending, _ := workbench.Body["data"].(map[string]any)["pendingUrges"].(float64)
	if pending < 1 {
		log("workbench pendingUrges=%v body=%s", pending, workbench.RawBody)
		os.Exit(1)
	}
	log("workbench pendingUrges=%v", pending)
	adminDetail := call(base, "GET", "/api/v1/admin/orders/"+orderID, admin, nil, log)
	adminUrge, _ := adminDetail.Body["data"].(map[string]any)["urge"].(map[string]any)
	records, _ := adminUrge["records"].([]any)
	if len(records) == 0 {
		log("admin detail has no urge records: %s", adminDetail.RawBody)
		os.Exit(1)
	}
	record, _ := records[0].(map[string]any)
	version, _ := record["version"].(float64)
	handled := call(base, "POST", "/api/v1/admin/urges/"+urgeID+"/handle", admin, map[string]any{
		"expectedVersion": int64(version), "note": "冒烟：已查看并安排处理",
	}, log)
	if handled.Status != 200 || handled.Body["data"].(map[string]any)["status"] != "HANDLED" {
		log("handle urge failed: status=%d %s", handled.Status, handled.RawBody)
		os.Exit(1)
	}
	log("admin handled urge")
	after := call(base, "GET", "/api/v1/admin/orders/"+orderID, admin, nil, log)
	if after.Body["data"].(map[string]any)["status"] != "DELIVERING" {
		log("handling urge changed order status: %s", after.RawBody)
		os.Exit(1)
	}
	tasks := call(base, "GET", "/api/v1/courier/tasks", courier, nil, log)
	var taskID string
	rawTasks, _ := tasks.Body["data"].([]any)
	for _, item := range rawTasks {
		task, _ := item.(map[string]any)
		if task["orderId"] == orderID {
			taskID, _ = task["id"].(string)
		}
	}
	if taskID == "" {
		log("courier task not found for order: %s", tasks.RawBody)
		os.Exit(1)
	}
	taskDetail := call(base, "GET", "/api/v1/courier/tasks/"+taskID, courier, nil, log)
	taskUrges, _ := taskDetail.Body["data"].(map[string]any)["urges"].([]any)
	if len(taskUrges) == 0 {
		log("courier task detail has no urges: %s", taskDetail.RawBody)
		os.Exit(1)
	}
	log("courier sees %d urge(s); order status remains DELIVERING", len(taskUrges))
	log("smoke OK %s", time.Now().Format(time.RFC3339))
}

func login(base, phone, password string, log func(string, ...any)) string {
	response := call(base, "POST", "/api/v1/auth/login", "", map[string]any{"phone": phone, "password": password}, log)
	data, _ := response.Body["data"].(map[string]any)
	token, _ := data["accessToken"].(string)
	if response.Status != 200 || token == "" {
		log("login %s failed: status=%d %s", phone, response.Status, response.RawBody)
		os.Exit(1)
	}
	return token
}

func call(base, method, path, token string, body map[string]any, log func(string, ...any)) apiResponse {
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		log("request build failed: %v", err)
		os.Exit(1)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		log("request %s %s failed: %v", method, path, err)
		os.Exit(1)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	parsed := map[string]any{}
	_ = json.Unmarshal(raw, &parsed)
	log("%s %s -> %d", method, path, response.StatusCode)
	return apiResponse{Status: response.StatusCode, Body: parsed, RawBody: string(raw)}
}
