# V免签 Go 版部署文档

本文档描述当前仓库的推荐部署方式：`Docker Compose + PostgreSQL`。

当前仓库同时支持两种容器部署模式：

- 本地源码构建：使用 `docker-compose.yml`
- 直接拉取 GitHub 自动构建镜像：使用 `docker-compose.ghcr.yml`

如果你需要版本发布规则说明，可参考：

- [`docs/RELEASING.md`](RELEASING.md)

## 1. 部署前准备

- 一台可运行 Docker 的 Linux 服务器
- 已安装 `docker` 与 `docker compose`
- 服务器开放业务端口，例如 `8080`
- 如需公网 HTTPS，请准备反向代理或负载均衡

建议最低检查：

```bash
docker --version
docker compose version
```

## 2. 获取代码

```bash
git clone <your-repo-url> vmq
cd vmq
```

如果代码已经存在，更新时可执行：

```bash
git pull
```

## 3. 配置环境变量

复制示例配置：

```bash
cp .env.example .env
```

至少修改以下字段：

```env
SESSION_SECRET=替换为至少32位随机字符串
ADMIN_USER=后台管理员账号
ADMIN_PASS=后台管理员密码
POSTGRES_PASSWORD=PostgreSQL密码
COOKIE_SECURE=0
APP_PORT=8080
```

生产环境建议：

- `SESSION_SECRET` 使用高强度随机字符串
- `ADMIN_PASS` 使用强密码
- 如果站点前面有 HTTPS 反向代理，将 `COOKIE_SECURE=1`
- 如果商户回调地址是公网地址，保持 `ALLOW_PRIVATE_CALLBACKS=0`
- 只有明确需要回调到内网系统时，才设置 `ALLOW_PRIVATE_CALLBACKS=1`
- 如果应用前面有 Nginx、Traefik、Caddy 等反代，设置 `TRUSTED_PROXY_CIDRS`
- 如果应用直接暴露在 Cloudflare 后面而不是先经过你自己的 Nginx，可设置 `TRUST_CLOUDFLARE_IPS=1`

反代相关环境变量说明：

```env
# 仅在应用前面有你自己可控的反向代理时设置
# 示例：本机 Nginx 反代到容器，可写 127.0.0.1/32
# 示例：Docker bridge 网段，可写 172.18.0.0/16
TRUSTED_PROXY_CIDRS=

# 仅在 Cloudflare 直接回源到应用容器时启用
# 如果是 Cloudflare -> Nginx -> app，通常保持 0，由 Nginx 统一处理真实 IP
TRUST_CLOUDFLARE_IPS=0
```

## 4. 启动服务

首次部署或代码更新后，执行：

```bash
docker compose up -d --build
```

该命令会完成：

- 构建 Go 服务镜像
- 启动 PostgreSQL
- 启动 `vmq` 应用容器
- 自动初始化数据库表
- 首次写入默认系统配置

如果你希望在 Debian 服务器直接拉取 GitHub 自动构建的镜像，而不是本地编译，可执行：

```bash
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
```

默认拉取的镜像为：

```text
ghcr.io/cvinit/vmq-go:latest
```

如需固定版本，可自行指定：

```bash
VMQ_IMAGE=ghcr.io/cvinit/vmq-go:v1.0.0 docker compose -f docker-compose.ghcr.yml up -d
```

如果你更希望直接用脚本，可执行：

```bash
./scripts/deploy-ghcr.sh
./scripts/update-ghcr.sh
```

也支持直接指定版本镜像：

```bash
./scripts/deploy-ghcr.sh ghcr.io/cvinit/vmq-go:v1.0.0
./scripts/update-ghcr.sh ghcr.io/cvinit/vmq-go:v1.0.0
```

## 5. 检查部署状态

查看容器状态：

```bash
docker compose ps
```

查看应用日志：

```bash
docker compose logs -f app
```

查看数据库日志：

```bash
docker compose logs -f postgres
```

正常情况下，应用日志应出现类似输出：

```text
vmq go server listening on :8080
```

## 6. 首次访问

部署成功后可访问：

- 前台首页：`http://服务器IP:8080/`
- 后台页面：`http://服务器IP:8080/index.html`

后台账号密码使用 `.env` 中配置的：

- `ADMIN_USER`
- `ADMIN_PASS`

首次启动会自动初始化：

- 商户通讯密钥 `key`
- 监控端通讯密钥 `deviceKey`
- 默认订单有效期、回调配置等系统设置

## 7. 常用运维命令

停止服务：

```bash
docker compose down
```

重启服务：

```bash
docker compose restart
```

重新构建并启动：

```bash
docker compose up -d --build
```

删除服务并清空数据库卷：

```bash
docker compose down -v
```

注意：`down -v` 会删除 PostgreSQL 持久化数据，只能在你确认不需要旧订单数据时使用。

## 8. 升级步骤

推荐升级流程：

```bash
git pull
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 app
```

当前程序启动时会自动执行建表与兼容性初始化，不需要额外手工迁移。

如果你使用的是 GitHub 自动构建镜像，推荐升级流程改为：

```bash
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
docker compose -f docker-compose.ghcr.yml ps
docker compose -f docker-compose.ghcr.yml logs --tail=100 app
```

## 8.1 GitHub Actions 自动构建

仓库已附带工作流：

- [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)
- [`.github/workflows/docker-publish.yml`](../.github/workflows/docker-publish.yml)
- [`.github/workflows/release.yml`](../.github/workflows/release.yml)

该工作流会在以下场景自动构建并发布容器镜像到 GHCR：

- 推送到 `master`
- 推送 `v*` 标签
- 在 GitHub Actions 页面手动触发

其中：

- `ci.yml` 负责在 `push` / `pull_request` 时先执行测试和 Docker 构建冒烟
- `docker-publish.yml` 负责在验证通过后发布 GHCR 镜像
- `release.yml` 负责在推送 `v*` 标签时自动创建 GitHub Release

默认发布：

- `ghcr.io/cvinit/vmq-go:latest`
- `ghcr.io/cvinit/vmq-go:master`
- `ghcr.io/cvinit/vmq-go:sha-...`
- 标签发布时的 `ghcr.io/cvinit/vmq-go:vX.Y.Z`

构建架构：

- `linux/amd64`
- `linux/arm64`

注意：

- 如果你希望 Debian 服务器可以匿名 `docker pull`，需要把 GHCR 包设置为 `public`
- 如果包仍是私有，需要先在服务器执行 `docker login ghcr.io`

推荐版本发布方式：

```bash
git tag v1.0.0
git push origin v1.0.0
```

这样会同时触发：

- `ghcr.io/cvinit/vmq-go:v1.0.0` 镜像发布
- 对应 GitHub Release 创建

## 9. 反向代理与真实 IP

如果需要公网 HTTPS，建议使用 Nginx 或 Caddy 反代到本地 `8080`。

仓库已附带一份可直接改造的 Nginx 模板：

- [`docs/nginx/vmq.conf.example`](nginx/vmq.conf.example)

反代后请注意：

- 浏览器实际访问为 HTTPS 时，将 `.env` 中 `COOKIE_SECURE=1`
- 反向代理需正确透传请求到 `127.0.0.1:8080`
- 不建议直接将后台暴露在无任何防护的公网环境
- 登录限速依赖真实客户端 IP，因此反代必须正确覆盖 `X-Real-IP` / `X-Forwarded-For`

### 9.1 Nginx 反代示例

如果是 `Nginx -> vmq`：

```nginx
server {
    listen 443 ssl http2;
    server_name your.domain.example;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

如果你想直接套一份更接近生产的模板，可优先参考仓库里的 [`docs/nginx/vmq.conf.example`](nginx/vmq.conf.example)。
其中已经预留了：

- `/login` 限流
- `/aaa.html` 与 `/admin/` 的可选 IP 白名单位置
- `Cloudflare -> Nginx -> vmq` 的真实 IP 恢复注释位

此时建议在 `.env` 中设置：

```env
COOKIE_SECURE=1
TRUSTED_PROXY_CIDRS=127.0.0.1/32
```

如果是 Docker 网络中单独跑 Nginx，请把 `TRUSTED_PROXY_CIDRS` 改成 Nginx 所在网段，例如：

```env
TRUSTED_PROXY_CIDRS=172.18.0.0/16
```

### 9.2 Cloudflare + Nginx

如果是 `Cloudflare -> Nginx -> vmq`，推荐由 Nginx 先恢复真实访客 IP，再回源给应用。应用只信任 Nginx，不直接信任 Cloudflare。

Nginx 关键配置示例：

```nginx
# 按 Cloudflare 官方 IP 列表维护 set_real_ip_from
set_real_ip_from 173.245.48.0/20;
set_real_ip_from 103.21.244.0/22;
set_real_ip_from 103.22.200.0/22;
set_real_ip_from 103.31.4.0/22;
set_real_ip_from 141.101.64.0/18;
set_real_ip_from 108.162.192.0/18;
set_real_ip_from 190.93.240.0/20;
set_real_ip_from 188.114.96.0/20;
set_real_ip_from 197.234.240.0/22;
set_real_ip_from 198.41.128.0/17;
set_real_ip_from 162.158.0.0/15;
set_real_ip_from 104.16.0.0/13;
set_real_ip_from 104.24.0.0/14;
set_real_ip_from 172.64.0.0/13;
set_real_ip_from 131.0.72.0/22;
set_real_ip_from 2400:cb00::/32;
set_real_ip_from 2606:4700::/32;
set_real_ip_from 2803:f800::/32;
set_real_ip_from 2405:b500::/32;
set_real_ip_from 2405:8100::/32;
set_real_ip_from 2a06:98c0::/29;
set_real_ip_from 2c0f:f248::/32;
real_ip_header CF-Connecting-IP;

server {
    listen 443 ssl http2;
    server_name your.domain.example;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

此场景下 `.env` 推荐：

```env
COOKIE_SECURE=1
TRUSTED_PROXY_CIDRS=127.0.0.1/32
TRUST_CLOUDFLARE_IPS=0
```

### 9.3 Cloudflare 直接回源到应用

只有在没有你自己的 Nginx，且 Cloudflare 直接请求应用容器时，才建议启用：

```env
TRUST_CLOUDFLARE_IPS=1
```

这会让应用仅在来源 IP 属于 Cloudflare 官方网段时，接受 `CF-Connecting-IP` 作为真实客户端 IP。

## 10. 故障排查

### 应用容器启动失败

优先检查：

```bash
docker compose logs --tail=200 app
```

常见原因：

- `SESSION_SECRET` 仍是弱默认值
- `ADMIN_USER` / `ADMIN_PASS` 仍是弱默认值
- `DATABASE_URL` 配置错误
- `BASE_WEB_PATH` 不存在

### 数据库未就绪

检查：

```bash
docker compose logs --tail=200 postgres
```

### 页面能打开但支付异常

优先检查后台系统设置：

- `notifyUrl`
- `returnUrl`
- `wxpay`
- `zfbpay`
- `key`
- `deviceKey`

并确认监控端使用的是 `deviceKey`，商户侧签名使用的是 `key`。

### 登录限速始终识别成代理 IP

优先检查：

- `.env` 中 `TRUSTED_PROXY_CIDRS` 是否配置为你自己的反代地址或网段
- Nginx 是否显式覆盖了 `X-Real-IP` 和 `X-Forwarded-For`
- 如果使用 Cloudflare，是否先由 Nginx 用 `CF-Connecting-IP` 恢复真实 IP
