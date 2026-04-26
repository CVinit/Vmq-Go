package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const adminCookieName = "vmq_admin"

func (a *App) setAdminCookie(ctx context.Context, w http.ResponseWriter) error {
	ts := strconv.FormatInt(a.now().Unix(), 10)
	payload := "1:" + ts
	secret, err := a.adminCookieSecret(ctx)
	if err != nil {
		return err
	}
	sig := signCookie(payload, secret)
	value := base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + sig))
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.cfg.CookieSecure,
		Expires:  a.now().Add(a.cfg.AdminSessionTTL),
		MaxAge:   int(a.cfg.AdminSessionTTL / time.Second),
	})
	return nil
}

func (a *App) clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.cfg.CookieSecure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func (a *App) isAdmin(r *http.Request) bool {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false
	}

	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "1" {
		return false
	}

	payload := fmt.Sprintf("%s:%s", parts[0], parts[1])
	secret, err := a.adminCookieSecret(r.Context())
	if err != nil {
		return false
	}
	expected := signCookie(payload, secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return false
	}
	issuedAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	if a.now().After(time.Unix(issuedAt, 0).Add(a.cfg.AdminSessionTTL)) {
		return false
	}

	return true
}

func (a *App) adminCookieSecret(ctx context.Context) (string, error) {
	pass, err := a.store.GetSetting(ctx, "pass")
	if err != nil {
		return "", err
	}
	return a.cfg.SessionSecret + ":" + pass, nil
}

func signCookie(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
