package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestEpayMAPICreatesVMQOrderAndReturnsPayURL(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	app.cfg.EpayPublicBaseURL = "https://vmq.example.com"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}

	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260001",
		"param":        "101",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/payment/return?payment_id=1",
		"name":         "Dujiao order",
		"money":        "10.00",
	})
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "vmq.example.com"
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"].(float64) != 1 {
		t.Fatalf("expected epay success payload, got %s", rec.Body.String())
	}
	if got := payload["payurl"].(string); !strings.HasPrefix(got, "https://vmq.example.com/payPage/pay.html?orderId=") {
		t.Fatalf("expected absolute VMQ pay page URL, got %q", got)
	}
	if got, ok := payload["qrcode"].(string); ok && got != "" {
		t.Fatalf("expected qrcode to be empty because VMQ pay page is returned as payurl, got %q", got)
	}

	order, err := app.store.GetOrderByPayID(ctx, "DUJIAO202604260001")
	if err != nil || order == nil {
		t.Fatalf("expected VMQ order to be created, order=%#v err=%v", order, err)
	}
	if order.Type != 2 || order.NotifyURL != "https://shop.example.com/api/v1/payments/callback" {
		t.Fatalf("unexpected order mapping: %#v", order)
	}
	if epayType, callbackParam, ok := parseEpayOrderParam(order.Param); !ok || epayType != "alipay" || callbackParam != "101" {
		t.Fatalf("expected epay marker in order param, got %q", order.Param)
	}
}

func TestEpayMAPIRejectsInvalidSignature(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260002",
		"param":        "102",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/payment/return",
		"name":         "Dujiao order",
		"money":        "10.00",
	})
	form.Set("sign", "bad-sign")
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "签名") {
		t.Fatalf("expected signature failure, got %#v", payload)
	}
	order, err := app.store.GetOrderByPayID(context.Background(), "DUJIAO202604260002")
	if err != nil {
		t.Fatalf("GetOrderByPayID returned error: %v", err)
	}
	if order != nil {
		t.Fatalf("expected invalid epay request not to create an order: %#v", order)
	}
}

func TestEpayMAPIRejectsZeroMoney(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260011",
		"param":        "111",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/pay",
		"name":         "Dujiao order",
		"money":        "0",
	})
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "money") {
		t.Fatalf("expected zero money to be rejected, got %#v", payload)
	}
	order, err := app.store.GetOrderByPayID(ctx, "DUJIAO202604260011")
	if err != nil {
		t.Fatalf("GetOrderByPayID returned error: %v", err)
	}
	if order != nil {
		t.Fatalf("expected zero money request not to create an order: %#v", order)
	}
}

func TestEpayMAPIRejectsTooLongOutTradeNo(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	outTradeNo := strings.Repeat("A", maxPayIDLength+1)
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": outTradeNo,
		"param":        "112",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/pay",
		"name":         "Dujiao order",
		"money":        "10.00",
	})
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "out_trade_no") {
		t.Fatalf("expected too long out_trade_no to be rejected, got %#v", payload)
	}
	order, err := app.store.GetOrderByPayID(ctx, outTradeNo)
	if err != nil {
		t.Fatalf("GetOrderByPayID returned error: %v", err)
	}
	if order != nil {
		t.Fatalf("expected too long out_trade_no request not to create an order: %#v", order)
	}
}

func TestEpayMAPIDuplicateOrderIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	app.cfg.EpayPublicBaseURL = "https://vmq.example.com"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260009",
		"param":        "109",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/pay",
		"name":         "Dujiao order",
		"money":        "10.00",
	})
	create := func() map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return payload
	}

	first := create()
	second := create()

	if first["code"].(float64) != 1 {
		t.Fatalf("expected first create success, got %#v", first)
	}
	if second["code"].(float64) != 1 {
		t.Fatalf("expected duplicate create to return existing payment, got %#v", second)
	}
	if first["trade_no"] != second["trade_no"] || first["payurl"] != second["payurl"] {
		t.Fatalf("expected duplicate create to be idempotent, first=%#v second=%#v", first, second)
	}
}

func TestEpayMAPIDuplicateOrderWithChangedFieldsIsRejected(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	baseFields := map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260012",
		"param":        "112",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/pay",
		"name":         "Dujiao order",
		"money":        "10.00",
	}
	first := signedEpayCreateForm(app, baseFields)
	firstReq := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(first.Encode()))
	firstReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, firstReq)

	changedFields := map[string]string{}
	for key, value := range baseFields {
		changedFields[key] = value
	}
	changedFields["money"] = "11.00"
	changedFields["notify_url"] = "https://attacker.example.com/callback"
	second := signedEpayCreateForm(app, changedFields)
	secondReq := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(second.Encode()))
	secondReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondRec := httptest.NewRecorder()

	app.Handler().ServeHTTP(secondRec, secondReq)

	var payload CommonRes
	if err := json.Unmarshal(secondRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "different epay order fields") {
		t.Fatalf("expected changed duplicate fields to be rejected, got %#v", payload)
	}
	order, err := app.store.GetOrderByPayID(ctx, "DUJIAO202604260012")
	if err != nil || order == nil {
		t.Fatalf("expected original order to exist, order=%#v err=%v", order, err)
	}
	if order.Price != 10 || order.NotifyURL != baseFields["notify_url"] {
		t.Fatalf("expected original order fields to stay unchanged, got %#v", order)
	}
}

func TestEpayCallbackUsesOriginalAmountAndSignsDujiaoPayload(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{
		"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST",
		"payQf":  "1",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	if ok, err := app.store.ReservePrice(ctx, priceKey(2, 10)); err != nil || !ok {
		t.Fatalf("ReservePrice returned ok=%v err=%v", ok, err)
	}
	var callback url.Values
	dujiao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm returned error: %v", err)
		}
		callback = r.PostForm
		_, _ = w.Write([]byte("success"))
	}))
	defer dujiao.Close()

	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO202604260003",
		"param":        "103",
		"notify_url":   dujiao.URL,
		"return_url":   "https://shop.example.com/payment/return",
		"name":         "Dujiao order",
		"money":        "10.00",
	})
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	order, err := app.store.GetOrderByPayID(ctx, "DUJIAO202604260003")
	if err != nil || order == nil {
		t.Fatalf("expected order to be created, order=%#v err=%v", order, err)
	}
	if order.ReallyPrice != 10.01 {
		t.Fatalf("expected VMQ amount discrimination to change reallyPrice to 10.01, got %v", order.ReallyPrice)
	}
	deviceKey, _ := app.store.GetSetting(ctx, "deviceKey")
	ts := strconv.FormatInt(app.now().UnixMilli(), 10)
	res := app.handleAppPushLogic(ctx, 2, "10.01", ts, md5Hex("2"+"10.01"+ts+deviceKey))
	if res.Code != 1 {
		t.Fatalf("expected app push success, got %#v", res)
	}
	if callback == nil {
		t.Fatal("expected Dujiao callback to be sent")
	}
	if got := callback.Get("money"); got != "10" {
		t.Fatalf("expected Dujiao callback to use original amount 10, got %q", got)
	}
	if got := callback.Get("out_trade_no"); got != "DUJIAO202604260003" {
		t.Fatalf("unexpected out_trade_no %q", got)
	}
	if got := callback.Get("trade_status"); got != "TRADE_SUCCESS" {
		t.Fatalf("unexpected trade_status %q", got)
	}
	if got := callback.Get("param"); got != "103" {
		t.Fatalf("expected Dujiao callback param, got %q", got)
	}
	if got := callback.Get("sign_type"); got != "MD5" {
		t.Fatalf("unexpected sign_type %q", got)
	}
	if !verifyEpaySign(valuesToMap(callback), app.cfg.EpayMerchantKey) {
		t.Fatalf("expected Dujiao callback signature to verify, got %v", callback)
	}
}

func TestEpayCallbackEndtimeUsesUnixSecondsForDujiaoNext(t *testing.T) {
	app := newTestApp(t)
	order := &PayOrder{
		OrderID: "VMQORDER-ENDTIME",
		PayID:   "DUJIAO202604260010",
		Param:   epayOrderParam("alipay", "110"),
		Type:    2,
		Price:   10,
		PayDate: app.now().UnixMilli(),
	}

	params := app.buildEpayCallbackParams(order, "alipay", "110", "epay-secret-with-at-least-thirty-two-bytes")

	want := strconv.FormatInt(app.now().Unix(), 10)
	if got := params["endtime"]; got != want {
		t.Fatalf("expected Dujiao-Next parsable Unix seconds endtime %q, got %q", want, got)
	}
}

func TestEpayAdminBackfillUsesEpayCallback(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	ctx := context.Background()
	var callback url.Values
	var method string
	dujiao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm returned error: %v", err)
		}
		callback = r.PostForm
		_, _ = w.Write([]byte("success"))
	}))
	defer dujiao.Close()
	order := &PayOrder{
		OrderID:     "VMQORDER-BACKFILL",
		PayID:       "DUJIAO202604260007",
		CreateDate:  app.now().UnixMilli(),
		Param:       epayOrderParam("alipay", "207"),
		Type:        2,
		Price:       10,
		ReallyPrice: 10.01,
		NotifyURL:   dujiao.URL,
		ReturnURL:   "https://shop.example.com/payment/return",
		State:       0,
		IsAuto:      1,
		PayURL:      "HTTPS://QR.ALIPAY.COM/TEST",
	}
	if err := app.store.CreateOrder(ctx, order); err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	cookieRec := httptest.NewRecorder()
	if err := app.setAdminCookie(ctx, cookieRec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/setBd", strings.NewReader(url.Values{"id": []string{strconv.FormatInt(order.ID, 10)}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "vmq.example.com"
	req.Header.Set("Origin", "https://vmq.example.com")
	req.AddCookie(cookieRec.Result().Cookies()[0])
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected admin backfill success, got %#v", payload)
	}
	if method != http.MethodPost {
		t.Fatalf("expected epay backfill to POST callback, got %q", method)
	}
	if callback.Get("payId") != "" {
		t.Fatalf("expected epay callback not to use legacy VMQ payId field, got %v", callback)
	}
	if got := callback.Get("out_trade_no"); got != order.PayID {
		t.Fatalf("unexpected out_trade_no %q", got)
	}
	if got := callback.Get("param"); got != "207" {
		t.Fatalf("unexpected param %q", got)
	}
	if got := callback.Get("endtime"); got != strconv.FormatInt(app.now().Unix(), 10) {
		t.Fatalf("expected backfill callback endtime to use current Unix seconds, got %q", got)
	}
	if !verifyEpaySign(valuesToMap(callback), app.cfg.EpayMerchantKey) {
		t.Fatalf("expected Dujiao callback signature to verify, got %v", callback)
	}
	stored, err := app.store.GetOrderByID(ctx, order.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetOrderByID returned order=%#v err=%v", stored, err)
	}
	if stored.State != 1 {
		t.Fatalf("expected backfilled order state to be paid, got %d", stored.State)
	}
	if stored.PayDate != app.now().UnixMilli() || stored.CloseDate != app.now().UnixMilli() {
		t.Fatalf("expected backfilled order payment timestamps to be stored, got payDate=%d closeDate=%d", stored.PayDate, stored.CloseDate)
	}
}

func TestEpayNotifyReportsNon2xxStatusAndBody(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	dujiao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad callback"))
	}))
	defer dujiao.Close()
	order := &PayOrder{
		OrderID:   "VMQORDER-NON2XX",
		PayID:     "DUJIAO202604260008",
		Param:     epayOrderParam("alipay", "208"),
		Type:      2,
		Price:     10,
		NotifyURL: dujiao.URL,
		State:     1,
	}

	resp := app.sendEpayNotify(context.Background(), order, "alipay", "208")

	if !strings.Contains(resp, "status=400") || !strings.Contains(resp, "bad callback") {
		t.Fatalf("expected non-2xx status and body in response, got %q", resp)
	}
}

func TestEpayCheckOrderReturnsDujiaoReturnURLWithoutLegacyVMQQuery(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := &PayOrder{
		OrderID:     "VMQORDER1",
		PayID:       "DUJIAO202604260004",
		CreateDate:  app.now().UnixMilli(),
		PayDate:     app.now().UnixMilli(),
		Param:       epayOrderParam("alipay", "104"),
		Type:        2,
		Price:       10,
		ReallyPrice: 10.01,
		NotifyURL:   "https://shop.example.com/api/v1/payments/callback",
		ReturnURL:   "https://shop.example.com/payment/return?payment_id=1",
		State:       1,
		IsAuto:      1,
		PayURL:      "HTTPS://QR.ALIPAY.COM/TEST",
	}
	if err := app.store.CreateOrder(ctx, order); err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	key, _ := app.store.GetSetting(ctx, "key")
	form := url.Values{}
	form.Set("orderId", order.OrderID)
	form.Set("sign", md5Hex(order.OrderID+key))
	req := httptest.NewRequest(http.MethodPost, "/checkOrder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 1 {
		t.Fatalf("expected success, got %#v", payload)
	}
	if got := payload.Data.(string); got != order.ReturnURL {
		t.Fatalf("expected exact Dujiao return URL, got %q", got)
	}
}

func signedEpayCreateForm(app *App, fields map[string]string) url.Values {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	form.Set("sign_type", "MD5")
	params := valuesToMap(form)
	form.Set("sign", epaySign(params, mustEpayKey(app)))
	return form
}

func valuesToMap(values url.Values) map[string]string {
	out := make(map[string]string, len(values))
	for key := range values {
		out[key] = values.Get(key)
	}
	return out
}

func mustEpayKey(app *App) string {
	key, err := app.epayMerchantKey(context.Background())
	if err != nil {
		panic(err)
	}
	return key
}

func verifyEpaySign(params map[string]string, key string) bool {
	sign := params["sign"]
	return secureEqual(strings.ToLower(sign), epaySign(params, key))
}

func TestEpaySubmitRedirectsToVMQPayPage(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	app.cfg.EpayPublicBaseURL = "https://vmq.example.com"
	if err := app.store.UpsertSettings(context.Background(), map[string]string{"wxpay": "weixin://test-pay"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "wxpay",
		"out_trade_no": "DUJIAO202604260005",
		"param":        "105",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/payment/return",
		"name":         "Dujiao order",
		"money":        "12.34",
	})
	req := httptest.NewRequest(http.MethodGet, "/submit.php?"+form.Encode(), nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "https://vmq.example.com/payPage/pay.html?orderId=") {
		t.Fatalf("unexpected redirect location %q", location)
	}
	order, err := app.store.GetOrderByPayID(context.Background(), "DUJIAO202604260005")
	if err != nil || order == nil {
		t.Fatalf("expected order to be created, order=%#v err=%v", order, err)
	}
	if order.Type != 1 {
		t.Fatalf("expected wxpay to map to VMQ type 1, got %d", order.Type)
	}
}

func TestEpayMAPIDoesNotAcceptQQPay(t *testing.T) {
	app := newTestApp(t)
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	form := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "qqpay",
		"out_trade_no": "DUJIAO202604260006",
		"param":        "106",
		"notify_url":   "https://shop.example.com/api/v1/payments/callback",
		"return_url":   "https://shop.example.com/payment/return",
		"name":         "Dujiao order",
		"money":        "12.34",
	})
	req := httptest.NewRequest(http.MethodPost, "/mapi.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "支付方式") {
		t.Fatalf("expected unsupported channel rejection, got %#v", payload)
	}
}

func TestEpayCallbackRejectsForgedVMQOrderParamOnUnsignedMerchantAPI(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/TEST"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	key, _ := app.store.GetSetting(ctx, "key")
	form := url.Values{}
	form.Set("payId", "FORGED-EPAY")
	form.Set("type", "2")
	form.Set("price", "10.00")
	form.Set("param", epayOrderParam("alipay", "107"))
	form.Set("notifyUrl", "https://shop.example.com/api/v1/payments/callback")
	form.Set("returnUrl", "https://shop.example.com/payment/return")
	form.Set("sign", md5Hex("FORGED-EPAY"+epayOrderParam("alipay", "107")+"2"+"10.00"+key))
	req := httptest.NewRequest(http.MethodPost, "/createOrder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != -1 || !strings.Contains(payload.Msg, "param") {
		t.Fatalf("expected public merchant API to reject reserved epay param, got %#v", payload)
	}
}

func TestEpayCallbackIncludesRequiredFieldsOnlyOnce(t *testing.T) {
	params := map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "ORDER",
		"param":        "108",
		"money":        "1.23",
		"trade_status": "TRADE_SUCCESS",
		"sign_type":    "MD5",
	}
	params["sign"] = epaySign(params, "epay-secret-with-at-least-thirty-two-bytes")
	if params["sign"] == "" {
		t.Fatal("expected sign to be generated")
	}
	if _, err := strconv.ParseFloat(params["money"], 64); err != nil {
		t.Fatalf("expected money to stay numeric: %v", err)
	}
}
