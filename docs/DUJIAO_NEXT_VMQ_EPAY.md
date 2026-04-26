# Dujiao-Next 接入 VMQ 易支付教程

本文档说明如何把本项目作为 Dujiao-Next 的易支付网关使用。推荐方式是把 VMQ 独立部署成外部支付服务，然后在 Dujiao-Next 后台新增 `epay` 支付渠道，不需要修改 Dujiao-Next 源码。

参考 Dujiao-Next 当前实现：

- `internal/payment/epay/epay.go`：`epay v1` 默认请求网关的 `/mapi.php`
- `internal/router/router.go`：支付回调入口为 `/api/v1/payments/callback`
- `internal/http/handlers/public/payment_callback_epay.go`：易支付回调处理

## 1. 对接流程

1. 用户在 Dujiao-Next 下单并选择支付宝或微信支付。
2. Dujiao-Next 使用 `epay v1` 向 VMQ 的 `/mapi.php` 发起下单请求。
3. VMQ 校验 `pid`、`sign_type`、MD5 签名、金额和回调地址，创建内部订单。
4. VMQ 返回 `payurl`，Dujiao-Next 将用户跳转到 VMQ 支付页。
5. 用户按 VMQ 支付页提示扫码付款。
6. VMQ 监控端确认收款后推送到 VMQ，VMQ 匹配订单并向 Dujiao-Next `/api/v1/payments/callback` 发送易支付成功回调。
7. Dujiao-Next 校验 VMQ 回调签名和金额后，把支付单标记为已支付。

## 2. 前置条件

- VMQ 已部署到公网 HTTPS 域名，例如 `https://vmq.example.com`。
- Dujiao-Next 已部署到公网 HTTPS 域名，例如 `https://dujiao.example.com`。
- VMQ 后台已经配置好支付宝或微信收款码。
- VMQ 监控端能正常在线，并且使用 `deviceKey` 推送收款结果。
- Dujiao-Next 服务器能访问 VMQ 域名，VMQ 服务器也能访问 Dujiao-Next 回调地址。

## 3. 配置 VMQ

编辑 VMQ 的 `.env`：

```env
EPAY_MERCHANT_ID=1000
EPAY_MERCHANT_KEY=replace-with-a-random-string-at-least-32-chars
EPAY_PUBLIC_BASE_URL=https://vmq.example.com
COOKIE_SECURE=1
ALLOW_PRIVATE_CALLBACKS=0
```

字段说明：

- `EPAY_MERCHANT_ID`：Dujiao-Next 中填写的易支付商户号，默认可用 `1000`。
- `EPAY_MERCHANT_KEY`：Dujiao-Next 中填写的易支付商户密钥，生产环境建议使用独立随机字符串。
- `EPAY_PUBLIC_BASE_URL`：VMQ 的公网访问地址。反代或容器部署时必须配置，避免 VMQ 返回内网地址或 HTTP 地址。
- `COOKIE_SECURE`：公网 HTTPS 部署时建议设为 `1`。
- `ALLOW_PRIVATE_CALLBACKS`：生产环境保持 `0`，避免支付回调被用作内网请求。

如果 `EPAY_MERCHANT_KEY` 留空，VMQ 会回退使用后台系统设置里的商户通讯密钥 `key`。不建议生产环境这样做，因为 Dujiao-Next 的易支付密钥应和 VMQ 原生商户密钥隔离。

重启 VMQ：

```bash
docker compose up -d --build
```

如果你使用 GHCR 镜像：

```bash
docker compose -f docker-compose.ghcr.yml up -d
```

## 4. 配置 Dujiao-Next 支付渠道

在 Dujiao-Next 后台进入支付渠道管理，新增支付宝渠道。

建议字段如下：

```yaml
provider_type: epay
channel_type: alipay
interaction_mode: redirect
gateway_url: https://vmq.example.com
epay_version: v1
merchant_id: "1000"
merchant_key: "replace-with-a-random-string-at-least-32-chars"
api_path: /mapi.php
notify_url: https://dujiao.example.com/api/v1/payments/callback
return_url: https://dujiao.example.com/payment/return
sign_type: MD5
device: pc
```

如果后台是 JSON 配置框，可使用：

```json
{
  "gateway_url": "https://vmq.example.com",
  "epay_version": "v1",
  "merchant_id": "1000",
  "merchant_key": "replace-with-a-random-string-at-least-32-chars",
  "api_path": "/mapi.php",
  "notify_url": "https://dujiao.example.com/api/v1/payments/callback",
  "return_url": "https://dujiao.example.com/payment/return",
  "sign_type": "MD5",
  "device": "pc"
}
```

微信渠道再新增一条，保持其他配置不变，只修改：

```yaml
channel_type: wechat
```

或：

```yaml
channel_type: wxpay
```

注意：

- VMQ 当前只支持 `alipay`、`wechat`、`wxpay`。
- 不要配置 `qqpay`，VMQ 会拒绝该渠道。
- `interaction_mode` 推荐使用 `redirect`，因为 VMQ 返回的是支付页 `payurl`，不是直接返回二维码内容。
- `merchant_id` 必须等于 VMQ 的 `EPAY_MERCHANT_ID`。
- `merchant_key` 必须等于 VMQ 的 `EPAY_MERCHANT_KEY`。

## 5. 快速验证

先确认 VMQ 易支付入口存在：

```bash
curl -i https://vmq.example.com/mapi.php
```

正常情况下会返回 `405 Method Not Allowed` 和 `method not allowed`，说明路由存在但要求 `POST`。

再确认跳转接口存在：

```bash
curl -i https://vmq.example.com/submit.php
```

正常情况下会返回参数或签名相关错误，说明路由已被 VMQ 接管。

完整验证建议按真实支付链路执行：

1. 在 Dujiao-Next 创建一个小额测试订单。
2. 选择刚配置的 `epay` 支付渠道。
3. 页面应跳转到 VMQ 的 `/payPage/pay.html?orderId=...&token=...`。
4. 在 VMQ 后台订单列表确认出现对应订单，商户订单号应是 Dujiao-Next 的订单号。
5. 使用 VMQ 监控端确认收款。
6. Dujiao-Next 支付记录应变为已支付。
7. Dujiao-Next 订单应进入后续发货或履约流程。

## 6. 回调字段说明

VMQ 确认收款后，会向 Dujiao-Next 的 `notify_url` 发送 `application/x-www-form-urlencoded` 回调，关键字段如下：

```text
pid=1000
type=alipay
out_trade_no=<Dujiao-Next订单号>
trade_no=<VMQ内部订单号>
trade_status=TRADE_SUCCESS
money=<Dujiao-Next原始订单金额>
param=<Dujiao-Next支付单ID>
sign_type=MD5
sign=<MD5签名>
```

重要细节：

- VMQ 内部可能使用 `reallyPrice` 做金额区分，例如 `10.00` 变为 `10.01`。
- 回调给 Dujiao-Next 时，VMQ 使用 Dujiao-Next 原始订单金额 `money`，避免 Dujiao-Next 金额校验失败。
- Dujiao-Next 下单时传入的 `param` 会被 VMQ 保存并回传，用于 Dujiao-Next 匹配支付单。

## 7. 常见问题

### Dujiao-Next 提示易支付请求失败

检查：

- `gateway_url` 是否能从 Dujiao-Next 服务器访问。
- VMQ 是否已经部署新版本并包含 `/mapi.php`。
- `api_path` 是否为 `/mapi.php`，或留空使用 Dujiao-Next 的 v1 默认值。
- VMQ 后台是否已经配置对应收款码。

### VMQ 返回签名校验不通过

检查：

- Dujiao-Next `merchant_key` 是否和 VMQ `EPAY_MERCHANT_KEY` 完全一致。
- Dujiao-Next `merchant_id` 是否和 VMQ `EPAY_MERCHANT_ID` 一致。
- `sign_type` 是否为 `MD5`。
- 不要在密钥前后多加空格。

### 用户没有跳转到 VMQ 公网域名

检查：

- VMQ `.env` 是否设置 `EPAY_PUBLIC_BASE_URL=https://vmq.example.com`。
- Nginx 是否正确转发 `Host` 和 `X-Forwarded-Proto`。
- 如果在 Cloudflare 或反代后面，优先固定配置 `EPAY_PUBLIC_BASE_URL`，不要依赖请求头推断。

### 支付成功但 Dujiao-Next 没有变为已支付

检查：

- Dujiao-Next `notify_url` 是否为 `https://dujiao.example.com/api/v1/payments/callback`。
- VMQ 服务器是否能访问 Dujiao-Next 的 `notify_url`。
- Dujiao-Next 是否返回纯文本 `success`。
- VMQ `ALLOW_PRIVATE_CALLBACKS=0` 时，不能回调内网、localhost 或私有 IP。
- Dujiao-Next `merchant_key` 是否和 VMQ `EPAY_MERCHANT_KEY` 一致。

### 微信支付渠道失败

检查：

- Dujiao-Next `channel_type` 使用 `wechat` 或 `wxpay`。
- VMQ 后台已经配置微信收款码。
- VMQ 监控端微信通道在线。

### QQ 钱包不可用

VMQ 当前易支付适配层不支持 `qqpay`。如果 Dujiao-Next 配置了 `qqpay`，VMQ 会返回支付方式错误。

## 8. 安全清单

上线前逐项确认：

- `EPAY_MERCHANT_KEY` 是独立随机密钥，长度不少于 32 位。
- Dujiao-Next 后台没有把 `merchant_key` 暴露给普通管理员或前台页面。
- VMQ 后台商户通讯密钥 `key` 和监控端密钥 `deviceKey` 保持不同。
- VMQ 与 Dujiao-Next 都使用 HTTPS。
- `ALLOW_PRIVATE_CALLBACKS=0`，除非你明确是在内网测试。
- 后台管理路径不要裸露在公网，至少加反代访问控制或强密码。
- 监控端只使用 `deviceKey`，不要把 VMQ 商户密钥或 `EPAY_MERCHANT_KEY` 配到监控端。

## 9. 生产推荐配置摘要

VMQ：

```env
EPAY_MERCHANT_ID=1000
EPAY_MERCHANT_KEY=<32位以上随机密钥>
EPAY_PUBLIC_BASE_URL=https://vmq.example.com
COOKIE_SECURE=1
ALLOW_PRIVATE_CALLBACKS=0
```

Dujiao-Next 支付渠道：

```yaml
provider_type: epay
epay_version: v1
gateway_url: https://vmq.example.com
api_path: /mapi.php
merchant_id: "1000"
merchant_key: "<同 VMQ EPAY_MERCHANT_KEY>"
notify_url: https://dujiao.example.com/api/v1/payments/callback
return_url: https://dujiao.example.com/payment/return
sign_type: MD5
device: pc
interaction_mode: redirect
```

支付宝渠道：

```yaml
channel_type: alipay
```

微信渠道：

```yaml
channel_type: wechat
```
