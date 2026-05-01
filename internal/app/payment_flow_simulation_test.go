package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestSimulatedMerchantPaymentMonitorCallbackAndBackfill(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	ctx := context.Background()

	var callbackMu sync.Mutex
	var callbackQueries []url.Values
	merchantCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbackMu.Lock()
		callbackQueries = append(callbackQueries, r.URL.Query())
		callbackMu.Unlock()
		_, _ = w.Write([]byte("success"))
	}))
	defer merchantCallback.Close()

	if err := app.store.UpsertSettings(ctx, map[string]string{
		"wxpay":     "weixin://default-qr",
		"notifyUrl": merchantCallback.URL,
		"returnUrl": "https://merchant.example.com/return",
	}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	merchantKey := mustStoreSetting(t, app, "key")
	deviceKey := mustStoreSetting(t, app, "deviceKey")

	heartTimestamp := strconv.FormatInt(app.now().UnixMilli(), 10)
	heartRes := postPublicForm(t, app, "/appHeart", url.Values{
		"t":    {heartTimestamp},
		"sign": {md5Hex(heartTimestamp + deviceKey)},
	})
	assertCommonSuccess(t, heartRes)
	if got := mustStoreSetting(t, app, "jkstate"); got != "1" {
		t.Fatalf("expected monitor heartbeat to set jkstate=1, got %q", got)
	}

	orderData := createMerchantOrder(t, app, "SIM-MERCHANT-PAID", "cart-1", "1", "12.34", merchantCallback.URL, "https://merchant.example.com/return?order=paid", merchantKey)
	pushTimestamp := strconv.FormatInt(app.now().UnixMilli()+1, 10)
	pushRes := postPublicForm(t, app, "/appPush", url.Values{
		"type":  {"1"},
		"price": {formatRawFloat(orderData.ReallyPrice)},
		"t":     {pushTimestamp},
		"sign":  {md5Hex("1" + formatRawFloat(orderData.ReallyPrice) + pushTimestamp + deviceKey)},
	})
	assertCommonSuccess(t, pushRes)

	callbackMu.Lock()
	if len(callbackQueries) != 1 {
		t.Fatalf("expected one merchant callback after monitor push, got %d", len(callbackQueries))
	}
	paidCallback := callbackQueries[0]
	callbackMu.Unlock()
	if paidCallback.Get("payId") != "SIM-MERCHANT-PAID" || paidCallback.Get("param") != "cart-1" {
		t.Fatalf("unexpected merchant callback query: %v", paidCallback)
	}
	if !secureEqual(paidCallback.Get("sign"), md5Hex("SIM-MERCHANT-PAID"+"cart-1"+"1"+"12.34"+"12.34"+merchantKey)) {
		t.Fatalf("merchant callback signature did not verify: %v", paidCallback)
	}
	paidOrder := mustOrderByPayID(t, app, "SIM-MERCHANT-PAID")
	if paidOrder.State != 1 || paidOrder.PayDate != app.now().UnixMilli()+1 {
		t.Fatalf("expected monitor push to mark order paid, got %+v", paidOrder)
	}

	checkRes := postPublicForm(t, app, "/checkOrder", url.Values{
		"orderId": {orderData.OrderID},
		"token":   {orderData.AccessToken},
	})
	checkPayload := decodeCommonResponse(t, checkRes)
	if checkPayload.Code != 1 || !strings.HasPrefix(checkPayload.Data.(string), "https://merchant.example.com/return?order=paid&") {
		t.Fatalf("expected checkOrder to return merchant return URL with callback query, got %+v", checkPayload)
	}

	backfillOrder := createMerchantOrder(t, app, "SIM-MERCHANT-BACKFILL", "cart-2", "1", "13.33", merchantCallback.URL, "https://merchant.example.com/return?order=backfill", merchantKey)
	adminCookie := newAdminCookie(t, app)
	adminRes := postAdminForm(t, app, "/admin/setBd", url.Values{"id": {strconv.FormatInt(mustOrderByPayID(t, app, backfillOrder.PayID).ID, 10)}}, adminCookie)
	assertCommonSuccess(t, adminRes)
	backfilled := mustOrderByPayID(t, app, "SIM-MERCHANT-BACKFILL")
	if backfilled.State != 1 || backfilled.PayDate <= 0 || backfilled.CloseDate <= 0 {
		t.Fatalf("expected admin backfill to mark order paid, got %+v", backfilled)
	}
	callbackMu.Lock()
	defer callbackMu.Unlock()
	if len(callbackQueries) != 2 {
		t.Fatalf("expected monitor push plus admin backfill callbacks, got %d", len(callbackQueries))
	}
	if callbackQueries[1].Get("payId") != "SIM-MERCHANT-BACKFILL" {
		t.Fatalf("unexpected backfill callback query: %v", callbackQueries[1])
	}
}

func TestSimulatedAdminQRCodeAddListDeleteAndOrderSelection(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	merchantKey := mustStoreSetting(t, app, "key")
	if err := app.store.UpsertSettings(ctx, map[string]string{"wxpay": "weixin://fallback-qr"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	adminCookie := newAdminCookie(t, app)

	addRes := postAdminForm(t, app, "/admin/addPayQrcode", url.Values{
		"type":   {"1"},
		"price":  {"88.88"},
		"payUrl": {"weixin://fixed-qr-8888"},
	}, adminCookie)
	assertCommonSuccess(t, addRes)

	listRec := postAdminForm(t, app, "/admin/getPayQrcodes", url.Values{
		"page":  {"1"},
		"limit": {"10"},
		"type":  {"1"},
	}, adminCookie)
	var listPayload struct {
		Code  int         `json:"code"`
		Msg   string      `json:"msg"`
		Count int64       `json:"count"`
		Data  []PayQRCode `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode qrcode list response: %v", err)
	}
	if listPayload.Code != 0 || listPayload.Count != 1 || len(listPayload.Data) != 1 {
		t.Fatalf("expected one listed QR code, got %+v body=%s", listPayload, listRec.Body.String())
	}
	code := listPayload.Data[0]
	if code.PayURL != "weixin://fixed-qr-8888" || code.Price != 88.88 || code.Type != 1 {
		t.Fatalf("unexpected listed QR code: %+v", code)
	}

	orderData := createMerchantOrder(t, app, "SIM-QR-ORDER", "qr-param", "1", "88.88", "", "", merchantKey)
	if orderData.PayURL != "weixin://fixed-qr-8888" || orderData.IsAuto != 0 {
		t.Fatalf("expected fixed QR code to be selected for exact amount, got %+v", orderData)
	}

	delRes := postAdminForm(t, app, "/admin/delPayQrcode", url.Values{"id": {strconv.FormatInt(code.ID, 10)}}, adminCookie)
	assertCommonSuccess(t, delRes)
	afterDelete, count, err := app.store.ListQRCodes(ctx, 1, 10, intPtr(1))
	if err != nil {
		t.Fatalf("ListQRCodes returned error: %v", err)
	}
	if count != 0 || len(afterDelete) != 0 {
		t.Fatalf("expected qrcode to be deleted, count=%d items=%+v", count, afterDelete)
	}
}

func TestSimulatedDujiaoEpayPaymentCallbackAndBackfill(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AllowPrivateCallbacks = true
	app.cfg.EpayMerchantID = "1000"
	app.cfg.EpayMerchantKey = "epay-secret-with-at-least-thirty-two-bytes"
	app.cfg.EpayPublicBaseURL = "https://vmq.example.com"
	ctx := context.Background()
	if err := app.store.UpsertSettings(ctx, map[string]string{"zfbpay": "HTTPS://QR.ALIPAY.COM/SIM"}); err != nil {
		t.Fatalf("UpsertSettings returned error: %v", err)
	}
	deviceKey := mustStoreSetting(t, app, "deviceKey")

	var callbackMu sync.Mutex
	var epayCallbacks []url.Values
	dujiaoCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm returned error: %v", err)
		}
		callbackMu.Lock()
		epayCallbacks = append(epayCallbacks, r.PostForm)
		callbackMu.Unlock()
		_, _ = w.Write([]byte(epayCallbackSuccess))
	}))
	defer dujiaoCallback.Close()

	createForm := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO-SIM-PAID",
		"param":        "dujiao-param-paid",
		"notify_url":   dujiaoCallback.URL,
		"return_url":   "https://shop.example.com/payment/return?payment_id=paid",
		"name":         "Dujiao simulated order",
		"money":        "20.00",
	})
	createRec := postPublicForm(t, app, "/mapi.php", createForm)
	createPayload := decodeCommonResponse(t, createRec)
	if createPayload.Code != 1 {
		t.Fatalf("expected epay create success, got %+v body=%s", createPayload, createRec.Body.String())
	}
	epayOrder := mustOrderByPayID(t, app, "DUJIAO-SIM-PAID")
	pushTimestamp := strconv.FormatInt(app.now().UnixMilli()+2, 10)
	pushRes := postPublicForm(t, app, "/appPush", url.Values{
		"type":  {"2"},
		"price": {formatRawFloat(epayOrder.ReallyPrice)},
		"t":     {pushTimestamp},
		"sign":  {md5Hex("2" + formatRawFloat(epayOrder.ReallyPrice) + pushTimestamp + deviceKey)},
	})
	assertCommonSuccess(t, pushRes)

	callbackMu.Lock()
	if len(epayCallbacks) != 1 {
		t.Fatalf("expected one Dujiao epay callback, got %d", len(epayCallbacks))
	}
	paidCallback := epayCallbacks[0]
	callbackMu.Unlock()
	if paidCallback.Get("out_trade_no") != "DUJIAO-SIM-PAID" || paidCallback.Get("param") != "dujiao-param-paid" {
		t.Fatalf("unexpected Dujiao callback form: %v", paidCallback)
	}
	if paidCallback.Get("money") != "20" || paidCallback.Get("trade_status") != epayStatusSuccess {
		t.Fatalf("unexpected Dujiao callback payment fields: %v", paidCallback)
	}
	if !verifyEpaySign(valuesToMap(paidCallback), app.cfg.EpayMerchantKey) {
		t.Fatalf("Dujiao callback signature did not verify: %v", paidCallback)
	}

	checkRec := postPublicForm(t, app, "/checkOrder", url.Values{
		"orderId": {epayOrder.OrderID},
		"token":   {orderAccessToken(epayOrder.OrderID, app.cfg.SessionSecret)},
	})
	checkPayload := decodeCommonResponse(t, checkRec)
	if checkPayload.Code != 1 || checkPayload.Data.(string) != "https://shop.example.com/payment/return?payment_id=paid" {
		t.Fatalf("expected epay checkOrder to return raw Dujiao return URL, got %+v", checkPayload)
	}

	backfillForm := signedEpayCreateForm(app, map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "DUJIAO-SIM-BACKFILL",
		"param":        "dujiao-param-backfill",
		"notify_url":   dujiaoCallback.URL,
		"return_url":   "https://shop.example.com/payment/return?payment_id=backfill",
		"name":         "Dujiao simulated backfill",
		"money":        "21.00",
	})
	backfillCreate := postPublicForm(t, app, "/mapi.php", backfillForm)
	if payload := decodeCommonResponse(t, backfillCreate); payload.Code != 1 {
		t.Fatalf("expected epay backfill order create success, got %+v", payload)
	}
	backfillOrder := mustOrderByPayID(t, app, "DUJIAO-SIM-BACKFILL")
	adminCookie := newAdminCookie(t, app)
	backfillRec := postAdminForm(t, app, "/admin/setBd", url.Values{"id": {strconv.FormatInt(backfillOrder.ID, 10)}}, adminCookie)
	assertCommonSuccess(t, backfillRec)
	storedBackfill := mustOrderByPayID(t, app, "DUJIAO-SIM-BACKFILL")
	if storedBackfill.State != 1 || storedBackfill.PayDate <= 0 || storedBackfill.CloseDate <= 0 {
		t.Fatalf("expected epay admin backfill to mark order paid, got %+v", storedBackfill)
	}
	callbackMu.Lock()
	defer callbackMu.Unlock()
	if len(epayCallbacks) != 2 {
		t.Fatalf("expected monitor push plus epay backfill callbacks, got %d", len(epayCallbacks))
	}
	if epayCallbacks[1].Get("out_trade_no") != "DUJIAO-SIM-BACKFILL" || !verifyEpaySign(valuesToMap(epayCallbacks[1]), app.cfg.EpayMerchantKey) {
		t.Fatalf("unexpected epay backfill callback: %v", epayCallbacks[1])
	}
}

func createMerchantOrder(t *testing.T, app *App, payID, param, payType, price, notifyURL, returnURL, key string) CreateOrderRes {
	t.Helper()
	form := url.Values{}
	form.Set("payId", payID)
	form.Set("param", param)
	form.Set("type", payType)
	form.Set("price", price)
	if notifyURL != "" {
		form.Set("notifyUrl", notifyURL)
	}
	if returnURL != "" {
		form.Set("returnUrl", returnURL)
	}
	form.Set("sign", md5Hex(payID+param+payType+price+key))
	rec := postPublicForm(t, app, "/createOrder", form)
	payload := decodeCommonResponse(t, rec)
	if payload.Code != 1 {
		t.Fatalf("expected createOrder success, got %+v body=%s", payload, rec.Body.String())
	}
	return decodeCommonData[CreateOrderRes](t, payload)
}

func postPublicForm(t *testing.T, app *App, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func postAdminForm(t *testing.T, app *App, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "vmq.example.com"
	req.Header.Set("Origin", "https://vmq.example.com")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func newAdminCookie(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := app.setAdminCookie(context.Background(), rec); err != nil {
		t.Fatalf("setAdminCookie returned error: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin cookie to be set")
	}
	return cookies[0]
}

func decodeCommonResponse(t *testing.T, rec *httptest.ResponseRecorder) CommonRes {
	t.Helper()
	var payload CommonRes
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode common response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func decodeCommonData[T any](t *testing.T, payload CommonRes) T {
	t.Helper()
	dataBytes, err := json.Marshal(payload.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	var out T
	if err := json.Unmarshal(dataBytes, &out); err != nil {
		t.Fatalf("decode response data: %v data=%s", err, string(dataBytes))
	}
	return out
}

func assertCommonSuccess(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	payload := decodeCommonResponse(t, rec)
	if payload.Code != 1 {
		t.Fatalf("expected success response, got %+v body=%s", payload, rec.Body.String())
	}
}

func mustStoreSetting(t *testing.T, app *App, key string) string {
	t.Helper()
	value, err := app.store.GetSetting(context.Background(), key)
	if err != nil {
		t.Fatalf("GetSetting(%s) returned error: %v", key, err)
	}
	return value
}

func mustOrderByPayID(t *testing.T, app *App, payID string) *PayOrder {
	t.Helper()
	order, err := app.store.GetOrderByPayID(context.Background(), payID)
	if err != nil {
		t.Fatalf("GetOrderByPayID returned error: %v", err)
	}
	if order == nil {
		t.Fatalf("expected order %s to exist", payID)
	}
	return order
}

func intPtr(value int) *int {
	return &value
}
