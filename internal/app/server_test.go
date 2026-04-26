package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBootstrapDefaults(t *testing.T) {
	app := newTestApp(t)
	settings, err := app.store.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings returned error: %v", err)
	}

	expectedKeys := []string{
		"user", "pass", "notifyUrl", "returnUrl", "key",
		"lastheart", "lastpay", "jkstate", "close", "payQf", "wxpay", "zfbpay",
	}
	for _, key := range expectedKeys {
		if _, ok := settings[key]; !ok {
			t.Fatalf("expected bootstrap key %q to exist", key)
		}
	}
	if settings["user"] != "rootadmin" {
		t.Fatalf("unexpected admin defaults: %#v", settings)
	}
	if settings["pass"] == "StrongPass!123456" {
		t.Fatalf("expected bootstrap password to be stored as a hash")
	}
	if !verifyAdminPassword(settings["pass"], "StrongPass!123456") {
		t.Fatalf("expected bootstrap password hash to verify")
	}
}

func TestBootstrapDefaultsCreatesSeparateDeviceKey(t *testing.T) {
	app := newTestApp(t)
	settings, err := app.store.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings returned error: %v", err)
	}

	deviceKey := settings["deviceKey"]
	if len(deviceKey) < 32 {
		t.Fatalf("expected deviceKey to be initialized with strong entropy, got %q", deviceKey)
	}
	if deviceKey == settings["key"] {
		t.Fatalf("expected deviceKey to be distinct from merchant key")
	}
}

func TestAdminGetMenuUnauthenticatedReturnsNull(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/getMenu", nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("expected null body, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected unauthenticated admin menu response to disable caching, got %q", got)
	}
}

func TestLoginRequiresPost(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestLogoutClearsAdminCookie(t *testing.T) {
	app := newTestApp(t)
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := cookieRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Host = "vmq.example.com"
	req.Header.Set("Origin", "https://vmq.example.com")
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	foundCleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == adminCookieName && cookie.MaxAge < 0 {
			foundCleared = true
		}
	}
	if !foundCleared {
		t.Fatal("expected logout to clear admin cookie")
	}
}

func TestAdminEndpointRequiresPost(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/getSettings", nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestAdminEndpointRejectsCrossOriginRequest(t *testing.T) {
	app := newTestApp(t)
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := cookieRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/getSettings", nil)
	req.Host = "vmq.example.com"
	req.Header.Set("Origin", "https://evil.example.net")
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestSensitiveJSONResponsesDisableCaching(t *testing.T) {
	app := newTestApp(t)
	form := url.Values{}
	form.Set("user", "rootadmin")
	form.Set("pass", "StrongPass!123456")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected no-store cache header, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", got)
	}
}

func TestAdminPagesSetSecurityHeaders(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{"/", "/index.html", "/aaa.html", "/admin/setting.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		app.Handler().ServeHTTP(rec, req)

		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Fatalf("expected %s to disable caching, got %q", path, got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Fatalf("expected %s to deny framing, got %q", path, got)
		}
	}
}

func TestPayPageDisablesCaching(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/payPage/pay.html", nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected pay page to disable caching, got %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("expected pay page not to set frame blocking header, got %q", got)
	}
}

func TestCreateOrderHTMLResponseDisablesCaching(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{
		"wxpay": "weixin://test-pay",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	key, err := app.store.GetSetting(ctx, "key")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}

	form := url.Values{}
	form.Set("payId", "merchant-html")
	form.Set("param", "demo")
	form.Set("type", "1")
	form.Set("price", "0.1")
	form.Set("isHtml", "1")
	form.Set("sign", md5Hex("merchant-htmldemo10.1"+key))
	req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected html createOrder response to disable caching, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "/payPage/pay.html?orderId=") {
		t.Fatalf("expected html createOrder response to redirect to pay page, got %q", rec.Body.String())
	}
}

func TestQRCodeImageDisablesCaching(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/enQrcode?url="+url.QueryEscape("weixin://test-pay"), nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected qrcode image response to disable caching, got %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("expected png content type, got %q", got)
	}
}

func TestStaticEntryRoutesServeIndex(t *testing.T) {
	app := newTestApp(t)

	for _, path := range []string{"/", "/index.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		app.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title>V免签</title>") {
			t.Fatalf("expected index content for %s", path)
		}
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	app := newTestApp(t)
	form := url.Values{}
	form.Set("user", "rootadmin")
	form.Set("pass", "StrongPass!123456")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected login success, got %+v", payload)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected login to set a cookie")
	}
}

func TestLoginMigratesLegacyPlaintextPassword(t *testing.T) {
	app := newTestApp(t)
	if err := app.store.UpsertSettings(context.Background(), map[string]string{
		"user": "rootadmin",
		"pass": "LegacyPass!123",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}

	form := url.Values{}
	form.Set("user", "rootadmin")
	form.Set("pass", "LegacyPass!123")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected legacy password login success, got %+v", payload)
	}

	storedPass, err := app.store.GetSetting(context.Background(), "pass")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}
	if storedPass == "LegacyPass!123" {
		t.Fatal("expected legacy plaintext password to be migrated to hash after login")
	}
	if !verifyAdminPassword(storedPass, "LegacyPass!123") {
		t.Fatal("expected migrated password hash to verify")
	}
}

func TestLoginBlocksAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t)
	now := time.UnixMilli(1713024000000)
	app.now = func() time.Time {
		return now
	}

	failAttempt := func(user, pass string) CommonRes {
		form := url.Values{}
		form.Set("user", user)
		form.Set("pass", pass)
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.10:54321"
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)

		var payload CommonRes
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal login response: %v", err)
		}
		return payload
	}

	for i := 0; i < 5; i++ {
		payload := failAttempt("rootadmin", "bad-password")
		if payload.Code != -1 {
			t.Fatalf("expected failed login on attempt %d, got %+v", i+1, payload)
		}
	}

	blocked := failAttempt("rootadmin", "StrongPass!123456")
	if blocked.Code != -1 {
		t.Fatalf("expected login to be temporarily blocked after repeated failures, got %+v", blocked)
	}

	now = now.Add(16 * time.Minute)
	recovered := failAttempt("rootadmin", "StrongPass!123456")
	if recovered.Code != 1 {
		t.Fatalf("expected login to recover after throttle window, got %+v", recovered)
	}
}

func TestClientIPUsesTrustedProxyHeaders(t *testing.T) {
	app := newTestApp(t)
	app.clientIPs = mustClientIPResolver(t, Config{
		TrustedProxyCIDRs: []string{"172.18.0.0/16"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.10:8080"
	req.Header.Set("X-Real-IP", "198.51.100.20")
	req.Header.Set("X-Forwarded-For", "198.51.100.20, 172.18.0.10")

	if got := app.clientIP(req); got != "198.51.100.20" {
		t.Fatalf("expected trusted proxy client ip, got %s", got)
	}
}

func TestClientIPIgnoresSpoofedProxyHeadersFromUntrustedSource(t *testing.T) {
	app := newTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.30:8080"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := app.clientIP(req); got != "198.51.100.30" {
		t.Fatalf("expected untrusted source to use remote ip, got %s", got)
	}
}

func TestClientIPUsesCloudflareConnectingIPWhenEnabled(t *testing.T) {
	app := newTestApp(t)
	app.clientIPs = mustClientIPResolver(t, Config{
		TrustCloudflareIPs: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "173.245.48.5:443"
	req.Header.Set("CF-Connecting-IP", "203.0.113.55")

	if got := app.clientIP(req); got != "203.0.113.55" {
		t.Fatalf("expected cloudflare client ip, got %s", got)
	}
}

func TestLoginThrottleUsesTrustedProxyClientIP(t *testing.T) {
	app := newTestApp(t)
	app.clientIPs = mustClientIPResolver(t, Config{
		TrustedProxyCIDRs: []string{"172.18.0.0/16"},
	})
	now := time.UnixMilli(1713024000000)
	app.now = func() time.Time {
		return now
	}

	attempt := func(clientIP, pass string) CommonRes {
		form := url.Values{}
		form.Set("user", "rootadmin")
		form.Set("pass", pass)
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "172.18.0.10:8080"
		req.Header.Set("X-Real-IP", clientIP)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)

		var payload CommonRes
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal login response: %v", err)
		}
		return payload
	}

	for i := 0; i < 5; i++ {
		payload := attempt("198.51.100.80", "bad-password")
		if payload.Code != -1 {
			t.Fatalf("expected failed login on attempt %d, got %+v", i+1, payload)
		}
	}

	blocked := attempt("198.51.100.80", "StrongPass!123456")
	if blocked.Code != -1 {
		t.Fatalf("expected trusted proxy client ip to be throttled, got %+v", blocked)
	}

	other := attempt("198.51.100.81", "StrongPass!123456")
	if other.Code != 1 {
		t.Fatalf("expected different trusted proxy client ip not to be blocked, got %+v", other)
	}
}

func TestCreateOrderRejectsBadSignature(t *testing.T) {
	app := newTestApp(t)
	if err := app.store.UpsertSettings(context.Background(), map[string]string{
		"wxpay":  "weixin://test-pay",
		"zfbpay": "alipay://test-pay",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}

	form := url.Values{}
	form.Set("payId", "merchant-1")
	form.Set("param", "demo")
	form.Set("type", "1")
	form.Set("price", "0.10")
	form.Set("sign", "bad-sign")
	req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal createOrder response: %v", err)
	}
	if payload.Code != -1 || payload.Msg != "签名校验不通过" {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

func TestCreateOrderAndGetOrderCompatibility(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{
		"wxpay": "weixin://test-pay",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	key, err := app.store.GetSetting(ctx, "key")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}

	form := url.Values{}
	form.Set("payId", "merchant-compat")
	form.Set("param", "demo")
	form.Set("type", "1")
	form.Set("price", "0.1")
	form.Set("sign", md5Hex("merchant-compatdemo10.1"+key))
	req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	var createPayload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("unmarshal createOrder response: %v", err)
	}
	if createPayload.Code != 1 {
		t.Fatalf("expected success, got %+v", createPayload)
	}

	dataBytes, err := json.Marshal(createPayload.Data)
	if err != nil {
		t.Fatalf("marshal createOrder data: %v", err)
	}
	var orderData CreateOrderRes
	if err := json.Unmarshal(dataBytes, &orderData); err != nil {
		t.Fatalf("unmarshal createOrder data: %v", err)
	}
	if orderData.OrderID == "" || orderData.PayURL != "weixin://test-pay" {
		t.Fatalf("unexpected createOrder data: %+v", orderData)
	}
	if orderData.AccessToken == "" {
		t.Fatalf("expected access token, got %+v", orderData)
	}

	getReq := httptest.NewRequest(http.MethodPost, "/getOrder", strings.NewReader(url.Values{
		"orderId": []string{orderData.OrderID},
		"token":   []string{orderData.AccessToken},
	}.Encode()))
	getReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	getRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(getRec, getReq)

	var getPayload CommonRes
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("unmarshal getOrder response: %v", err)
	}
	if getPayload.Code != 1 {
		t.Fatalf("expected getOrder success, got %+v", getPayload)
	}
}

func TestGetOrderRejectsUnsignedRequest(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/getOrder", strings.NewReader(url.Values{"orderId": []string{"202604140001"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal getOrder response: %v", err)
	}
	if payload.Code != -1 || payload.Msg != "请传入签名" {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

func TestAppPushWithoutMatchingOrderIsRejected(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	key, err := app.store.GetSetting(ctx, "deviceKey")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}
	timestamp := strconv.FormatInt(app.now().UnixMilli(), 10)

	res := app.handleAppPushLogic(ctx, 1, "9.99", timestamp, md5Hex("19.99"+timestamp+key))
	if res.Code != -1 || res.Msg != "未匹配到待支付订单" {
		t.Fatalf("unexpected response: %+v", res)
	}

	orders, _, err := app.store.ListOrders(ctx, 1, 10, OrderFilter{})
	if err != nil {
		t.Fatalf("ListOrders returned error: %v", err)
	}
	if len(orders) != 0 {
		t.Fatalf("expected no orders to be created, got %+v", orders)
	}
}

func TestAppPushReplayDoesNotPaySecondOrder(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	ctx := context.Background()
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("success"))
	}))
	defer callback.Close()
	if err := app.store.UpsertSettings(ctx, map[string]string{
		"wxpay":     "weixin://test-pay",
		"notifyUrl": callback.URL,
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	key, err := app.store.GetSetting(ctx, "key")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}
	deviceKey, err := app.store.GetSetting(ctx, "deviceKey")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}

	createReq := func(payID string) CreateOrderRes {
		form := url.Values{}
		form.Set("payId", payID)
		form.Set("param", "demo")
		form.Set("type", "1")
		form.Set("price", "9.99")
		form.Set("sign", md5Hex(payID+"demo19.99"+key))
		req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)

		var payload CommonRes
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal createOrder response: %v", err)
		}
		if payload.Code != 1 {
			t.Fatalf("expected createOrder success, got %+v", payload)
		}
		dataBytes, err := json.Marshal(payload.Data)
		if err != nil {
			t.Fatalf("marshal createOrder data: %v", err)
		}
		var orderData CreateOrderRes
		if err := json.Unmarshal(dataBytes, &orderData); err != nil {
			t.Fatalf("unmarshal createOrder data: %v", err)
		}
		return orderData
	}

	first := createReq("merchant-1")
	timestamp := strconv.FormatInt(app.now().UnixMilli(), 10)
	pushSign := md5Hex("19.99" + timestamp + deviceKey)

	res := app.handleAppPushLogic(ctx, 1, "9.99", timestamp, pushSign)
	if res.Code != 1 {
		t.Fatalf("expected first push success, got %+v", res)
	}

	second := createReq("merchant-2")
	replay := app.handleAppPushLogic(ctx, 1, "9.99", timestamp, pushSign)
	if replay.Code != -1 || replay.Msg != "重复推送" {
		t.Fatalf("expected replay to be rejected, got %+v", replay)
	}

	order, err := app.store.GetOrderByOrderID(ctx, second.OrderID)
	if err != nil {
		t.Fatalf("GetOrderByOrderID returned error: %v", err)
	}
	if order == nil || order.State != 0 {
		t.Fatalf("expected second order to remain pending, got %+v", order)
	}

	paidOrder, err := app.store.GetOrderByOrderID(ctx, first.OrderID)
	if err != nil {
		t.Fatalf("GetOrderByOrderID returned error: %v", err)
	}
	if paidOrder == nil || paidOrder.State != 1 || paidOrder.PayDate != app.now().UnixMilli() {
		t.Fatalf("expected first order to be marked paid with push timestamp, got %+v", paidOrder)
	}
}

func TestAppPushDoesNotAcceptMerchantKey(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	ctx := context.Background()
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("success"))
	}))
	defer callback.Close()

	if err := app.store.UpsertSettings(ctx, map[string]string{
		"wxpay":     "weixin://test-pay",
		"notifyUrl": callback.URL,
		"deviceKey": "monitor-secret-1234567890monitor-secret",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	merchantKey, err := app.store.GetSetting(ctx, "key")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}
	deviceKey, err := app.store.GetSetting(ctx, "deviceKey")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}

	createReq := func(payID string) CreateOrderRes {
		form := url.Values{}
		form.Set("payId", payID)
		form.Set("param", "demo")
		form.Set("type", "1")
		form.Set("price", "9.99")
		form.Set("sign", md5Hex(payID+"demo19.99"+merchantKey))
		req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)

		var payload CommonRes
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal createOrder response: %v", err)
		}
		if payload.Code != 1 {
			t.Fatalf("expected createOrder success, got %+v", payload)
		}
		dataBytes, err := json.Marshal(payload.Data)
		if err != nil {
			t.Fatalf("marshal createOrder data: %v", err)
		}
		var orderData CreateOrderRes
		if err := json.Unmarshal(dataBytes, &orderData); err != nil {
			t.Fatalf("unmarshal createOrder data: %v", err)
		}
		return orderData
	}

	first := createReq("merchant-push-1")
	timestamp := strconv.FormatInt(app.now().UnixMilli(), 10)
	merchantPush := app.handleAppPushLogic(ctx, 1, "9.99", timestamp, md5Hex("19.99"+timestamp+merchantKey))
	if merchantPush.Code != -1 || merchantPush.Msg != "签名校验错误" {
		t.Fatalf("expected merchant key push to be rejected, got %+v", merchantPush)
	}
	firstOrder, err := app.store.GetOrderByOrderID(ctx, first.OrderID)
	if err != nil {
		t.Fatalf("GetOrderByOrderID returned error: %v", err)
	}
	if firstOrder == nil || firstOrder.State != 0 {
		t.Fatalf("expected first order to remain pending, got %+v", firstOrder)
	}

	second := createReq("merchant-push-2")
	devicePush := app.handleAppPushLogic(ctx, 1, "10", timestamp, md5Hex("110"+timestamp+deviceKey))
	if devicePush.Code != 1 {
		t.Fatalf("expected device key push to succeed, got %+v", devicePush)
	}
	secondOrder, err := app.store.GetOrderByOrderID(ctx, second.OrderID)
	if err != nil {
		t.Fatalf("GetOrderByOrderID returned error: %v", err)
	}
	if secondOrder == nil || secondOrder.State != 1 {
		t.Fatalf("expected second order to be paid by device push, got %+v", secondOrder)
	}
}

func TestAdminSaveSettingRejectsWeakSecurityConfig(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowInsecureDefaults = false
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := cookieRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}

	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "short")
	form.Set("notifyUrl", "https://example.com/callback")
	form.Set("returnUrl", "https://merchant.example.com/return")
	form.Set("key", "weak")
	form.Set("wxpay", "weixin://pay")
	form.Set("zfbpay", "alipay://pay")
	form.Set("close", "5")
	form.Set("payQf", "1")

	req := httptest.NewRequest(http.MethodPost, "/admin/saveSetting", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal saveSetting response: %v", err)
	}
	if payload.Code != -1 {
		t.Fatalf("expected weak admin settings to be rejected, got %+v", payload)
	}
}

func TestAdminGetSettingsDoesNotExposePassword(t *testing.T) {
	app := newTestApp(t)
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := cookieRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/getSettings", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal getSettings response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected getSettings success, got %+v", payload)
	}
	dataBytes, err := json.Marshal(payload.Data)
	if err != nil {
		t.Fatalf("marshal getSettings data: %v", err)
	}
	var settings map[string]string
	if err := json.Unmarshal(dataBytes, &settings); err != nil {
		t.Fatalf("unmarshal getSettings data: %v", err)
	}
	if settings["pass"] != "" {
		t.Fatalf("expected password to be redacted from admin settings response, got %q", settings["pass"])
	}
}

func TestAdminSaveSettingKeepsExistingPasswordWhenBlank(t *testing.T) {
	app := newTestApp(t)
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := cookieRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}
	originalPass, err := app.store.GetSetting(context.Background(), "pass")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}

	form := url.Values{}
	form.Set("user", "rootadmin")
	form.Set("pass", "")
	form.Set("notifyUrl", "https://example.com/callback")
	form.Set("returnUrl", "https://merchant.example.com/return")
	form.Set("key", strings.Repeat("k", 32))
	form.Set("wxpay", "weixin://pay")
	form.Set("zfbpay", "alipay://pay")
	form.Set("close", "5")
	form.Set("payQf", "1")

	req := httptest.NewRequest(http.MethodPost, "/admin/saveSetting", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal saveSetting response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected blank password save to preserve current password, got %+v", payload)
	}

	storedPass, err := app.store.GetSetting(context.Background(), "pass")
	if err != nil {
		t.Fatalf("GetSetting returned error: %v", err)
	}
	if storedPass != originalPass {
		t.Fatal("expected blank password save to keep existing stored password")
	}
}

func TestQRCodeDecodeRequiresAdmin(t *testing.T) {
	app := newTestApp(t)
	form := url.Values{}
	form.Set("base64", "ZmFrZQ==")
	req := httptest.NewRequest(http.MethodPost, "/deQrcode", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal deQrcode response: %v", err)
	}
	if payload.Code != -1 || payload.Msg != "未登录" {
		t.Fatalf("expected unauthenticated QR decode to be rejected, got %+v", payload)
	}
}

func TestValidateConfigRejectsWeakDefaults(t *testing.T) {
	err := ValidateConfig(Config{
		Port:               "8080",
		SessionSecret:      "change-me",
		BootstrapAdminUser: "admin",
		BootstrapAdminPass: "admin",
	})
	if err == nil {
		t.Fatal("expected weak config to be rejected")
	}
}

func TestValidateOutboundCallbackURLRejectsLocalhostAlias(t *testing.T) {
	err := validateOutboundCallbackURL("http://localhost./callback", false)
	if err == nil {
		t.Fatal("expected localhost alias callback to be rejected")
	}
}

func TestSendNotifyGETDoesNotFollowRedirects(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true

	finalHits := 0
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalHits++
		_, _ = w.Write([]byte("success"))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	res := app.sendNotifyGET(redirect.URL, "payId=test")
	if res == "success" {
		t.Fatalf("expected redirecting callback not to be treated as success")
	}
	if finalHits != 0 {
		t.Fatalf("expected final callback target not to be reached, got %d hits", finalHits)
	}
}

func TestSendNotifyGETRejectsNon2xxSuccessBody(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("success"))
	}))
	defer server.Close()

	res := app.sendNotifyGET(server.URL, "payId=test")
	if res == "success" {
		t.Fatal("expected non-2xx callback response not to be treated as success")
	}
}

func TestPaymentPageDoesNotLoadRemoteScripts(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "payPage", "pay.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pay page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, "https://lib.baomitu.com/") || strings.Contains(page, "http://lib.baomitu.com/") {
		t.Fatal("expected payment page to avoid third-party remote scripts")
	}
}

func TestLoginPageDoesNotLoadRemoteScripts(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "index.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read login page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, "https://cdn.jsdelivr.net/") || strings.Contains(page, "https://lib.baomitu.com/") {
		t.Fatal("expected login page to avoid third-party remote scripts")
	}
}

func TestLoginPageEncodesCredentialsViaFormEncoding(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "index.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read login page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, `$.post("/login","user="+$("#user").val()+"&pass="+$("#pass").val(),function (data) {`) {
		t.Fatal("expected login page to use form encoding instead of manual query concatenation")
	}
	if !strings.Contains(page, "$.param({") {
		t.Fatal("expected login page to use $.param for credential form encoding")
	}
}

func TestAdminListPagesUsePostForDynamicRequests(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "src", "main", "webapp", "admin", "orderlist.html"),
		filepath.Join("..", "..", "src", "main", "webapp", "admin", "wxqrcodelist.html"),
		filepath.Join("..", "..", "src", "main", "webapp", "admin", "zfbqrcodelist.html"),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read admin list page %s: %v", path, err)
		}
		page := string(content)
		if !strings.Contains(page, ",method: 'post'") {
			t.Fatalf("expected admin list page %s to use POST for dynamic requests", path)
		}
	}
}

func TestAdminShellDoesNotLoadRemoteScripts(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "aaa.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin shell page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, "https://lib.baomitu.com/") || strings.Contains(page, "https://unpkg.com/") {
		t.Fatal("expected admin shell page to avoid third-party remote scripts")
	}
}

func TestOrderListEscapesCustomParam(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "admin", "orderlist.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read orderlist page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, `out += "<p>自定义参数："+data.param+"</p>";`) {
		t.Fatal("expected order detail popup to escape custom param before rendering HTML")
	}
	if !strings.Contains(page, "escapeHTML(data.param)") {
		t.Fatal("expected order detail popup to escape custom param before rendering HTML")
	}
}

func TestQRCodeListPagesEncodePayURL(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "src", "main", "webapp", "admin", "wxqrcodelist.html"),
		filepath.Join("..", "..", "src", "main", "webapp", "admin", "zfbqrcodelist.html"),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read qrcode list page %s: %v", path, err)
		}
		page := string(content)
		if strings.Contains(page, `return '<img src="/enQrcode?url='+d.payUrl+'"/>';`) {
			t.Fatalf("expected qrcode list page %s to encode payUrl before embedding it into HTML", path)
		}
		if !strings.Contains(page, "encodeURIComponent(d.payUrl") {
			t.Fatalf("expected qrcode list page %s to encode payUrl before embedding it into HTML", path)
		}
	}
}

func TestSettingsPageDoesNotPopulateCurrentPassword(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "admin", "setting.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings page: %v", err)
	}
	page := string(content)
	if strings.Contains(page, `$("#pass").val(data.data.pass);`) {
		t.Fatal("expected settings page not to populate the current admin password")
	}
}

func TestAdminShellUsesLogoutEndpoint(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "aaa.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin shell page: %v", err)
	}
	page := string(content)
	if !strings.Contains(page, "logoutAdmin()") || !strings.Contains(page, `$.post("/logout"`) {
		t.Fatal("expected admin shell to call logout endpoint")
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	store := NewMemoryStore()
	cfg := Config{
		Port:                  "8080",
		SessionSecret:         "test-secret-with-at-least-thirty-two-bytes",
		BootstrapAdminUser:    "rootadmin",
		BootstrapAdminPass:    "StrongPass!123456",
		WebRoot:               "../../src/main/webapp",
		HTTPClientTimeout:     2 * time.Second,
		AllowInsecureDefaults: true,
		AdminSessionTTL:       24 * time.Hour,
	}
	app, err := New(cfg, store)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	app.now = func() time.Time {
		return time.UnixMilli(1713024000000)
	}
	if err := app.store.BootstrapDefaults(context.Background(), app.now(), cfg); err != nil {
		t.Fatalf("BootstrapDefaults returned error: %v", err)
	}
	return app
}

func mustClientIPResolver(t *testing.T, cfg Config) *clientIPResolver {
	t.Helper()
	resolver, err := newClientIPResolver(cfg)
	if err != nil {
		t.Fatalf("newClientIPResolver returned error: %v", err)
	}
	return resolver
}
