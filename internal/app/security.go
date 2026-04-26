package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxNotifyURLLength = 2048
	maxReturnURLLength = 2048
	maxParamLength     = 256
	maxPayIDLength     = 128
	maxPriceYuan       = 1000000
	minAdminPassLength = 12
	maxAdminPassLength = 72
	minSharedKeyLength = 32
	maxOrderCloseMin   = 24 * 60
)

var moneyPattern = regexp.MustCompile(`^(0|[1-9]\d{0,7})(\.\d{1,2})?$`)

func ValidateConfig(cfg Config) error {
	if cfg.Port == "" {
		return errors.New("APP_PORT is required")
	}
	if cfg.SessionSecret == "" {
		return errors.New("SESSION_SECRET is required")
	}
	if cfg.BootstrapAdminUser == "" {
		return errors.New("ADMIN_USER is required")
	}
	if cfg.BootstrapAdminPass == "" {
		return errors.New("ADMIN_PASS is required")
	}
	if cfg.AllowInsecureDefaults {
		return nil
	}
	if len(cfg.SessionSecret) < 32 || strings.Contains(strings.ToLower(cfg.SessionSecret), "change-me") {
		return errors.New("SESSION_SECRET must be at least 32 characters and not use the default placeholder")
	}
	if cfg.BootstrapAdminUser == "admin" && cfg.BootstrapAdminPass == "admin" {
		return errors.New("ADMIN_USER/ADMIN_PASS must not use the default admin/admin credentials")
	}
	if len(cfg.BootstrapAdminPass) > maxAdminPassLength {
		return fmt.Errorf("ADMIN_PASS must not exceed %d characters", maxAdminPassLength)
	}
	if cfg.EpayMerchantKey != "" {
		if err := validateSharedSecret("EPAY_MERCHANT_KEY", cfg.EpayMerchantKey, cfg.AllowInsecureDefaults); err != nil {
			return err
		}
	}
	if cfg.EpayPublicBaseURL != "" {
		if err := validateCallbackURL(cfg.EpayPublicBaseURL, maxNotifyURLLength, false); err != nil {
			return fmt.Errorf("EPAY_PUBLIC_BASE_URL invalid: %w", err)
		}
	}
	return nil
}

func validateStoredSecurity(settings map[string]string, allowInsecure bool) error {
	if allowInsecure {
		return nil
	}
	user := settings["user"]
	pass := settings["pass"]
	if user == "" || pass == "" {
		return errors.New("admin credentials must be configured")
	}
	if user == "admin" && pass == "admin" {
		return errors.New("stored admin credentials must not use the default admin/admin values")
	}
	if len(pass) < minAdminPassLength {
		return errors.New("admin password must be at least 12 characters")
	}
	if err := validateSharedSecret("merchant key", settings["key"], allowInsecure); err != nil {
		return err
	}
	if err := validateSharedSecret("device key", settings["deviceKey"], allowInsecure); err != nil {
		return err
	}
	if settings["key"] == settings["deviceKey"] {
		return errors.New("device key must be different from merchant key")
	}
	if err := validateCloseMinutes(settings["close"]); err != nil {
		return err
	}
	if err := validatePayDirection(settings["payQf"]); err != nil {
		return err
	}
	return nil
}

func validateAdminSettingsInput(user, pass, notifyURL, returnURL string, allowInsecure, allowPrivateCallbacks bool) error {
	if len(user) == 0 || len(user) > maxPayIDLength {
		return fmt.Errorf("账号长度必须在1到%d之间", maxPayIDLength)
	}
	if pass != "" {
		if !allowInsecure && len(pass) < minAdminPassLength {
			return errors.New("后台密码长度不能少于12位")
		}
		if len(pass) > maxAdminPassLength {
			return fmt.Errorf("后台密码长度不能超过%d位", maxAdminPassLength)
		}
	}
	if notifyURL != "" {
		if err := validateOutboundCallbackURL(notifyURL, allowPrivateCallbacks); err != nil {
			return fmt.Errorf("notifyUrl不合法: %w", err)
		}
	}
	if returnURL != "" {
		if err := validateCallbackURL(returnURL, maxReturnURLLength, true); err != nil {
			return fmt.Errorf("returnUrl不合法: %w", err)
		}
	}
	return nil
}

func validateSharedSecret(label, value string, allowInsecure bool) error {
	if value == "" {
		return fmt.Errorf("%s must be configured", label)
	}
	if allowInsecure {
		return nil
	}
	if len(value) < minSharedKeyLength {
		return fmt.Errorf("%s must be at least %d characters", label, minSharedKeyLength)
	}
	return nil
}

func validateCloseMinutes(raw string) error {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return errors.New("订单有效期格式错误")
	}
	if value < 1 || value > maxOrderCloseMin {
		return fmt.Errorf("订单有效期必须在1到%d分钟之间", maxOrderCloseMin)
	}
	return nil
}

func validatePayDirection(raw string) error {
	if raw != "1" && raw != "2" {
		return errors.New("区分方式只允许1或2")
	}
	return nil
}

func validateCreateOrderInput(payID, param, priceRaw, notifyURL, returnURL string) error {
	if len(payID) == 0 || len(payID) > maxPayIDLength {
		return fmt.Errorf("payId length must be between 1 and %d", maxPayIDLength)
	}
	if len(param) > maxParamLength {
		return fmt.Errorf("param length must not exceed %d", maxParamLength)
	}
	if !moneyPattern.MatchString(priceRaw) {
		return errors.New("price format must be a positive amount with up to 2 decimals")
	}
	price, err := strconv.ParseFloat(priceRaw, 64)
	if err != nil {
		return errors.New("invalid price")
	}
	if price <= 0 || price > maxPriceYuan {
		return fmt.Errorf("price must be between 0.01 and %d", maxPriceYuan)
	}
	if notifyURL != "" {
		if err := validateCallbackURL(notifyURL, maxNotifyURLLength, true); err != nil {
			return fmt.Errorf("invalid notifyUrl: %w", err)
		}
	}
	if err := validateCallbackURL(returnURL, maxReturnURLLength, true); err != nil {
		return fmt.Errorf("invalid returnUrl: %w", err)
	}
	return nil
}

func validateCallbackURL(raw string, maxLength int, allowEmpty bool) error {
	if raw == "" {
		if allowEmpty {
			return nil
		}
		return errors.New("empty url")
	}
	if len(raw) > maxLength {
		return errors.New("url too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("malformed url")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("url scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("url host is required")
	}
	if u.User != nil {
		return errors.New("userinfo is not allowed")
	}
	return nil
}

func validateOutboundCallbackURL(raw string, allowPrivate bool) error {
	if err := validateCallbackURL(raw, maxNotifyURLLength, false); err != nil {
		return err
	}
	if allowPrivate {
		return nil
	}
	u, _ := url.Parse(raw)
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return errors.New("url host is required")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return errors.New("localhost callbacks are not allowed")
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if isPrivateIP(ip) {
			return errors.New("private network callbacks are not allowed")
		}
		return nil
	}

	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return errors.New("callback host lookup failed")
	}
	for _, addr := range ips {
		if isPrivateIP(addr.IP) {
			return errors.New("private network callbacks are not allowed")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	privateCIDRs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range privateCIDRs {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

func withinSignedRequestWindow(now time.Time, raw string, maxSkew time.Duration) bool {
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	diff := now.UnixMilli() - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= maxSkew.Milliseconds()
}

func orderAccessToken(orderID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(orderID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func newRandomHexSecret(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 32
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return md5Hex(fmt.Sprintf("%d", time.Now().UnixNano())) + md5Hex(fmt.Sprintf("%d", time.Now().UnixNano()+1))
}

func secureEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
