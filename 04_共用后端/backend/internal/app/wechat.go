package app

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"campusretail/internal/config"
)

type wechatClient struct {
	cfg         config.Config
	http        *http.Client
	privateKey  *rsa.PrivateKey
	platformKey *rsa.PublicKey
}

type prepayInput struct {
	OrderNo     string
	AmountCents int64
	Description string
	Channel     string
	OpenID      string
}

type prepayResult struct {
	PrepayID    string
	Params      map[string]any
	ExpiresAt   time.Time
}

func newWeChatClient(cfg config.Config) *wechatClient {
	client := &wechatClient{cfg: cfg, http: &http.Client{Timeout: 8 * time.Second}}
	if cfg.WeChatPrivateKey != "" {
		key, err := loadPrivateKey(cfg.WeChatPrivateKey)
		if err == nil {
			client.privateKey = key
		}
	}
	if cfg.WeChatPlatformPublicKey != "" {
		key, err := loadPublicKey(cfg.WeChatPlatformPublicKey)
		if err == nil {
			client.platformKey = key
		}
	}
	return client
}

func (c *wechatClient) configured() bool {
	return c != nil && c.cfg.WeChatMchID != "" && c.cfg.WeChatAppID != "" && c.privateKey != nil &&
		c.cfg.WeChatAPIv3Key != "" && c.cfg.WeChatSerialNo != "" && c.cfg.WeChatNotifyURL != ""
}

func loadPrivateKey(value string) (*rsa.PrivateKey, error) {
	body := []byte(value)
	if !strings.Contains(value, "BEGIN") {
		read, err := os.ReadFile(value)
		if err != nil {
			return nil, err
		}
		body = read
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func loadPublicKey(value string) (*rsa.PublicKey, error) {
	if strings.Contains(value, "BEGIN") {
		block, _ := pem.Decode([]byte(value))
		if block == nil {
			return nil, errors.New("invalid public key PEM")
		}
		if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if rsaKey, ok := key.(*rsa.PublicKey); ok {
				return rsaKey, nil
			}
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			if rsaKey, ok := cert.PublicKey.(*rsa.PublicKey); ok {
				return rsaKey, nil
			}
		}
		return nil, errors.New("invalid public key")
	}
	cert, err := os.ReadFile(value)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return nil, errors.New("invalid certificate")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := parsed.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("certificate is not RSA")
	}
	return rsaKey, nil
}

func (c *wechatClient) createPrepay(ctx context.Context, in prepayInput) (*prepayResult, error) {
	if !c.configured() {
		return nil, errNotConfigured
	}
	if in.Channel == "WECHAT_JSAPI" || in.Channel == "WECHAT_MINI" {
		if in.OpenID == "" {
			return nil, errors.New("微信用户身份未绑定，无法发起 JSAPI/小程序支付")
		}
		path := "/v3/pay/transactions/jsapi"
		body := map[string]any{
			"appid": c.cfg.WeChatAppID, "mchid": c.cfg.WeChatMchID, "description": in.Description,
			"out_trade_no": in.OrderNo, "notify_url": c.cfg.WeChatNotifyURL,
			"amount": map[string]any{"total": in.AmountCents, "currency": "CNY"},
			"payer":  map[string]any{"openid": in.OpenID},
		}
		raw, _ := json.Marshal(body)
		response, err := c.request(ctx, http.MethodPost, path, raw)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			PrepayID string `json:"prepay_id"`
		}
		if err := json.Unmarshal(response, &parsed); err != nil || parsed.PrepayID == "" {
			return nil, errors.New("微信未返回 prepay_id")
		}
		now := time.Now()
		nonce := nonceString(16)
		pkg := "prepay_id=" + parsed.PrepayID
		message := c.cfg.WeChatAppID + "\n" + strconv.FormatInt(now.Unix(), 10) + "\n" + nonce + "\n" + pkg + "\n"
		signature, err := c.sign([]byte(message))
		if err != nil {
			return nil, err
		}
		return &prepayResult{
			PrepayID: parsed.PrepayID,
			Params: map[string]any{
				"timeStamp": strconv.FormatInt(now.Unix(), 10), "nonceStr": nonce,
				"package": pkg, "signType": "RSA", "paySign": signature,
			},
			ExpiresAt: now.Add(10 * time.Minute),
		}, nil
	}
	// H5 payment. The JSAPI/小程序 identity and AppID binding must be supplied
	// by a verified WeChat login, never by the client.
	path := "/v3/pay/transactions/h5"
	body := map[string]any{
		"appid": c.cfg.WeChatAppID, "mchid": c.cfg.WeChatMchID, "description": in.Description,
		"out_trade_no": in.OrderNo, "notify_url": c.cfg.WeChatNotifyURL,
		"amount": map[string]any{"total": in.AmountCents, "currency": "CNY"},
		"scene_info": map[string]any{"payer_client_ip": "127.0.0.1", "h5_info": map[string]any{"type": "Wap"}},
	}
	raw, _ := json.Marshal(body)
	response, err := c.request(ctx, http.MethodPost, path, raw)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		H5URL string `json:"h5_url"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil || parsed.H5URL == "" {
		return nil, errors.New("微信未返回 h5_url")
	}
	return &prepayResult{Params: map[string]any{"h5_url": parsed.H5URL}, ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
}

func (c *wechatClient) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if c.privateKey == nil {
		return nil, errNotConfigured
	}
	nonce := nonceString(16)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	signature, err := c.sign([]byte(message))
	if err != nil {
		return nil, err
	}
	authorization := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`,
		c.cfg.WeChatMchID, nonce, signature, timestamp, c.cfg.WeChatSerialNo)
	req, err := http.NewRequestWithContext(ctx, method, "https://api.mch.weixin.qq.com"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	response, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wechat pay HTTP %d: %s", resp.StatusCode, truncate(string(response), 300))
	}
	return response, nil
}

func (c *wechatClient) sign(message []byte) (string, error) {
	if c.privateKey == nil {
		return "", errNotConfigured
	}
	digest := sha256.Sum256(message)
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (c *wechatClient) verifyNotification(headers http.Header, body []byte) (wechatEvent, error) {
	if !c.configured() || c.platformKey == nil {
		return wechatEvent{}, errNotConfigured
	}
	timestamp := headers.Get("Wechatpay-Timestamp")
	nonce := headers.Get("Wechatpay-Nonce")
	signatureText := headers.Get("Wechatpay-Signature")
	if timestamp == "" || nonce == "" || signatureText == "" {
		return wechatEvent{}, errors.New("missing WeChat Pay signature headers")
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		return wechatEvent{}, errors.New("invalid signature encoding")
	}
	message := timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	digest := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(c.platformKey, crypto.SHA256, digest[:], signature); err != nil {
		return wechatEvent{}, errors.New("WeChat Pay signature verification failed")
	}
	var envelope struct {
		ID        string `json:"id"`
		EventType string `json:"event_type"`
		Resource  struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wechatEvent{}, err
	}
	if envelope.ID == "" || envelope.Resource.Ciphertext == "" {
		return wechatEvent{}, errors.New("invalid notification body")
	}
	plain, err := decryptResource(c.cfg.WeChatAPIv3Key, envelope.Resource.Nonce, envelope.Resource.AssociatedData, envelope.Resource.Ciphertext)
	if err != nil {
		return wechatEvent{}, err
	}
	return wechatEvent{ID: envelope.ID, Type: envelope.EventType, Raw: plain}, nil
}

type wechatEvent struct {
	ID   string
	Type string
	Raw  []byte
}

func decryptResource(apiKey, nonce, associatedData, ciphertext string) ([]byte, error) {
	key := []byte(apiKey)
	if len(key) != 32 {
		return nil, errors.New("APIv3 key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, []byte(nonce), data, []byte(associatedData))
}

func nonceString(length int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, length)
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	for i, value := range random {
		buf[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(buf)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
