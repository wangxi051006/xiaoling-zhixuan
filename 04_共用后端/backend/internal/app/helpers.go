package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func badRequest(code, msg string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, code: code, msg: msg}
}

func conflict(code, msg string) *apiError {
	return &apiError{status: http.StatusConflict, code: code, msg: msg}
}

func notFound(msg string) *apiError {
	return &apiError{status: http.StatusNotFound, code: "NOT_FOUND", msg: msg}
}

func forbidden(msg string) *apiError {
	return &apiError{status: http.StatusForbidden, code: "FORBIDDEN", msg: msg}
}

func unavailable(code, msg string) *apiError {
	return &apiError{status: http.StatusServiceUnavailable, code: code, msg: msg}
}

func writeJSON(w http.ResponseWriter, status int, traceID string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Code: "OK", Data: data, TraceID: traceID})
}

func writeError(w http.ResponseWriter, r *http.Request, err *apiError) {
	writeJSONError(w, r, err.status, err.code, err.msg)
}

func writeJSONError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Code: code, Message: message, TraceID: traceIDFrom(r)})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if r.Body == nil {
		return badRequest("BODY_REQUIRED", "请求体不能为空")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return badRequest("INVALID_JSON", "请求体不是合法 JSON 或包含未知字段")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return badRequest("INVALID_JSON", "请求体只能包含一个 JSON 对象")
	}
	return nil
}

func parseID(value, field string) (int64, error) {
	value = strings.TrimSpace(value)
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("INVALID_ID", field+" 必须是正整数")
	}
	return id, nil
}

func toStringID(value int64) string { return strconv.FormatInt(value, 10) }

func newToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randomDigits(length int) string {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	digits := make([]byte, length)
	for i, value := range buf {
		digits[i] = byte('0' + int(value)%10)
	}
	return string(digits)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hashCanonical(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(body), nil
}

func traceIDFrom(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-Id")); value != "" && len(value) <= 64 {
		return value
	}
	value, _ := r.Context().Value(traceKey{}).(string)
	return value
}

type traceKey struct{}

func newTraceID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func loadShanghai() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

func (a *App) todayShanghai() string {
	return time.Now().In(a.tz).Format("2006-01-02")
}

func (a *App) dateWindow(date string) (time.Time, time.Time, string, error) {
	date = strings.TrimSpace(date)
	if date == "" {
		date = a.todayShanghai()
	}
	start, err := time.ParseInLocation("2006-01-02", date, a.tz)
	if err != nil {
		return time.Time{}, time.Time{}, "", badRequest("INVALID_DATE", "日期必须是 YYYY-MM-DD")
	}
	// The report SQL uses DATE(paid_at + 8h); returning the Shanghai boundaries
	// keeps API callers and SQL on the same natural-day definition.
	return start.UTC(), start.AddDate(0, 0, 1).UTC(), date, nil
}

func (a *App) formatTime(t time.Time) string { return t.In(a.tz).Format(time.RFC3339) }

func trimRemark(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func validRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizeItems(items []ItemInput) ([]ItemInput, error) {
	if len(items) == 0 {
		return nil, badRequest("ITEMS_REQUIRED", "至少选择一件商品")
	}
	merged := make(map[int64]int)
	order := make([]int64, 0, len(items))
	for _, item := range items {
		skuID, err := parseID(item.SkuID, "skuId")
		if err != nil {
			return nil, err
		}
		if item.Quantity < 1 || item.Quantity > 99 {
			return nil, badRequest("INVALID_QUANTITY", "每件商品数量必须在 1 到 99 之间")
		}
		if _, exists := merged[skuID]; !exists {
			order = append(order, skuID)
		}
		merged[skuID] += item.Quantity
		if merged[skuID] > 99 {
			return nil, badRequest("INVALID_QUANTITY", "同一 SKU 合并后不能超过 99 件")
		}
	}
	if len(order) > 20 {
		return nil, badRequest("TOO_MANY_SKUS", "每单最多 20 种不同 SKU")
	}
	result := make([]ItemInput, 0, len(order))
	for _, skuID := range order {
		result = append(result, ItemInput{SkuID: toStringID(skuID), Quantity: merged[skuID]})
	}
	return result, nil
}

func encodeCursor(createdAt time.Time, id int64) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(value string) (time.Time, int64, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, 0, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, 0, badRequest("INVALID_CURSOR", "分页游标无效")
	}
	parts := strings.SplitN(string(body), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, badRequest("INVALID_CURSOR", "分页游标无效")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, 0, badRequest("INVALID_CURSOR", "分页游标无效")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, badRequest("INVALID_CURSOR", "分页游标无效")
	}
	return createdAt, id, nil
}

func pageLimit(value string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func isOneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func scanJSON(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(value)
}

func joinErrors(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "duplicate entry") || strings.Contains(text, "error 1062")
}

func allowedOrderActions(status, paymentStatus string, isAdmin bool) []string {
	actions := []string{}
	switch status {
	case "PENDING_PAYMENT":
		if isAdmin {
			actions = append(actions, "CLOSE")
		} else {
			actions = append(actions, "PAY", "CANCEL")
		}
	case "PAID":
		if isAdmin {
			actions = append(actions, "ACCEPT", "REFUND")
		}
	case "PICKING":
		if isAdmin {
			actions = append(actions, "READY")
		}
	case "READY":
		if isAdmin {
			actions = append(actions, "ASSIGN_DELIVERY")
		}
	case "DELIVERING":
		if isAdmin {
			actions = append(actions, "REFUND", "REASSIGN")
		}
	case "COMPLETED":
		if isAdmin {
			actions = append(actions, "REFUND")
		}
	case "PAYMENT_EXCEPTION":
		if isAdmin {
			actions = append(actions, "RESOLVE_PAYMENT")
		}
	}
	if paymentStatus == "PAID" || paymentStatus == "PARTIAL_REFUNDED" {
		// refundable even after completion; explicit endpoint still rechecks.
		if isAdmin && !isOneOf("REFUND", actions...) {
			actions = append(actions, "REFUND")
		}
	}
	return actions
}

var errNotConfigured = errors.New("payment channel not configured")
