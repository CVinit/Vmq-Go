package app

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type App struct {
	cfg           Config
	store         Store
	client        *http.Client
	now           func() time.Time
	loginThrottle *loginThrottle
	clientIPs     *clientIPResolver
}

func New(cfg Config, store Store) (*App, error) {
	clientIPs, err := newClientIPResolver(cfg)
	if err != nil {
		return nil, err
	}
	app := &App{
		cfg:   cfg,
		store: store,
		client: &http.Client{
			Timeout: cfg.HTTPClientTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:           time.Now,
		loginThrottle: newLoginThrottle(),
		clientIPs:     clientIPs,
	}

	if err := store.BootstrapDefaults(context.Background(), app.now(), cfg); err != nil {
		return nil, err
	}
	settings, err := store.GetSettings(context.Background())
	if err != nil {
		return nil, err
	}
	if err := validateStoredSecurity(settings, cfg.AllowInsecureDefaults); err != nil {
		return nil, err
	}

	return app, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", requireMethod(http.MethodPost, a.handleLogin))
	mux.HandleFunc("/logout", adminPostOnly(a.handleLogout))
	mux.HandleFunc("/admin/getMenu", adminPostOnly(a.handleAdminGetMenu))
	mux.HandleFunc("/admin/saveSetting", adminPostOnly(a.handleAdminSaveSetting))
	mux.HandleFunc("/admin/getSettings", adminPostOnly(a.handleAdminGetSettings))
	mux.HandleFunc("/admin/getOrders", adminPostOnly(a.handleAdminGetOrders))
	mux.HandleFunc("/admin/setBd", adminPostOnly(a.handleAdminSetBd))
	mux.HandleFunc("/admin/getPayQrcodes", adminPostOnly(a.handleAdminGetPayQRCodes))
	mux.HandleFunc("/admin/delPayQrcode", adminPostOnly(a.handleAdminDelPayQrcode))
	mux.HandleFunc("/admin/addPayQrcode", adminPostOnly(a.handleAdminAddPayQrcode))
	mux.HandleFunc("/admin/getMain", adminPostOnly(a.handleAdminGetMain))
	mux.HandleFunc("/admin/delOrder", adminPostOnly(a.handleAdminDelOrder))
	mux.HandleFunc("/admin/delGqOrder", adminPostOnly(a.handleAdminDelGqOrder))
	mux.HandleFunc("/admin/delLastOrder", adminPostOnly(a.handleAdminDelLastOrder))

	mux.HandleFunc("/createOrder", a.handleCreateOrder)
	mux.HandleFunc("/closeOrder", a.handleCloseOrder)
	mux.HandleFunc("/appHeart", a.handleAppHeart)
	mux.HandleFunc("/appPush", a.handleAppPush)
	mux.HandleFunc("/getOrder", a.handleGetOrder)
	mux.HandleFunc("/checkOrder", a.handleCheckOrder)
	mux.HandleFunc("/getState", a.handleGetState)
	mux.HandleFunc("/enQrcode", a.handleEncodeQRCode)
	mux.HandleFunc("/deQrcode", adminPostOnly(a.handleDecodeQRCode))
	mux.HandleFunc("/deQrcode2", adminPostOnly(a.handleDecodeQRCodeFile))
	mux.HandleFunc("/mapi.php", a.handleEpayMAPI)
	mux.HandleFunc("/submit.php", a.handleEpaySubmit)

	fileServer := http.FileServer(http.Dir(a.cfg.WebRoot))
	mux.HandleFunc("/index.html", func(w http.ResponseWriter, r *http.Request) {
		applyAdminPageHeaders(w)
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			applyAdminPageHeaders(w)
			http.ServeFile(w, r, filepath.Join(a.cfg.WebRoot, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/") || r.URL.Path == "/aaa.html" {
			applyAdminPageHeaders(w)
		} else if strings.HasPrefix(r.URL.Path, "/payPage/") {
			applySensitiveNoStoreHeaders(w)
		} else {
			applyCommonSecurityHeaders(w)
		}
		fileServer.ServeHTTP(w, r)
	}))

	return mux
}

func (a *App) StartBackground(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			a.runMaintenance(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) runMaintenance(ctx context.Context) {
	closeSetting, err := a.store.GetSetting(ctx, "close")
	if err == nil {
		timeoutMinutes, convErr := strconv.Atoi(closeSetting)
		if convErr == nil && timeoutMinutes > 0 {
			now := a.now().UnixMilli()
			_, _ = a.store.ExpireOrders(ctx, now-int64(timeoutMinutes)*60*1000, now)
		}
	}

	settings, err := a.store.GetSettings(ctx)
	if err != nil {
		return
	}
	if settings["jkstate"] != "1" {
		return
	}
	lastHeart, err := strconv.ParseInt(settings["lastheart"], 10, 64)
	if err != nil {
		return
	}
	if a.now().UnixMilli()-lastHeart > 60*1000 {
		_ = a.store.UpsertSettings(ctx, map[string]string{"jkstate": "0"})
	}
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	applySensitiveNoStoreHeaders(w)
	_ = r.ParseForm()
	user := r.FormValue("user")
	pass := r.FormValue("pass")
	if user == "" {
		a.writeJSON(w, errorRes("请输入账号"))
		return
	}
	if pass == "" {
		a.writeJSON(w, errorRes("请输入密码"))
		return
	}
	loginKey := a.clientIP(r)
	if a.loginThrottle.isBlocked(loginKey, a.now()) {
		a.writeJSON(w, errorRes("账号或密码不正确"))
		return
	}

	ctx := r.Context()
	expectUser, err := a.store.GetSetting(ctx, "user")
	if err != nil {
		a.loginThrottle.recordFailure(loginKey, a.now())
		a.writeJSON(w, errorRes("账号或密码不正确"))
		return
	}
	expectPass, err := a.store.GetSetting(ctx, "pass")
	if err != nil {
		a.loginThrottle.recordFailure(loginKey, a.now())
		a.writeJSON(w, errorRes("账号或密码不正确"))
		return
	}
	if user != expectUser || !verifyAdminPassword(expectPass, pass) {
		a.loginThrottle.recordFailure(loginKey, a.now())
		a.writeJSON(w, errorRes("账号或密码不正确"))
		return
	}
	if !isPasswordHash(expectPass) {
		hashedPass, err := hashAdminPassword(pass)
		if err != nil {
			a.writeJSON(w, errorOnly())
			return
		}
		if err := a.store.UpsertSettings(ctx, map[string]string{"pass": hashedPass}); err != nil {
			a.writeJSON(w, errorOnly())
			return
		}
	}
	a.loginThrottle.reset(loginKey)

	if err := a.setAdminCookie(r.Context(), w); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	applySensitiveNoStoreHeaders(w)
	a.clearAdminCookie(w)
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminGetMenu(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		applySensitiveNoStoreHeaders(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, "null")
		return
	}

	ts := strconv.FormatInt(a.now().UnixMilli(), 10)
	menu := []map[string]any{
		{"name": "系统设置", "type": "url", "url": "admin/setting.html?t=" + ts},
		{"name": "监控端设置", "type": "url", "url": "admin/jk.html?t=" + ts},
		{
			"name": "微信二维码",
			"type": "menu",
			"node": []map[string]any{
				{"name": "添加", "type": "url", "url": "admin/addwxqrcode.html?t=" + ts},
				{"name": "管理", "type": "url", "url": "admin/wxqrcodelist.html?t=" + ts},
			},
		},
		{
			"name": "支付宝二维码",
			"type": "menu",
			"node": []map[string]any{
				{"name": "添加", "type": "url", "url": "admin/addzfbqrcode.html?t=" + ts},
				{"name": "管理", "type": "url", "url": "admin/zfbqrcodelist.html?t=" + ts},
			},
		},
		{"name": "订单列表", "type": "url", "url": "admin/orderlist.html?t=" + ts},
		{"name": "Api说明", "type": "url", "url": "../api.html?t=" + ts},
	}

	a.writeJSON(w, menu)
}

func (a *App) handleAdminSaveSetting(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	values := map[string]string{
		"user":      r.FormValue("user"),
		"pass":      r.FormValue("pass"),
		"notifyUrl": r.FormValue("notifyUrl"),
		"returnUrl": r.FormValue("returnUrl"),
		"key":       r.FormValue("key"),
		"wxpay":     r.FormValue("wxpay"),
		"zfbpay":    r.FormValue("zfbpay"),
		"close":     r.FormValue("close"),
		"payQf":     r.FormValue("payQf"),
	}
	current, err := a.store.GetSettings(r.Context())
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	if err := validateAdminSettingsInput(values["user"], values["pass"], values["notifyUrl"], values["returnUrl"], a.cfg.AllowInsecureDefaults, a.cfg.AllowPrivateCallbacks); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	if err := validateSharedSecret("merchant key", values["key"], a.cfg.AllowInsecureDefaults); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	if err := validateCloseMinutes(values["close"]); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	if err := validatePayDirection(values["payQf"]); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	if values["pass"] == "" {
		values["pass"] = current["pass"]
		if values["pass"] == "" {
			a.writeJSON(w, errorRes("后台密码不能为空"))
			return
		}
		if !isPasswordHash(values["pass"]) {
			hashedPass, err := hashAdminPassword(values["pass"])
			if err != nil {
				a.writeJSON(w, errorOnly())
				return
			}
			values["pass"] = hashedPass
		}
	} else {
		hashedPass, err := hashAdminPassword(values["pass"])
		if err != nil {
			a.writeJSON(w, errorRes("后台密码不合法"))
			return
		}
		values["pass"] = hashedPass
	}
	deviceKey := current["deviceKey"]
	if deviceKey == "" {
		deviceKey = newRandomHexSecret(32)
		values["deviceKey"] = deviceKey
	}
	if err := validateSharedSecret("device key", deviceKey, a.cfg.AllowInsecureDefaults); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	if secureEqual(values["key"], deviceKey) {
		a.writeJSON(w, errorRes("监控端密钥不能与商户通讯密钥相同"))
		return
	}
	if err := a.store.UpsertSettings(r.Context(), values); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	settings, err := a.store.GetSettings(r.Context())
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	settings["pass"] = ""
	a.writeJSON(w, successRes(settings))
}

func (a *App) handleAdminGetOrders(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, pageError("未登录"))
		return
	}
	_ = r.ParseForm()
	page := parsePositiveIntDefault(r.FormValue("page"), 1)
	limit := parsePositiveIntDefault(r.FormValue("limit"), 10)
	filter := OrderFilter{
		Type:  parseOptionalInt(r.FormValue("type")),
		State: parseOptionalInt(r.FormValue("state")),
	}
	orders, count, err := a.store.ListOrders(r.Context(), page, limit, filter)
	if err != nil {
		a.writeJSON(w, pageError("失败"))
		return
	}
	a.writeJSON(w, pageSuccess(count, orders))
}

func (a *App) handleAdminSetBd(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id == 0 {
		a.writeJSON(w, errorOnly())
		return
	}

	order, err := a.store.GetOrderByID(r.Context(), id)
	if err != nil || order == nil {
		a.writeJSON(w, errorRes("订单不存在"))
		return
	}

	key, err := a.store.GetSetting(r.Context(), "key")
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	query := buildNotifyQuery(order, key)
	notifyURL := order.NotifyURL
	if notifyURL == "" {
		notifyURL, _ = a.store.GetSetting(r.Context(), "notifyUrl")
		if notifyURL == "" {
			a.writeJSON(w, errorRes("您还未配置异步通知地址，请现在系统配置中配置"))
			return
		}
	}
	if err := validateOutboundCallbackURL(notifyURL, a.cfg.AllowPrivateCallbacks); err != nil {
		a.writeJSON(w, errorRes("异步通知地址不合法"))
		return
	}
	resp := a.sendNotifyGET(notifyURL, query)
	if resp != "success" {
		a.writeJSON(w, errorResCode(-2, resp))
		return
	}
	if order.State == 0 {
		_ = a.store.ReleasePrice(r.Context(), priceKey(order.Type, order.ReallyPrice))
	}
	order.State = 1
	if err := a.store.UpdateOrder(r.Context(), order); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminGetPayQRCodes(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, pageError("未登录"))
		return
	}
	_ = r.ParseForm()
	page := parsePositiveIntDefault(r.FormValue("page"), 1)
	limit := parsePositiveIntDefault(r.FormValue("limit"), 10)
	typeFilter := parseOptionalInt(r.FormValue("type"))
	items, count, err := a.store.ListQRCodes(r.Context(), page, limit, typeFilter)
	if err != nil {
		a.writeJSON(w, pageError("失败"))
		return
	}
	a.writeJSON(w, pageSuccess(count, items))
}

func (a *App) handleAdminDelPayQrcode(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := a.store.DeleteQRCode(r.Context(), id); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminAddPayQrcode(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	price, err := strconv.ParseFloat(r.FormValue("price"), 64)
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	payType, err := strconv.Atoi(r.FormValue("type"))
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	payURL := r.FormValue("payUrl")
	if payURL == "" || round2(price) == 0 || payType == 0 {
		a.writeJSON(w, errorOnly())
		return
	}
	if err := a.store.CreateQRCode(r.Context(), &PayQRCode{
		PayURL: payURL,
		Price:  round2(price),
		Type:   payType,
	}); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminGetMain(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	now := a.now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()).UnixMilli()
	stats, err := a.store.GetDashboardStats(r.Context(), start, end)
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successRes(map[string]string{
		"todayOrder":        strconv.FormatInt(stats.TodayOrder, 10),
		"todaySuccessOrder": strconv.FormatInt(stats.TodaySuccessOrder, 10),
		"todayCloseOrder":   strconv.FormatInt(stats.TodayCloseOrder, 10),
		"todayMoney":        formatMoney(stats.TodayMoney),
		"countOrder":        strconv.FormatInt(stats.CountOrder, 10),
		"countMoney":        formatMoney(stats.CountMoney),
	}))
}

func (a *App) handleAdminDelOrder(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	order, err := a.store.GetOrderByID(r.Context(), id)
	if err != nil || order == nil {
		a.writeJSON(w, errorOnly())
		return
	}
	if order.State == 0 {
		_ = a.store.ReleasePrice(r.Context(), priceKey(order.Type, order.ReallyPrice))
	}
	if err := a.store.DeleteOrder(r.Context(), id); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminDelGqOrder(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	if err := a.store.DeleteOrdersByState(r.Context(), -1); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAdminDelLastOrder(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	before := a.now().Add(-7 * 24 * time.Hour).UnixMilli()
	if err := a.store.DeleteOrdersBeforeCreateDate(r.Context(), before); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	payID := r.FormValue("payId")
	if payID == "" {
		a.writeJSON(w, errorRes("请传入商户订单号"))
		return
	}

	typeRaw := r.FormValue("type")
	if typeRaw == "" {
		a.writeJSON(w, errorRes("请传入支付方式=>1|微信 2|支付宝"))
		return
	}

	payType, err := strconv.Atoi(typeRaw)
	if err != nil || (payType != 1 && payType != 2) {
		a.writeJSON(w, errorRes("支付方式错误=>1|微信 2|支付宝"))
		return
	}

	priceRaw := r.FormValue("price")
	if err := validateCreateOrderInput(payID, r.FormValue("param"), priceRaw, r.FormValue("notifyUrl"), r.FormValue("returnUrl")); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	price, _ := strconv.ParseFloat(priceRaw, 64)

	sign := r.FormValue("sign")
	if sign == "" {
		a.writeJSON(w, errorRes("请传入签名"))
		return
	}

	param := r.FormValue("param")
	if isEpayOrderParam(param) {
		a.writeJSON(w, errorRes("param使用了系统保留前缀"))
		return
	}
	isHTML := parsePositiveIntDefault(r.FormValue("isHtml"), 0)
	res := a.createOrder(r.Context(), payID, param, payType, priceRaw, round2(price), r.FormValue("notifyUrl"), r.FormValue("returnUrl"), sign)
	if isHTML == 0 {
		a.writeJSON(w, res)
		return
	}
	applySensitiveNoStoreHeaders(w)
	if res.Data == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, res.Msg)
		return
	}
	orderRes, _ := res.Data.(CreateOrderRes)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<script>window.location.href = '/payPage/pay.html?orderId="+url.QueryEscape(orderRes.OrderID)+"&token="+url.QueryEscape(orderRes.AccessToken)+"'</script>")
}

func (a *App) createOrder(ctx context.Context, payID, param string, payType int, priceRaw string, price float64, notifyURL, returnURL, sign string) CommonRes {
	key, err := a.merchantKey(ctx)
	if err != nil {
		return errorOnly()
	}
	expectedSign := md5Hex(payID + param + strconv.Itoa(payType) + priceRaw + key)
	if !secureEqual(sign, expectedSign) {
		return errorRes("签名校验不通过")
	}
	if notifyURL != "" {
		if err := validateOutboundCallbackURL(notifyURL, a.cfg.AllowPrivateCallbacks); err != nil {
			return errorRes("notifyUrl不合法")
		}
	}
	if returnURL != "" {
		if err := validateCallbackURL(returnURL, maxReturnURLLength, true); err != nil {
			return errorRes("returnUrl不合法")
		}
	}

	orderID := a.newOrderID()
	payQfRaw, _ := a.store.GetSetting(ctx, "payQf")
	payQf, _ := strconv.Atoi(payQfRaw)

	reallyPrice := round2(price)
	reservedKey := ""
	for {
		reservedKey = priceKey(payType, reallyPrice)
		ok, err := a.store.ReservePrice(ctx, reservedKey)
		if err != nil {
			return errorOnly()
		}
		if ok {
			break
		}
		if payQf == 1 {
			reallyPrice = round2(reallyPrice + 0.01)
		} else {
			reallyPrice = round2(reallyPrice - 0.01)
		}
		if reallyPrice <= 0 {
			return errorRes("所有金额均被占用")
		}
	}

	payURL, _ := a.store.GetSetting(ctx, map[int]string{1: "wxpay", 2: "zfbpay"}[payType])
	if payURL == "" {
		_ = a.store.ReleasePrice(ctx, reservedKey)
		return errorRes("请您先进入后台配置程序")
	}

	isAuto := 1
	code, err := a.store.GetQRCodeByPriceAndType(ctx, reallyPrice, payType)
	if err != nil {
		_ = a.store.ReleasePrice(ctx, reservedKey)
		return errorOnly()
	}
	if code != nil {
		payURL = code.PayURL
		isAuto = 0
	}

	existing, err := a.store.GetOrderByPayID(ctx, payID)
	if err != nil {
		_ = a.store.ReleasePrice(ctx, reservedKey)
		return errorOnly()
	}
	if existing != nil {
		_ = a.store.ReleasePrice(ctx, reservedKey)
		return errorRes("商户订单号已存在！")
	}

	order := &PayOrder{
		OrderID:     orderID,
		PayID:       payID,
		CreateDate:  a.now().UnixMilli(),
		PayDate:     0,
		CloseDate:   0,
		Param:       param,
		Type:        payType,
		Price:       price,
		ReallyPrice: reallyPrice,
		NotifyURL:   notifyURL,
		ReturnURL:   returnURL,
		State:       0,
		IsAuto:      isAuto,
		PayURL:      payURL,
	}
	if err := a.store.CreateOrder(ctx, order); err != nil {
		_ = a.store.ReleasePrice(ctx, reservedKey)
		return errorOnly()
	}

	timeOut, _ := a.store.GetSetting(ctx, "close")
	timeoutMinutes, _ := strconv.Atoi(timeOut)
	return successRes(CreateOrderRes{
		PayID:       payID,
		OrderID:     orderID,
		AccessToken: orderAccessToken(orderID, a.cfg.SessionSecret),
		PayType:     payType,
		Price:       price,
		ReallyPrice: reallyPrice,
		PayURL:      payURL,
		IsAuto:      isAuto,
		State:       0,
		TimeOut:     timeoutMinutes,
		Date:        order.CreateDate,
	})
}

func (a *App) handleCloseOrder(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	orderID := r.FormValue("orderId")
	if orderID == "" {
		a.writeJSON(w, errorRes("请传入云端订单号"))
		return
	}
	sign := r.FormValue("sign")
	key, err := a.merchantKey(r.Context())
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	token := r.FormValue("token")
	if sign == "" && token == "" {
		a.writeJSON(w, errorRes("请传入签名"))
		return
	}
	if token != "" {
		if !secureEqual(token, orderAccessToken(orderID, a.cfg.SessionSecret)) {
			a.writeJSON(w, errorRes("签名校验不通过"))
			return
		}
	} else if !secureEqual(sign, md5Hex(orderID+key)) {
		a.writeJSON(w, errorRes("签名校验不通过"))
		return
	}
	order, err := a.store.GetOrderByOrderID(r.Context(), orderID)
	if err != nil || order == nil {
		a.writeJSON(w, errorRes("云端订单编号不存在"))
		return
	}
	if order.State != 0 {
		a.writeJSON(w, errorRes("订单状态不允许关闭"))
		return
	}
	_ = a.store.ReleasePrice(r.Context(), priceKey(order.Type, order.ReallyPrice))
	order.CloseDate = a.now().UnixMilli()
	order.State = -1
	if err := a.store.UpdateOrder(r.Context(), order); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successOnly())
}

func (a *App) handleAppHeart(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	timestamp := r.FormValue("t")
	sign := r.FormValue("sign")
	res := a.handleAppHeartLogic(r.Context(), timestamp, sign)
	a.writeJSON(w, res)
}

func (a *App) handleAppHeartLogic(ctx context.Context, timestamp, sign string) CommonRes {
	key, err := a.deviceKey(ctx)
	if err != nil {
		return errorOnly()
	}
	if !secureEqual(sign, md5Hex(timestamp+key)) {
		return errorRes("签名校验错误")
	}
	if !withinSignedRequestWindow(a.now(), timestamp, 50*time.Second) {
		return errorRes("客户端时间错误")
	}
	if err := a.store.UpsertSettings(ctx, map[string]string{
		"lastheart": timestamp,
		"jkstate":   "1",
	}); err != nil {
		return errorOnly()
	}
	return successOnly()
}

func (a *App) handleAppPush(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	payType, err := strconv.Atoi(r.FormValue("type"))
	if err != nil || (payType != 1 && payType != 2) {
		a.writeJSON(w, errorOnly())
		return
	}
	res := a.handleAppPushLogic(r.Context(), payType, r.FormValue("price"), r.FormValue("t"), r.FormValue("sign"))
	a.writeJSON(w, res)
}

func (a *App) handleAppPushLogic(ctx context.Context, payType int, priceRaw, timestamp, sign string) CommonRes {
	key, err := a.deviceKey(ctx)
	if err != nil {
		return errorOnly()
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errorOnly()
	}
	if !withinSignedRequestWindow(a.now(), timestamp, 50*time.Second) {
		return errorRes("客户端时间错误")
	}
	if !secureEqual(sign, md5Hex(strconv.Itoa(payType)+priceRaw+timestamp+key)) {
		return errorRes("签名校验错误")
	}
	if err := a.store.UpsertSettings(ctx, map[string]string{"lastpay": timestamp}); err != nil {
		return errorOnly()
	}
	existing, err := a.store.GetOrderByPayDate(ctx, ts)
	if err != nil {
		return errorOnly()
	}
	if existing != nil {
		return errorRes("重复推送")
	}

	price, err := strconv.ParseFloat(priceRaw, 64)
	if err != nil {
		return errorOnly()
	}
	price = round2(price)
	nowMillis := a.now().UnixMilli()
	order, err := a.store.MarkOrderPaidByPrice(ctx, price, payType, ts, nowMillis)
	if err != nil {
		return errorOnly()
	}
	if order == nil {
		return errorRes("未匹配到待支付订单")
	}

	if epayType, callbackParam, ok := parseEpayOrderParam(order.Param); ok {
		resp := a.sendEpayNotify(ctx, order, epayType, callbackParam)
		if resp == epayCallbackSuccess {
			return successOnly()
		}
		order.State = 2
		_ = a.store.UpdateOrder(ctx, order)
		return errorRes("通知易支付异步地址失败")
	}

	merchantKey, err := a.merchantKey(ctx)
	if err != nil {
		return errorOnly()
	}
	query := buildNotifyQuery(order, merchantKey)
	notifyURL := order.NotifyURL
	if notifyURL == "" {
		notifyURL, _ = a.store.GetSetting(ctx, "notifyUrl")
		if notifyURL == "" {
			order.State = 2
			_ = a.store.UpdateOrder(ctx, order)
			return errorRes("您还未配置异步通知地址，请现在系统配置中配置")
		}
	}
	if err := validateOutboundCallbackURL(notifyURL, a.cfg.AllowPrivateCallbacks); err != nil {
		order.State = 2
		_ = a.store.UpdateOrder(ctx, order)
		return errorRes("异步通知地址不合法")
	}
	resp := a.sendNotifyGET(notifyURL, query)
	if resp == "success" {
		return successOnly()
	}
	order.State = 2
	_ = a.store.UpdateOrder(ctx, order)
	return errorRes("通知异步地址失败")
}

func (a *App) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	orderID := r.FormValue("orderId")
	if orderID == "" {
		a.writeJSON(w, errorRes("请传入订单编号"))
		return
	}
	if err := a.authorizeOrderRead(r.Context(), orderID, r.FormValue("sign"), r.FormValue("token")); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	order, err := a.store.GetOrderByOrderID(r.Context(), orderID)
	if err != nil || order == nil {
		a.writeJSON(w, errorRes("云端订单编号不存在"))
		return
	}
	timeOutRaw, _ := a.store.GetSetting(r.Context(), "close")
	timeout, _ := strconv.Atoi(timeOutRaw)
	a.writeJSON(w, successRes(CreateOrderRes{
		PayID:       order.PayID,
		OrderID:     order.OrderID,
		AccessToken: orderAccessToken(order.OrderID, a.cfg.SessionSecret),
		PayType:     order.Type,
		Price:       order.Price,
		ReallyPrice: order.ReallyPrice,
		PayURL:      order.PayURL,
		IsAuto:      order.IsAuto,
		State:       order.State,
		TimeOut:     timeout,
		Date:        order.CreateDate,
	}))
}

func (a *App) handleCheckOrder(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	orderID := r.FormValue("orderId")
	if orderID == "" {
		a.writeJSON(w, errorRes("请传入订单编号"))
		return
	}
	if err := a.authorizeOrderRead(r.Context(), orderID, r.FormValue("sign"), r.FormValue("token")); err != nil {
		a.writeJSON(w, errorRes(err.Error()))
		return
	}
	order, err := a.store.GetOrderByOrderID(r.Context(), orderID)
	if err != nil || order == nil {
		a.writeJSON(w, errorRes("云端订单编号不存在"))
		return
	}
	if order.State == 0 {
		a.writeJSON(w, errorRes("订单未支付"))
		return
	}
	if order.State == -1 {
		a.writeJSON(w, errorRes("订单已过期"))
		return
	}
	if _, _, ok := parseEpayOrderParam(order.Param); ok {
		if order.ReturnURL != "" {
			if err := validateCallbackURL(order.ReturnURL, maxReturnURLLength, true); err != nil {
				a.writeJSON(w, errorRes("returnUrl不合法"))
				return
			}
		}
		a.writeJSON(w, successRes(order.ReturnURL))
		return
	}
	key, err := a.store.GetSetting(r.Context(), "key")
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	query := buildNotifyQuery(order, key)
	target := order.ReturnURL
	if target == "" {
		target, _ = a.store.GetSetting(r.Context(), "returnUrl")
	}
	if target != "" {
		if err := validateCallbackURL(target, maxReturnURLLength, true); err != nil {
			a.writeJSON(w, errorRes("returnUrl不合法"))
			return
		}
	}
	a.writeJSON(w, successRes(target+"?"+query))
}

func (a *App) handleGetState(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	timestamp := r.FormValue("t")
	sign := r.FormValue("sign")
	if timestamp == "" {
		a.writeJSON(w, errorRes("请传入t"))
		return
	}
	if sign == "" {
		a.writeJSON(w, errorRes("请传入sign"))
		return
	}
	key, err := a.deviceKey(r.Context())
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	if !secureEqual(sign, md5Hex(timestamp+key)) {
		a.writeJSON(w, errorRes("签名校验不通过"))
		return
	}
	if !withinSignedRequestWindow(a.now(), timestamp, 50*time.Second) {
		a.writeJSON(w, errorRes("客户端时间错误"))
		return
	}
	settings, err := a.store.GetSettings(r.Context())
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successRes(map[string]string{
		"state":     settings["jkstate"],
		"lastheart": settings["lastheart"],
		"lastpay":   settings["lastpay"],
	}))
}

func (a *App) handleEncodeQRCode(w http.ResponseWriter, r *http.Request) {
	applySensitiveNoStoreHeaders(w)
	content := r.URL.Query().Get("url")
	if content == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	image, err := encodeQRCode(content)
	if err != nil {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(image)
}

func (a *App) handleDecodeQRCode(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	_ = r.ParseForm()
	content, err := decodeQRCodeFromBase64(r.FormValue("base64"))
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successRes(content))
}

func (a *App) handleDecodeQRCodeFile(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		a.writeJSON(w, errorRes("未登录"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	defer file.Close()

	payload, err := io.ReadAll(file)
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	content, err := decodeQRCodeBytes(payload)
	if err != nil {
		a.writeJSON(w, errorOnly())
		return
	}
	a.writeJSON(w, successRes(content))
}

func (a *App) writeJSON(w http.ResponseWriter, payload any) {
	applySensitiveNoStoreHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func (a *App) sendNotifyGET(target, query string) string {
	if err := validateOutboundCallbackURL(target, a.cfg.AllowPrivateCallbacks); err != nil {
		return "异步通知地址不合法"
	}
	targetURL, err := url.Parse(target)
	if err != nil {
		return "服务器无响应"
	}
	if targetURL.RawQuery == "" {
		targetURL.RawQuery = query
	} else {
		targetURL.RawQuery += "&" + query
	}
	req, err := http.NewRequest(http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return "服务器无响应"
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("User-Agent", "Mozilla/4.0 (compatible; MSIE 6.0; Windows NT 5.1;SV1)")
	resp, err := a.client.Do(req)
	if err != nil {
		return "服务器无响应"
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "异步通知返回非2xx状态"
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "服务器无响应"
	}
	return strings.TrimSpace(string(body))
}

func (a *App) newOrderID() string {
	now := a.now().Format("20060102150405")
	suffix, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000_000_000))
	if err != nil {
		return now + strconv.FormatInt(a.now().UnixNano(), 10)
	}
	return now + fmt.Sprintf("%018d", suffix.Int64())
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func parsePositiveIntDefault(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseOptionalInt(raw string) *int {
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &value
}

func priceKey(payType int, price float64) string {
	return fmt.Sprintf("%d-%s", payType, strconv.FormatFloat(round2(price), 'f', -1, 64))
}

func buildNotifyQuery(order *PayOrder, key string) string {
	sign := md5Hex(order.PayID + order.Param + strconv.Itoa(order.Type) + formatRawFloat(order.Price) + formatRawFloat(order.ReallyPrice) + key)
	return "payId=" + url.QueryEscape(order.PayID) +
		"&param=" + url.QueryEscape(order.Param) +
		"&type=" + url.QueryEscape(strconv.Itoa(order.Type)) +
		"&price=" + url.QueryEscape(formatRawFloat(order.Price)) +
		"&reallyPrice=" + url.QueryEscape(formatRawFloat(order.ReallyPrice)) +
		"&sign=" + url.QueryEscape(sign)
}

func formatRawFloat(value float64) string {
	return strconv.FormatFloat(round2(value), 'f', -1, 64)
}

func formatMoney(value float64) string {
	return strconv.FormatFloat(round2(value), 'f', -1, 64)
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func (a *App) authorizeOrderRead(ctx context.Context, orderID, sign, token string) error {
	if sign == "" && token == "" {
		return fmt.Errorf("请传入签名")
	}
	if token != "" {
		if !secureEqual(token, orderAccessToken(orderID, a.cfg.SessionSecret)) {
			return fmt.Errorf("签名校验不通过")
		}
		return nil
	}
	key, err := a.merchantKey(ctx)
	if err != nil {
		return fmt.Errorf("失败")
	}
	if !secureEqual(sign, md5Hex(orderID+key)) {
		return fmt.Errorf("签名校验不通过")
	}
	return nil
}

func (a *App) merchantKey(ctx context.Context) (string, error) {
	return a.store.GetSetting(ctx, "key")
}

func (a *App) deviceKey(ctx context.Context) (string, error) {
	return a.store.GetSetting(ctx, "deviceKey")
}

func (a *App) clientIP(r *http.Request) string {
	if a.clientIPs == nil {
		return clientKey(r)
	}
	return a.clientIPs.clientIP(r)
}

func WebRootExists(path string) error {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("web root %s is not a directory", path)
	}
	return nil
}
