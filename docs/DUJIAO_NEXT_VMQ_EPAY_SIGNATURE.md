# Dujiao-Next 与 VMQ 易支付参数防篡改说明

本文说明 Dujiao-Next 通过 VMQ 易支付适配层完成支付时，`money`、`notify_url`、`return_url`、`out_trade_no` 等参数如何防止被中间人或客户端篡改。

## 1. 总体边界

Dujiao-Next 和 VMQ 共享同一组易支付身份信息：

```text
EPAY_MERCHANT_ID
EPAY_MERCHANT_KEY
```

其中 `EPAY_MERCHANT_KEY` 只应保存在 Dujiao-Next 后台配置和 VMQ 服务端环境变量中。参数完整性主要靠这个密钥生成的易支付签名保证。

VMQ 中对应实现：

- `/mapi.php` 和 `/submit.php` 易支付入口：`internal/app/epay_adapter.go`
- VMQ 内部订单创建：`internal/app/app.go`
- 监控端收款推送：`internal/app/app.go`
- VMQ 回调 Dujiao-Next：`internal/app/epay_adapter.go`

## 2. Dujiao-Next 请求 VMQ 时的签名保护

Dujiao-Next 向 VMQ 创建支付订单时，请求 VMQ：

```text
POST https://vmq.example.com/mapi.php
```

核心参数：

```text
pid=<EPAY_MERCHANT_ID>
type=alipay|wxpay|wechat
out_trade_no=<Dujiao-Next订单号>
notify_url=https://shop.example.com/api/v1/payments/callback
return_url=https://shop.example.com/payment/return
name=<商品或订单名称>
money=<Dujiao-Next原始订单金额>
param=<Dujiao-Next需要回传的参数>
sign_type=MD5
sign=<易支付签名>
```

VMQ 校验签名时会：

1. 去掉 `sign` 和 `sign_type`。
2. 去掉空值字段。
3. 按参数名排序。
4. 拼接为 `key=value&key=value`。
5. 末尾追加 `EPAY_MERCHANT_KEY`。
6. 计算 MD5 并与请求中的 `sign` 做常量时间比较。

因此以下字段都在 Dujiao-Next 到 VMQ 的签名保护范围内：

```text
pid
type
out_trade_no
notify_url
return_url
name
money
param
```

只要中途有人篡改 `money`、`notify_url`、`return_url`、`out_trade_no`、`param` 等任意已签名字段，VMQ 重新计算出的签名就会不一致，并返回“签名校验不通过”。

## 3. VMQ 创建内部订单后的保存方式

VMQ 只有在签名校验通过后才创建内部订单。字段映射如下：

```text
Dujiao out_trade_no -> VMQ PayID
Dujiao money        -> VMQ Price
Dujiao notify_url   -> VMQ NotifyURL
Dujiao return_url   -> VMQ ReturnURL
Dujiao type         -> VMQ Type
Dujiao param        -> VMQ Param 内的易支付标记
```

VMQ 会把 Dujiao 的 `param` 包装成内部保留格式：

```text
__epay_v1:<epay_type>|<base64url(param)>
```

这个标记用于区分该订单来自易支付适配层，避免和 VMQ 原生商户接口混淆。后续回调时，VMQ 会解析该标记并恢复 Dujiao 原始 `param`。

如果 Dujiao-Next 使用同一个 `out_trade_no` 重试创建订单，VMQ 不会直接覆盖旧订单，而是检查这些字段是否与旧订单一致：

```text
type
money
notify_url
return_url
param
```

如果任何字段不一致，VMQ 会拒绝该请求，避免攻击者用相同订单号替换金额或回调地址。

## 4. VMQ 内部金额区分与 Dujiao 原始金额

VMQ 内部有两个金额概念：

```text
Price       = Dujiao-Next 原始订单金额
ReallyPrice = VMQ 用于收款匹配的实际金额
```

例如 Dujiao 订单金额是：

```text
money=20.00
```

为了区分同时支付的订单，VMQ 可能把实际收款金额调整为：

```text
ReallyPrice=20.01
```

监控端上报收款时匹配的是 `ReallyPrice`。但是 VMQ 回调 Dujiao-Next 时使用的是原始 `Price`，也就是 Dujiao-Next 发来的 `money`。这样 Dujiao-Next 校验金额时不会因为 VMQ 内部金额区分而失败。

## 5. 监控端收款推送的防篡改

用户扫码支付后，VMQ 监控端向 VMQ 推送收款结果：

```text
POST /appPush
type=1|2
price=<实际收款金额>
t=<毫秒时间戳>
sign=md5(type + price + t + deviceKey)
```

这里使用的是 VMQ 监控端密钥 `deviceKey`，不是 `EPAY_MERCHANT_KEY`。

VMQ 校验：

- `type` 必须是 `1` 或 `2`。
- `t` 必须在允许时间窗口内。
- `sign` 必须等于 `md5(type + price + t + deviceKey)`。
- 同一个收款时间 `pay_date` 不能重复入账。

这能防止未持有 `deviceKey` 的请求伪造收款推送。

## 6. VMQ 回调 Dujiao-Next 时的签名保护

VMQ 确认收款后，会向 Dujiao-Next 的 `notify_url` 发送易支付回调：

```text
POST https://shop.example.com/api/v1/payments/callback
Content-Type: application/x-www-form-urlencoded
```

回调参数：

```text
pid=<EPAY_MERCHANT_ID>
type=alipay|wxpay
out_trade_no=<Dujiao-Next原订单号>
trade_no=<VMQ内部订单号>
trade_status=TRADE_SUCCESS
money=<Dujiao-Next原始订单金额>
name=VMQ order <Dujiao-Next原订单号>
endtime=<Unix秒级付款时间>
param=<Dujiao-Next原param>
sign_type=MD5
sign=<易支付签名>
```

VMQ 生成 `sign` 时同样使用 `EPAY_MERCHANT_KEY`。Dujiao-Next 收到回调后也用同一密钥验签。

因此以下回调字段被签名保护：

```text
pid
type
out_trade_no
trade_no
trade_status
money
name
endtime
param
```

如果有人把：

```text
money=20.00
```

改成：

```text
money=0.01
```

或者把：

```text
trade_status=TRADE_SUCCESS
```

改成其他值，Dujiao-Next 重新计算签名时都会失败。

## 7. return_url 的保护边界

`return_url` 是用户支付后的前端回跳地址，不是支付成功确认依据。

它的保护方式是：

1. Dujiao-Next 创建订单时，`return_url` 已经参与 Dujiao -> VMQ 的易支付签名。
2. VMQ 签名校验通过后，把 `return_url` 保存到订单。
3. 后续支付页或 `checkOrder` 只使用数据库中保存的 `return_url`。
4. VMQ 回调 Dujiao-Next 时不重新携带 `return_url`，因为 Dujiao-Next 标记支付成功依赖的是 `notify_url` 的服务端回调，而不是浏览器跳转。

也就是说，`return_url` 在下单阶段防篡改；支付成功状态不依赖 `return_url`。

## 8. notify_url 的保护边界

`notify_url` 是 VMQ 服务端回调 Dujiao-Next 的地址。

它的保护方式是：

1. Dujiao-Next 创建订单时，`notify_url` 已参与易支付签名。
2. VMQ 校验签名通过后才保存 `notify_url`。
3. VMQ 回调前会校验 `notify_url` 是否是合法 HTTP/HTTPS URL。
4. 生产环境 `ALLOW_PRIVATE_CALLBACKS=0` 时，VMQ 会拒绝回调 localhost、私有 IP、内网地址，避免 SSRF。

攻击者如果在 Dujiao -> VMQ 请求途中替换 `notify_url`，签名会失败；如果直接构造没有正确签名的请求，也会失败。

## 9. VMQ 原生接口与易支付适配层的区别

VMQ 原生 `/createOrder` 接口的签名格式是：

```text
md5(payId + param + type + price + merchantKey)
```

它主要保护 VMQ 原生商户接口中的核心下单字段。

Dujiao-Next 不走这个接口，而是走易支付适配层 `/mapi.php`。易支付适配层使用的是易支付标准签名，覆盖 `money`、`notify_url`、`return_url` 等请求参数。

因此对 Dujiao-Next 接入场景，应以 `/mapi.php` 的易支付签名边界为准。

## 10. 安全前提

上述防篡改机制成立需要满足以下前提：

```text
EPAY_MERCHANT_KEY 只在 Dujiao-Next 和 VMQ 服务端保存
VMQ 与 Dujiao-Next 使用 HTTPS
Dujiao-Next 的 merchant_key 与 VMQ 的 EPAY_MERCHANT_KEY 完全一致
不要把 EPAY_MERCHANT_KEY 配到浏览器、监控端或前台页面
不要把 EPAY_MERCHANT_KEY 与 VMQ 原生商户 key、deviceKey 混用
生产环境保持 ALLOW_PRIVATE_CALLBACKS=0
```

如果 `EPAY_MERCHANT_KEY` 泄露，攻击者就可以伪造合法签名，因此密钥隔离和服务端保存是最重要的安全边界。
