package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is intentionally environment-driven. Secrets are never written to
// source, logs or the database. Production refuses to start with demo secrets.
type Config struct {
	Env             string
	HTTPAddr        string
	MySQLDSN        string
	JWTSecret       string
	AccessTokenTTL  time.Duration
	StoreID         int64
	PaymentMode     string // simulated | wechat | disabled
	AUTO_MIGRATE    bool
	MigrationsDir   string
	SeedDemo        bool
	DemoPassword    string
	WeChatMchID     string
	WeChatAppID     string
	WeChatSerialNo  string
	WeChatAPIv3Key  string
	WeChatPrivateKey string
	WeChatPlatformPublicKey string
	WeChatNotifyURL string
	OSSDir          string
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func Load() (Config, error) {
	envName := strings.ToLower(env("APP_ENV", "local"))
	if envName != "local" && envName != "test" && envName != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be local, test or production")
	}
	ttl, err := time.ParseDuration(env("ACCESS_TOKEN_TTL", "8h"))
	if err != nil {
		return Config{}, fmt.Errorf("ACCESS_TOKEN_TTL: %w", err)
	}
	storeID, err := strconv.ParseInt(env("STORE_ID", "1"), 10, 64)
	if err != nil || storeID <= 0 {
		return Config{}, fmt.Errorf("STORE_ID must be a positive integer")
	}
	paymentMode := strings.ToLower(env("PAYMENT_MODE", map[bool]string{true: "simulated", false: "disabled"}[envName == "local"]))
	if paymentMode != "simulated" && paymentMode != "wechat" && paymentMode != "disabled" {
		return Config{}, fmt.Errorf("PAYMENT_MODE must be simulated, wechat or disabled")
	}
	if envName == "production" && paymentMode == "simulated" {
		return Config{}, fmt.Errorf("production must not use simulated payments")
	}
	secret := env("JWT_SECRET", "")
	if envName == "production" && len(secret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must be at least 32 bytes in production")
	}
	if secret == "" {
		secret = "local-only-not-for-production-change-me"
	}
	cfg := Config{
		Env:                     envName,
		HTTPAddr:                env("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLDSN:                env("MYSQL_DSN", "campus:campus@tcp(127.0.0.1:3306)/campus_retail?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"),
		JWTSecret:               secret,
		AccessTokenTTL:          ttl,
		StoreID:                 storeID,
		PaymentMode:             paymentMode,
		AUTO_MIGRATE:            envBool("AUTO_MIGRATE", envName != "production"),
		MigrationsDir:           env("MIGRATIONS_DIR", "./db/migrations"),
		SeedDemo:                envBool("SEED_DEMO", false),
		DemoPassword:            env("DEMO_PASSWORD", "CampusDemo!2026"),
		WeChatMchID:             env("WECHAT_MCH_ID", ""),
		WeChatAppID:             env("WECHAT_APP_ID", ""),
		WeChatSerialNo:          env("WECHAT_MCH_SERIAL_NO", ""),
		WeChatAPIv3Key:          env("WECHAT_API_V3_KEY", ""),
		WeChatPrivateKey:        env("WECHAT_MCH_PRIVATE_KEY", ""),
		WeChatPlatformPublicKey: env("WECHAT_PLATFORM_PUBLIC_KEY", ""),
		WeChatNotifyURL:         env("WECHAT_NOTIFY_URL", ""),
		OSSDir:                  env("OSS_DIR", "./data/uploads"),
	}
	if cfg.SeedDemo && envName == "production" {
		return Config{}, fmt.Errorf("SEED_DEMO is forbidden in production")
	}
	if cfg.SeedDemo && cfg.DemoPassword == "" {
		return Config{}, fmt.Errorf("DEMO_PASSWORD is required when SEED_DEMO=true")
	}
	return cfg, nil
}
