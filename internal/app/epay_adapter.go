package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEpayMerchantID = "1000"
	epayOrderParamPrefix  = "__epay_v1:"
	epaySignTypeMD5       = "MD5"
	epayStatusSuccess     = "TRADE_SUCCESS"
	epayCallbackSuccess   = "success"
)

type epayCreateResult struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	TradeNo   string `json:"trade_no,omitempty"`
	PayURL    string `json:"payurl,omitempty"`
	QRCode    string `json:"qrcode,omitempty"`
	URLScheme string `json:"urlscheme,omitempty"`
}

type epayCreateInput struct {
	PID        string
	PayType    int
	EpayType   string
	OutTradeNo string
	Param      string
	NotifyURL  string
	ReturnURL  string
	Name       string
	Money      string
}

func (a *App) handleEpayMAPI(w http.ResponseWriter, r *http.Request) {
	applySensitiveNoStoreHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(epayCreateResult{Code: -1, Msg: "method not allowed"})
		return
	}

	result, err := a.createEpayOrder(r.Context(), r)
	if err != nil {
		_ = json.NewEncoder(w).Encode(epayCreateResult{Code: -1, Msg: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func (a *App) handleEpaySubmit(w http.ResponseWriter, r *http.Request) {
	applySensitiveNoStoreHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := a.createEpayOrder(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, result.PayURL, http.StatusFound)
}

func (a *App) createEpayOrder(ctx context.Context, r *http.Request) (*epayCreateResult, error) {
	input, err := a.parseEpayCreateInput(ctx, r)
	if err != nil {
		return nil, err
	}
	merchantKey, err := a.merchantKey(ctx)
	if err != nil {
		return nil, errors.New("merchant key unavailable")
	}
	param := epayOrderParam(input.EpayType, input.Param)
	sign := md5Hex(input.OutTradeNo + param + strconv.Itoa(input.PayType) + input.Money + merchantKey)
	res := a.createOrder(ctx, input.OutTradeNo, param, input.PayType, input.Money, mustParseMoney(input.Money), input.NotifyURL, input.ReturnURL, sign)
	if res.Code != 1 {
		if res.Msg == "" {
			return nil, errors.New("create order failed")
		}
		return nil, errors.New(res.Msg)
	}
	orderRes, ok := res.Data.(CreateOrderRes)
	if !ok {
		return nil, errors.New("create order response invalid")
	}
	payURL := a.epayPayPageURL(r, orderRes.OrderID, orderRes.AccessToken)
	return &epayCreateResult{
		Code:    1,
		Msg:     "success",
		TradeNo: orderRes.OrderID,
		PayURL:  payURL,
		QRCode:  "",
	}, nil
}

func (a *App) parseEpayCreateInput(ctx context.Context, r *http.Request) (*epayCreateInput, error) {
	if err := r.ParseForm(); err != nil {
		return nil, errors.New("form parse failed")
	}
	params := epayFormMap(r.Form)
	key, err := a.epayMerchantKey(ctx)
	if err != nil {
		return nil, errors.New("epay merchant key unavailable")
	}
	signType := strings.ToUpper(strings.TrimSpace(params["sign_type"]))
	if signType != "" && signType != epaySignTypeMD5 {
		return nil, errors.New("only MD5 sign_type is supported")
	}
	if strings.TrimSpace(params["sign"]) == "" || !secureEqual(strings.ToLower(params["sign"]), epaySign(params, key)) {
		return nil, errors.New("签名校验不通过")
	}
	pid := strings.TrimSpace(params["pid"])
	if pid == "" || pid != a.epayMerchantID() {
		return nil, errors.New("pid校验不通过")
	}
	epayType, payType, err := mapEpayPayType(params["type"])
	if err != nil {
		return nil, err
	}
	outTradeNo := strings.TrimSpace(params["out_trade_no"])
	if outTradeNo == "" {
		return nil, errors.New("out_trade_no is required")
	}
	callbackParam := strings.TrimSpace(params["param"])
	if len(callbackParam) > maxPayIDLength {
		return nil, fmt.Errorf("param length must not exceed %d", maxPayIDLength)
	}
	notifyURL := strings.TrimSpace(params["notify_url"])
	if notifyURL == "" {
		return nil, errors.New("notify_url is required")
	}
	returnURL := strings.TrimSpace(params["return_url"])
	if returnURL == "" {
		return nil, errors.New("return_url is required")
	}
	money := strings.TrimSpace(params["money"])
	if !moneyPattern.MatchString(money) {
		return nil, errors.New("money format invalid")
	}
	return &epayCreateInput{
		PID:        pid,
		PayType:    payType,
		EpayType:   epayType,
		OutTradeNo: outTradeNo,
		Param:      callbackParam,
		NotifyURL:  notifyURL,
		ReturnURL:  returnURL,
		Name:       strings.TrimSpace(params["name"]),
		Money:      money,
	}, nil
}

func (a *App) sendEpayNotify(ctx context.Context, order *PayOrder, epayType, callbackParam string) string {
	if order.NotifyURL == "" {
		return "notify_url is empty"
	}
	if err := validateOutboundCallbackURL(order.NotifyURL, a.cfg.AllowPrivateCallbacks); err != nil {
		return "notify_url invalid"
	}
	key, err := a.epayMerchantKey(ctx)
	if err != nil {
		return "epay merchant key unavailable"
	}
	params := a.buildEpayCallbackParams(order, epayType, callbackParam, key)
	values := url.Values{}
	for k, v := range params {
		if strings.TrimSpace(v) != "" {
			values.Set(k, v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, order.NotifyURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "request build failed"
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	resp, err := a.client.Do(req)
	if err != nil {
		return "server unavailable"
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "non-2xx callback response"
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "response read failed"
	}
	return strings.TrimSpace(string(body))
}

func (a *App) buildEpayCallbackParams(order *PayOrder, epayType, callbackParam, key string) map[string]string {
	params := map[string]string{
		"pid":          a.epayMerchantID(),
		"type":         epayType,
		"out_trade_no": order.PayID,
		"trade_no":     order.OrderID,
		"trade_status": epayStatusSuccess,
		"money":        formatRawFloat(order.Price),
		"name":         "VMQ order " + order.PayID,
		"endtime":      formatEpayTime(order.PayDate),
		"sign_type":    epaySignTypeMD5,
	}
	if callbackParam != "" {
		params["param"] = callbackParam
	}
	params["sign"] = epaySign(params, key)
	return params
}

func (a *App) epayMerchantID() string {
	if strings.TrimSpace(a.cfg.EpayMerchantID) != "" {
		return strings.TrimSpace(a.cfg.EpayMerchantID)
	}
	return defaultEpayMerchantID
}

func (a *App) epayMerchantKey(ctx context.Context) (string, error) {
	if strings.TrimSpace(a.cfg.EpayMerchantKey) != "" {
		return strings.TrimSpace(a.cfg.EpayMerchantKey), nil
	}
	return a.merchantKey(ctx)
}

func (a *App) epayPayPageURL(r *http.Request, orderID, token string) string {
	base := strings.TrimRight(a.epayPublicBaseURL(r), "/")
	return base + "/payPage/pay.html?orderId=" + url.QueryEscape(orderID) + "&token=" + url.QueryEscape(token)
}

func (a *App) epayPublicBaseURL(r *http.Request) string {
	if strings.TrimSpace(a.cfg.EpayPublicBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(a.cfg.EpayPublicBaseURL), "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := firstForwardedHeaderValue(r.Header.Get("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}
	return scheme + "://" + r.Host
}

func firstForwardedHeaderValue(raw string) string {
	value := strings.TrimSpace(strings.Split(raw, ",")[0])
	return strings.ToLower(value)
}

func mapEpayPayType(raw string) (string, int, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "alipay":
		return "alipay", 2, nil
	case "wxpay", "wechat":
		return "wxpay", 1, nil
	default:
		return "", 0, errors.New("支付方式错误=>alipay|wxpay")
	}
}

func epayOrderParam(epayType, callbackParam string) string {
	value := epayOrderParamPrefix + strings.ToLower(strings.TrimSpace(epayType))
	if callbackParam == "" {
		return value
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(callbackParam))
	return value + "|" + encoded
}

func parseEpayOrderParam(param string) (string, string, bool) {
	if !isEpayOrderParam(param) {
		return "", "", false
	}
	body := strings.TrimPrefix(param, epayOrderParamPrefix)
	epayType, encodedParam, hasParam := strings.Cut(body, "|")
	if epayType == "" {
		return "", "", false
	}
	if !hasParam || encodedParam == "" {
		return epayType, "", true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encodedParam)
	if err != nil {
		return "", "", false
	}
	return epayType, string(decoded), true
}

func isEpayOrderParam(param string) bool {
	return strings.HasPrefix(param, epayOrderParamPrefix)
}

func epayFormMap(values url.Values) map[string]string {
	out := make(map[string]string, len(values))
	for key := range values {
		out[key] = strings.TrimSpace(values.Get(key))
	}
	return out
}

func epaySign(params map[string]string, key string) string {
	return md5Hex(epaySignContent(params) + key)
}

func epaySignContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if strings.TrimSpace(value) == "" {
			continue
		}
		lowerKey := strings.ToLower(key)
		if lowerKey == "sign" || lowerKey == "sign_type" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}

func formatEpayTime(millis int64) string {
	if millis <= 0 {
		return ""
	}
	return time.UnixMilli(millis).Format("2006-01-02 15:04:05")
}

func mustParseMoney(raw string) float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return round2(value)
}
