package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 210000
	pbkdf2KeyLength  = 32
)

type Principal struct {
	UserID    int64
	Nickname  string
	Phone     string
	IsDemo    bool
	Session   int64
	Staff     *StaffMembership
	TokenType string
}

type StaffMembership struct {
	ID      int64
	StoreID int64
	Version int64
	Roles   []string
}

func (p *Principal) HasCapability(capability string) bool {
	if p == nil || p.Staff == nil {
		return false
	}
	for _, role := range p.Staff.Roles {
		for _, allowed := range roleCapabilities[role] {
			if allowed == "*" || allowed == capability {
				return true
			}
		}
	}
	return false
}

var roleCapabilities = map[string][]string{
	"store_admin":        {"*"},
	"system_admin":       {"*"},
	"catalog_operator":   {"product_read", "product_write", "inventory_read"},
	"warehouse_operator": {"product_read", "inventory_read", "inventory_write"},
	"dispatcher":         {"order_read", "dispatch", "delivery_read", "urge_manage"},
	"courier":            {"courier"},
	"support":            {"order_read", "refund_read", "audit_read", "urge_manage"},
	"finance":            {"order_read", "refund_read", "refund_write", "report_read", "audit_read"},
}

type jwtClaims struct {
	Sub     string `json:"sub"`
	SV      int64  `json:"sv"`
	StoreID int64  `json:"storeId"`
	Typ     string `json:"typ"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Jti     string `json:"jti"`
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := randomRead(salt); err != nil {
		return "", err
	}
	derived := pbkdf2([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLength)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100000 || iterations > 2000000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	derived := pbkdf2([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(derived, expected) == 1
}

// pbkdf2 is a small RFC 8018 implementation to avoid an extra security
// dependency; it is only used for local/staff passwords and can be replaced
// with Argon2id once a maintained library is selected.
func pbkdf2(password, salt []byte, iterations, keyLength int) []byte {
	hashLength := sha256.Size
	blocks := (keyLength + hashLength - 1) / hashLength
	result := make([]byte, 0, blocks*hashLength)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		counter := make([]byte, 4)
		binary.BigEndian.PutUint32(counter, uint32(block))
		mac.Write(counter)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLength]
}

func randomRead(target []byte) (int, error) {
	// Kept separate so tests can reason about the source of entropy.
	return rand.Read(target)
}

func (a *App) issueToken(p *Principal) (string, error) {
	now := a.now().UTC()
	claims := jwtClaims{
		Sub:     strconv.FormatInt(p.UserID, 10),
		SV:      p.Session,
		Typ:     "access",
		Iat:     now.Unix(),
		Exp:     now.Add(a.cfg.AccessTokenTTL).Unix(),
		Jti:     newToken(12),
	}
	if p.Staff != nil {
		claims.StoreID = p.Staff.StoreID
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(a.cfg.JWTSecret))
	mac.Write([]byte(unsigned))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + signature, nil
}

func (a *App) parseToken(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.JWTSecret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(expected, actual) {
		return nil, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload")
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("invalid claims")
	}
	if claims.Typ != "access" || claims.Exp <= a.now().UTC().Unix() || claims.Sub == "" {
		return nil, errors.New("expired or invalid token")
	}
	return &claims, nil
}

func (a *App) loadPrincipal(ctx context.Context, userID int64, sessionVersion int64) (*Principal, error) {
	var p Principal
	var status string
	var defaultAddress sql.NullInt64
	err := a.db.QueryRowContext(ctx, `SELECT id, nickname, COALESCE(phone,''), status, session_version, is_demo, default_address_id
		FROM users WHERE id=?`, userID).Scan(&p.UserID, &p.Nickname, &p.Phone, &status, &p.Session, &p.IsDemo, &defaultAddress)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	if status != "ACTIVE" || p.Session != sessionVersion {
		return nil, errors.New("session revoked")
	}
	var membership StaffMembership
	var roles string
	err = a.db.QueryRowContext(ctx, `SELECT m.id, m.store_id, m.version, COALESCE(GROUP_CONCAT(r.role ORDER BY r.role), '')
		FROM staff_memberships m LEFT JOIN staff_roles r ON r.membership_id=m.id
		WHERE m.user_id=? AND m.active=1 GROUP BY m.id, m.store_id, m.version
		ORDER BY m.id LIMIT 1`, userID).Scan(&membership.ID, &membership.StoreID, &membership.Version, &roles)
	if err == nil {
		for _, role := range strings.Split(roles, ",") {
			if strings.TrimSpace(role) != "" {
				membership.Roles = append(membership.Roles, strings.TrimSpace(role))
			}
		}
		p.Staff = &membership
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return &p, nil
}

func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
			writeJSONError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
			return
		}
		claims, err := a.parseToken(strings.TrimSpace(header[7:]))
		if err != nil {
			writeJSONError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "登录状态已失效")
			return
		}
		userID, err := strconv.ParseInt(claims.Sub, 10, 64)
		if err != nil {
			writeJSONError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "登录状态已失效")
			return
		}
		principal, err := a.loadPrincipal(r.Context(), userID, claims.SV)
		if err != nil {
			writeJSONError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "登录状态已失效")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	}
}

type principalKey struct{}

func principalFrom(r *http.Request) *Principal {
	value, _ := r.Context().Value(principalKey{}).(*Principal)
	return value
}

func (a *App) staff(capability string, next http.HandlerFunc) http.HandlerFunc {
	return a.auth(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if p == nil || p.Staff == nil {
			writeJSONError(w, r, http.StatusForbidden, "STAFF_REQUIRED", "需要员工权限")
			return
		}
		if !p.HasCapability(capability) {
			writeJSONError(w, r, http.StatusForbidden, "CAPABILITY_REQUIRED", "没有该操作权限")
			return
		}
		next(w, r)
	})
}

type loginRequest struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	req.Phone = strings.TrimSpace(req.Phone)
	if req.Phone == "" || req.Password == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, "INVALID_CREDENTIALS", "请输入手机号和密码")
		return
	}
	if a.tooManyLoginFailures(r, req.Phone) {
		writeJSONError(w, r, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "尝试次数过多，请 15 分钟后再试")
		return
	}
	var userID int64
	var hash sql.NullString
	var status string
	var sessionVersion int64
	var nickname string
	var isDemo bool
	err := a.db.QueryRowContext(r.Context(), `SELECT id, nickname, password_hash, status, session_version, is_demo FROM users WHERE phone=?`, req.Phone).
		Scan(&userID, &nickname, &hash, &status, &sessionVersion, &isDemo)
	if errors.Is(err, sql.ErrNoRows) || !hash.Valid || !verifyPassword(hash.String, req.Password) || status != "ACTIVE" {
		a.recordLogin(r, req.Phone, false)
		writeJSONError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "手机号或密码不正确")
		return
	}
	if err != nil {
		writeError(w, r, &apiError{status: 500, code: "DB_ERROR", msg: "登录失败"})
		return
	}
	a.recordLogin(r, req.Phone, true)
	p, err := a.loadPrincipal(r.Context(), userID, sessionVersion)
	if err != nil {
		writeError(w, r, &apiError{status: 500, code: "DB_ERROR", msg: "登录失败"})
		return
	}
	p.Nickname = nickname
	p.IsDemo = isDemo
	token, err := a.issueToken(p)
	if err != nil {
		writeError(w, r, &apiError{status: 500, code: "TOKEN_ERROR", msg: "登录失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"accessToken": token,
		"user":        a.principalView(p),
	})
}

func (a *App) staffLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err.(*apiError))
		return
	}
	req.Phone = strings.TrimSpace(req.Phone)
	if req.Phone == "" || req.Password == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, "INVALID_CREDENTIALS", "请输入手机号和密码")
		return
	}
	if a.tooManyLoginFailures(r, req.Phone) {
		writeJSONError(w, r, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "尝试次数过多，请 15 分钟后再试")
		return
	}
	var userID int64
	var hash sql.NullString
	var status string
	var sessionVersion int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id, password_hash, status, session_version FROM users WHERE phone=?`, req.Phone).
		Scan(&userID, &hash, &status, &sessionVersion)
	if errors.Is(err, sql.ErrNoRows) || !hash.Valid || !verifyPassword(hash.String, req.Password) || status != "ACTIVE" {
		a.recordLogin(r, req.Phone, false)
		writeJSONError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "手机号或密码不正确")
		return
	}
	if err != nil {
		writeError(w, r, &apiError{status: 500, code: "DB_ERROR", msg: "登录失败"})
		return
	}
	p, err := a.loadPrincipal(r.Context(), userID, sessionVersion)
	if err != nil || p.Staff == nil {
		writeJSONError(w, r, http.StatusForbidden, "STAFF_REQUIRED", "该账号没有员工权限")
		return
	}
	a.recordLogin(r, req.Phone, true)
	token, err := a.issueToken(p)
	if err != nil {
		writeError(w, r, &apiError{status: 500, code: "TOKEN_ERROR", msg: "登录失败"})
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{
		"accessToken": token,
		"user":        a.principalView(p),
	})
}

func (a *App) principalView(p *Principal) map[string]any {
	view := map[string]any{
		"id":      toStringID(p.UserID),
		"nickname": p.Nickname,
		"phone":   p.Phone,
		"isDemo":  p.IsDemo,
		"roles":   []string{},
		"storeId": "",
	}
	if p.Staff != nil {
		view["roles"] = p.Staff.Roles
		view["storeId"] = toStringID(p.Staff.StoreID)
	}
	return view
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p == nil {
		writeJSONError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
		return
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), a.principalView(p))
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p != nil {
		_, _ = a.db.ExecContext(r.Context(), `UPDATE users SET session_version=session_version+1 WHERE id=?`, p.UserID)
	}
	writeJSON(w, http.StatusOK, traceIDFrom(r), map[string]any{"ok": true})
}

func (a *App) recordLogin(r *http.Request, identity string, success bool) {
	if identity == "" {
		return
	}
	_, _ = a.db.ExecContext(context.Background(), `INSERT INTO login_attempts(identity,ip,success) VALUES (?,?,?)`,
		identity, clientIP(r), boolInt(success))
}

func (a *App) tooManyLoginFailures(r *http.Request, identity string) bool {
	var count int
	err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM login_attempts
		WHERE identity=? AND success=0 AND created_at > DATE_SUB(NOW(6), INTERVAL 15 MINUTE)`, identity).Scan(&count)
	return err == nil && count >= 10
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return r.RemoteAddr
}
