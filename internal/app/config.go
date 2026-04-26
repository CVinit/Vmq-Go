package app

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                  string
	DatabaseURL           string
	SessionSecret         string
	BootstrapAdminUser    string
	BootstrapAdminPass    string
	WebRoot               string
	HTTPClientTimeout     time.Duration
	CookieSecure          bool
	AllowInsecureDefaults bool
	AllowPrivateCallbacks bool
	AdminSessionTTL       time.Duration
	TrustedProxyCIDRs     []string
	TrustCloudflareIPs    bool
}

func LoadConfig() Config {
	cfg := Config{
		Port:                  getEnv("APP_PORT", "8080"),
		DatabaseURL:           getEnv("DATABASE_URL", "postgres://vmq:vmq@postgres:5432/vmq?sslmode=disable"),
		SessionSecret:         getEnv("SESSION_SECRET", "change-me-to-a-long-random-string"),
		BootstrapAdminUser:    getEnv("ADMIN_USER", "admin"),
		BootstrapAdminPass:    getEnv("ADMIN_PASS", "admin"),
		WebRoot:               getEnv("BASE_WEB_PATH", "src/main/webapp"),
		HTTPClientTimeout:     10 * time.Second,
		CookieSecure:          getEnv("COOKIE_SECURE", "") == "1",
		AllowInsecureDefaults: getEnv("ALLOW_INSECURE_DEV_DEFAULTS", "") == "1",
		AllowPrivateCallbacks: getEnv("ALLOW_PRIVATE_CALLBACKS", "") == "1",
		AdminSessionTTL:       30 * 24 * time.Hour,
		TrustedProxyCIDRs:     splitCSVEnv("TRUSTED_PROXY_CIDRS"),
		TrustCloudflareIPs:    getEnv("TRUST_CLOUDFLARE_IPS", "") == "1",
	}

	if raw := os.Getenv("HTTP_CLIENT_TIMEOUT"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			cfg.HTTPClientTimeout = time.Duration(seconds) * time.Second
		}
	}
	if raw := os.Getenv("ADMIN_SESSION_TTL_HOURS"); raw != "" {
		if hours, err := strconv.Atoi(raw); err == nil && hours > 0 {
			cfg.AdminSessionTTL = time.Duration(hours) * time.Hour
		}
	}

	return cfg
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitCSVEnv(key string) []string {
	value := os.Getenv(key)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
