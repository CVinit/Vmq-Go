# Vmq-Go

基于上游 [`szvone/Vmq`](https://github.com/szvone/Vmq) 的非官方 Go 重写版。

当前仓库保留了原项目的主要页面结构、接口路径和使用方式，并将服务端改写为 Go，实现 PostgreSQL 持久化、Docker 部署和一系列支付安全加固。

## 合规与来源

- 本仓库是上游项目 `szvone/Vmq` 的衍生作品和 GitHub fork，不是上游官方仓库。
- 服务端实现已重写为 Go，但仍复用了部分前端页面、静态资源和接口兼容行为。
- 本仓库增加了 [`LICENSE`](LICENSE) 和 [`NOTICE`](NOTICE) 说明来源、许可和主要改动。
- 若仓库内某些第三方静态资源或前端库保留其原有版权/许可声明，应继续保留，不应删除。

## 主要修改与优化

### 架构调整

- 服务端从原实现迁移为 Go
- 数据库存储从 H2 改为 PostgreSQL
- 增加 `cmd/vmq` 作为服务启动入口
- 以 `internal/app` 组织业务逻辑、路由、存储和二维码处理
- 增加 Dockerfile 与 `docker-compose.yml`，简化部署
- 增加 GitHub Actions 自动构建并发布 `linux/amd64` 与 `linux/arm64` 镜像到 GHCR

### 兼容性保留

- 保留主要接口路径，例如 `/login`、`/createOrder`、`/getOrder`、`/checkOrder`、`/appHeart`、`/appPush`、`/getState`、`/admin/*`
- 保留 `src/main/webapp` 中的主要前端页面和后台交互结构
- 保留 `CommonRes` / `PageRes` 等返回格式习惯
- 保留支付页通过 `/payPage/pay.html?orderId=...&token=...` 读取订单的兼容方式

### 已做的安全优化

- 将商户通讯密钥 `key` 与监控端密钥 `deviceKey` 拆分，避免商户密钥直接伪造监控回调
- 创建订单、系统设置和异步通知目标地址增加服务端校验
- 默认拒绝私网/本机回调地址，降低 SSRF 风险
- 后台密码改为哈希存储，旧明文密码在首次登录后自动迁移
- 后台登录增加失败限速
- 后台动态接口收紧为同源 `POST`
- 敏感 JSON、后台页面、支付页、二维码动态响应统一增加 `no-store`
- 增加真实退出接口 `/logout`，服务端清理后台 Cookie
- 适配 `Docker + Nginx + Cloudflare` 场景下的真实客户端 IP 识别

## 仓库结构

- `cmd/vmq`: Go 服务启动入口
- `internal/app`: 路由、业务逻辑、存储、认证、二维码处理
- `src/main/webapp`: 复用的前端页面和静态资源
- `docs/DEPLOYMENT.md`: 详细部署文档
- `docs/nginx/vmq.conf.example`: Nginx 反向代理模板
- `docker-compose.yml`: Docker Compose 部署文件

## 快速部署

推荐直接使用 Docker Compose。

### 1. 准备环境变量

```bash
cp .env.example .env
```

至少修改以下配置：

```env
SESSION_SECRET=替换为至少32位随机字符串
ADMIN_USER=后台管理员账号
ADMIN_PASS=后台管理员密码
POSTGRES_PASSWORD=数据库密码
COOKIE_SECURE=1
```

### 2. 启动服务

```bash
docker compose up -d --build
```

### 3. 查看状态

```bash
docker compose ps
docker compose logs -f app
docker compose logs -f postgres
```

默认访问：

- 前台首页：`http://127.0.0.1:8080/`
- 后台登录页：`http://127.0.0.1:8080/index.html`

更多部署说明见 [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)。

## GitHub 自动构建 Docker 镜像

仓库已包含 GitHub Actions 工作流：

- [`.github/workflows/docker-publish.yml`](.github/workflows/docker-publish.yml)

触发条件：

- 推送到 `master`
- 推送 `v*` 标签
- 手动执行 `workflow_dispatch`

工作流会自动：

- 使用 Buildx 构建多架构镜像
- 发布到 `ghcr.io/cvinit/vmq-go`
- 同时生成 `linux/amd64` 与 `linux/arm64`
- 在发布前先执行测试、YAML 校验和本地 Docker smoke build

### CI 与版本发布

仓库还包含：

- [`.github/workflows/ci.yml`](.github/workflows/ci.yml)
- [`.github/workflows/release.yml`](.github/workflows/release.yml)

作用分别是：

- `ci.yml`：在 `push` / `pull_request` 时先跑测试和构建冒烟
- `release.yml`：在推送 `v*` 标签时自动创建 GitHub Release

推荐发布版本时使用：

```bash
git tag v1.0.0
git push origin v1.0.0
```

推送标签后会自动：

- 生成 `ghcr.io/cvinit/vmq-go:v1.0.0`
- 创建对应 GitHub Release

更完整的分支和版本约定见：

- [`docs/RELEASING.md`](docs/RELEASING.md)

### Debian 服务器直接拉取

如果你不想在服务器本地构建，可直接使用仓库中的 GHCR 版 Compose：

```bash
cp .env.example .env
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
```

默认镜像地址：

```text
ghcr.io/cvinit/vmq-go:latest
```

如果后续需要固定版本，可自行指定：

```bash
VMQ_IMAGE=ghcr.io/cvinit/vmq-go:v1.0.0 docker compose -f docker-compose.ghcr.yml up -d
```

如果你希望在 Debian 服务器上直接使用一键脚本，可执行：

```bash
./scripts/deploy-ghcr.sh
./scripts/update-ghcr.sh
```

如果要固定版本，也可以：

```bash
./scripts/deploy-ghcr.sh ghcr.io/cvinit/vmq-go:v1.0.0
./scripts/update-ghcr.sh ghcr.io/cvinit/vmq-go:v1.0.0
```

## 本地开发

### 环境要求

- Go 1.26+
- PostgreSQL 16+

### 启动步骤

1. 复制环境变量文件

```bash
cp .env.example .env
```

2. 准备 PostgreSQL，并确保 `DATABASE_URL` 可连接

3. 导出环境变量后启动服务

```bash
export $(grep -v '^#' .env | xargs)
go run ./cmd/vmq
```

### 本地测试

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod GOPATH=/tmp/go go test ./...
```

## 构建教程

### 本机构建二进制

```bash
mkdir -p bin
go build -o bin/vmq ./cmd/vmq
```

生成后的可执行文件位于：

```text
bin/vmq
```

### 构建 Docker 镜像

```bash
docker build -t vmq-go:local .
```

Dockerfile 已支持 Buildx 多架构构建，适用于 GitHub Actions 发布 `amd64`/`arm64` 镜像。

## 反向代理建议

- 生产环境建议走 HTTPS，并设置 `COOKIE_SECURE=1`
- 若前面有 Nginx、Traefik、Caddy 等反代，应正确配置 `TRUSTED_PROXY_CIDRS`
- 若为 `Cloudflare -> Nginx -> app`，建议由 Nginx 先恢复真实 IP，再透传给应用
- 可直接参考 [`docs/nginx/vmq.conf.example`](docs/nginx/vmq.conf.example)

## 兼容性边界

- 当前仓库重写的是服务端，不包含安卓监控端 APK 的实现和发布
- 支付成功的根信任仍然建立在监控端上报到账这一架构前提上
- 若生产环境需要回调到私网地址，需显式开启 `ALLOW_PRIVATE_CALLBACKS=1`

## 许可证

本仓库按 MIT 方式发布，详见 [`LICENSE`](LICENSE)。

同时请阅读 [`NOTICE`](NOTICE) 中的上游来源与衍生说明。
