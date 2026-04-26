# VMQ 与 Dujiao-Next 共用 PostgreSQL 容器部署方案

本文档用于部署两个项目：

- 当前仓库 `Vmq`
- `Dujiao-Next`

目标：

- 两个项目都通过 Docker 部署
- 只使用一个 PostgreSQL 容器
- 两个项目数据库彼此隔离，互不影响
- 通过 Nginx 做统一反向代理

说明：

- 本方案基于当前仓库的 [docker-compose.ghcr.yml](../docker-compose.ghcr.yml) 与 [docs/nginx/vmq.conf.example](./nginx/vmq.conf.example) 整理
- Dujiao-Next 部分基于其官方 Docker Compose 部署文档与官方 `config.yml.example` 的 PostgreSQL 思路改造
- 由于 Dujiao-Next 上游版本可能变化，正式部署前建议再与当前版本官方模板核对一次镜像名和配置字段

## 1. 推荐架构

不建议把两个项目硬塞进一个超大的 Compose 文件。

推荐拆成 3 套：

1. `shared-postgres`
   只跑一个 PostgreSQL 容器，负责创建两套独立数据库和独立用户
2. `vmq`
   只跑当前仓库的 `app`，连接 `shared-postgres`
3. `dujiao-next`
   只跑 `redis + api + user + admin`，连接 `shared-postgres`

这样做的优点：

- 两个项目可以独立启停、独立升级
- PostgreSQL 只维护一套
- 通过独立数据库和独立数据库用户隔离权限

## 2. 隔离原则

互不影响的关键不是“同一个 PostgreSQL 容器”，而是：

- 不同数据库
- 不同数据库用户
- 不共享超级用户
- 不让业务应用直接使用 `postgres` 超级账号

示例分配：

- VMQ 使用数据库 `vmq`，用户 `vmq`
- Dujiao-Next 使用数据库 `dujiao_next`，用户 `dujiao`

## 3. 目录规划

建议服务器目录如下：

```text
/opt/shared-postgres
/opt/vmq
/opt/dujiao-next
```

## 4. 共享 PostgreSQL 组件

### 4.1 `/opt/shared-postgres/.env`

```env
TZ=Asia/Shanghai

POSTGRES_SUPERUSER=postgres
POSTGRES_SUPERPASS=replacewith64hexpostgresadminpass

VMQ_DB_NAME=vmq
VMQ_DB_USER=vmq
VMQ_DB_PASSWORD=replacewith48hexvmqdbpass

DUJIAO_DB_NAME=dujiao_next
DUJIAO_DB_USER=dujiao
DUJIAO_DB_PASSWORD=replacewith48hexdujiaodbpass
```

### 4.2 `/opt/shared-postgres/docker-compose.yml`

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: shared-postgres
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      POSTGRES_DB: postgres
      POSTGRES_USER: ${POSTGRES_SUPERUSER}
      POSTGRES_PASSWORD: ${POSTGRES_SUPERPASS}
      VMQ_DB_NAME: ${VMQ_DB_NAME}
      VMQ_DB_USER: ${VMQ_DB_USER}
      VMQ_DB_PASSWORD: ${VMQ_DB_PASSWORD}
      DUJIAO_DB_NAME: ${DUJIAO_DB_NAME}
      DUJIAO_DB_USER: ${DUJIAO_DB_USER}
      DUJIAO_DB_PASSWORD: ${DUJIAO_DB_PASSWORD}
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./initdb:/docker-entrypoint-initdb.d:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_SUPERUSER} -d postgres"]
      interval: 10s
      timeout: 5s
      retries: 10
    networks:
      shared-db-net:
        aliases:
          - shared-postgres

networks:
  shared-db-net:
    name: shared-db-net
    driver: bridge
```

### 4.3 `/opt/shared-postgres/initdb/01-init-multi-db.sh`

```sh
#!/bin/sh
set -eu

psql_admin() {
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres "$@"
}

if ! psql_admin -tAc "SELECT 1 FROM pg_roles WHERE rolname='${VMQ_DB_USER}'" | grep -q 1; then
  psql_admin -c "CREATE ROLE ${VMQ_DB_USER} LOGIN PASSWORD '${VMQ_DB_PASSWORD}';"
fi

if ! psql_admin -tAc "SELECT 1 FROM pg_database WHERE datname='${VMQ_DB_NAME}'" | grep -q 1; then
  psql_admin -c "CREATE DATABASE ${VMQ_DB_NAME} OWNER ${VMQ_DB_USER};"
fi

psql_admin -c "REVOKE ALL ON DATABASE ${VMQ_DB_NAME} FROM PUBLIC;"

if ! psql_admin -tAc "SELECT 1 FROM pg_roles WHERE rolname='${DUJIAO_DB_USER}'" | grep -q 1; then
  psql_admin -c "CREATE ROLE ${DUJIAO_DB_USER} LOGIN PASSWORD '${DUJIAO_DB_PASSWORD}';"
fi

if ! psql_admin -tAc "SELECT 1 FROM pg_database WHERE datname='${DUJIAO_DB_NAME}'" | grep -q 1; then
  psql_admin -c "CREATE DATABASE ${DUJIAO_DB_NAME} OWNER ${DUJIAO_DB_USER};"
fi

psql_admin -c "REVOKE ALL ON DATABASE ${DUJIAO_DB_NAME} FROM PUBLIC;"
```

## 5. VMQ 组件

### 5.1 `/opt/vmq/.env`

```env
VMQ_IMAGE=ghcr.io/cvinit/vmq-go:latest
VMQ_HOST_PORT=18080

SESSION_SECRET=replacewith64hexsessionsecret
ADMIN_USER=replace-admin-user
ADMIN_PASS=replace-admin-password

COOKIE_SECURE=1

VMQ_DB_NAME=vmq
VMQ_DB_USER=vmq
VMQ_DB_PASSWORD=replacewith48hexvmqdbpass

ADMIN_SESSION_TTL_HOURS=720
ALLOW_INSECURE_DEV_DEFAULTS=0
ALLOW_PRIVATE_CALLBACKS=0
TRUSTED_PROXY_CIDRS=127.0.0.1/32
TRUST_CLOUDFLARE_IPS=0
```

### 5.2 `/opt/vmq/docker-compose.shared-pg.yml`

```yaml
services:
  app:
    image: ${VMQ_IMAGE}
    container_name: vmq-app
    pull_policy: always
    restart: unless-stopped
    environment:
      APP_PORT: "8080"
      SESSION_SECRET: ${SESSION_SECRET}
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASS: ${ADMIN_PASS}
      COOKIE_SECURE: ${COOKIE_SECURE}
      DATABASE_URL: postgres://${VMQ_DB_USER}:${VMQ_DB_PASSWORD}@shared-postgres:5432/${VMQ_DB_NAME}?sslmode=disable
      ADMIN_SESSION_TTL_HOURS: ${ADMIN_SESSION_TTL_HOURS}
      ALLOW_INSECURE_DEV_DEFAULTS: ${ALLOW_INSECURE_DEV_DEFAULTS}
      ALLOW_PRIVATE_CALLBACKS: ${ALLOW_PRIVATE_CALLBACKS}
      TRUSTED_PROXY_CIDRS: ${TRUSTED_PROXY_CIDRS}
      TRUST_CLOUDFLARE_IPS: ${TRUST_CLOUDFLARE_IPS}
      BASE_WEB_PATH: /app/src/main/webapp
    ports:
      - "127.0.0.1:${VMQ_HOST_PORT}:8080"
    networks:
      - vmq-net
      - shared-db-net

networks:
  vmq-net:
    driver: bridge
  shared-db-net:
    external: true
    name: shared-db-net
```

## 6. Dujiao-Next 组件

### 6.1 `/opt/dujiao-next/.env`

```env
TAG=latest
TZ=Asia/Shanghai

API_PORT=18081
USER_PORT=18082
ADMIN_PORT=18083
REDIS_HOST_PORT=16379

DJ_DEFAULT_ADMIN_USERNAME=replace-admin-user
DJ_DEFAULT_ADMIN_PASSWORD=replace-admin-password

REDIS_PASSWORD=replacewith48hexredispass
```

### 6.2 `/opt/dujiao-next/docker-compose.shared-pg.yml`

```yaml
services:
  redis:
    image: redis:7-alpine
    container_name: dujiaonext-redis
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes", "--requirepass", "${REDIS_PASSWORD}"]
    ports:
      - "127.0.0.1:${REDIS_HOST_PORT}:6379"
    volumes:
      - ./data/redis:/data
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "${REDIS_PASSWORD}", "ping"]
      interval: 10s
      timeout: 3s
      retries: 10
    networks:
      - dujiao-net

  api:
    image: dujiaonext/api:${TAG}
    container_name: dujiaonext-api
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      DJ_DEFAULT_ADMIN_USERNAME: ${DJ_DEFAULT_ADMIN_USERNAME}
      DJ_DEFAULT_ADMIN_PASSWORD: ${DJ_DEFAULT_ADMIN_PASSWORD}
    ports:
      - "127.0.0.1:${API_PORT}:8080"
    volumes:
      - ./config/config.yml:/app/config.yml:ro
      - ./data/uploads:/app/uploads
      - ./data/logs:/app/logs
    depends_on:
      redis:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/health"]
      interval: 10s
      timeout: 3s
      retries: 10
    networks:
      - dujiao-net
      - shared-db-net

  user:
    image: dujiaonext/user:${TAG}
    container_name: dujiaonext-user
    restart: unless-stopped
    environment:
      TZ: ${TZ}
    ports:
      - "127.0.0.1:${USER_PORT}:80"
    depends_on:
      api:
        condition: service_healthy
    networks:
      - dujiao-net

  admin:
    image: dujiaonext/admin:${TAG}
    container_name: dujiaonext-admin
    restart: unless-stopped
    environment:
      TZ: ${TZ}
    ports:
      - "127.0.0.1:${ADMIN_PORT}:80"
    depends_on:
      api:
        condition: service_healthy
    networks:
      - dujiao-net

networks:
  dujiao-net:
    driver: bridge
  shared-db-net:
    external: true
    name: shared-db-net
```

### 6.3 `/opt/dujiao-next/config/config.yml`

```yaml
app:
  secret_key: replacewith64hexdujiaoappsecret

server:
  host: 0.0.0.0
  port: 8080
  mode: release

log:
  dir: /app/logs
  filename: app.log
  max_size_mb: 100
  max_backups: 7
  max_age_days: 30
  compress: true

database:
  driver: postgres
  dsn: host=shared-postgres user=dujiao password=replacewith48hexdujiaodbpass dbname=dujiao_next port=5432 sslmode=disable TimeZone=Asia/Shanghai
  pool:
    max_open_conns: 20
    max_idle_conns: 10
    conn_max_lifetime_seconds: 3600
    conn_max_idle_time_seconds: 600

jwt:
  secret: replacewith64hexdujiaoadminjwtsecret
  expire_hours: 24

user_jwt:
  secret: replacewith64hexdujiaouserjwtsecret
  expire_hours: 24
  remember_me_expire_hours: 168

bootstrap:
  default_admin_username: ""
  default_admin_password: ""

redis:
  enabled: true
  host: redis
  port: 6379
  password: replacewith48hexredispass
  db: 0
  prefix: "dj"

queue:
  enabled: true
  host: redis
  port: 6379
  password: replacewith48hexredispass
  db: 1
  concurrency: 10
  queues:
    default: 10
    critical: 5
  upstream_sync_interval: "5m"

upload:
  max_size: 10485760
  allowed_types:
    - image/jpeg
    - image/png
    - image/gif
    - image/webp
    - image/svg+xml
  allowed_extensions:
    - .jpg
    - .jpeg
    - .png
    - .gif
    - .webp
    - .svg
  max_width: 4096
  max_height: 4096

cors:
  allowed_origins:
    - https://shop.example.com
    - https://admin.shop.example.com
  allowed_methods:
    - GET
    - POST
    - PUT
    - PATCH
    - DELETE
    - OPTIONS
  allowed_headers:
    - Content-Type
    - Content-Length
    - Accept-Encoding
    - Authorization
    - Cache-Control
    - X-Requested-With
    - X-CSRF-Token
  allow_credentials: true
  max_age: 600

security:
  login_rate_limit:
    window_seconds: 300
    max_attempts: 5
    block_seconds: 900
  password_policy:
    min_length: 8
    require_upper: true
    require_lower: true
    require_number: true
    require_special: false

email:
  enabled: false
  host: ""
  port: 465
  username: ""
  password: ""
  from: ""
  from_name: ""
  use_tls: false
  use_ssl: true

verify_code:
  expire_minutes: 10
  send_interval_seconds: 60
  max_attempts: 5
  length: 6

order:
  payment_expire_minutes: 15
  max_refund_days: 30
```

## 7. Nginx 反向代理配置

示例域名：

- `vmq.example.com`
- `shop.example.com`
- `admin.shop.example.com`

文件建议保存到：

- `/etc/nginx/conf.d/vmq-dujiao.conf`

```nginx
limit_req_zone $binary_remote_addr zone=vmq_login:10m rate=5r/m;

upstream vmq_app {
    server 127.0.0.1:18080;
    keepalive 32;
}

upstream dujiao_api {
    server 127.0.0.1:18081;
    keepalive 32;
}

upstream dujiao_user {
    server 127.0.0.1:18082;
    keepalive 32;
}

upstream dujiao_admin {
    server 127.0.0.1:18083;
    keepalive 32;
}

server {
    listen 80;
    server_name vmq.example.com shop.example.com admin.shop.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name vmq.example.com;

    ssl_certificate /etc/letsencrypt/live/vmq.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/vmq.example.com/privkey.pem;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:10m;
    ssl_protocols TLSv1.2 TLSv1.3;

    client_max_body_size 6m;

    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy no-referrer always;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_read_timeout 30s;
    proxy_connect_timeout 5s;
    proxy_send_timeout 30s;

    location = /login {
        limit_req zone=vmq_login burst=5 nodelay;
        proxy_pass http://vmq_app;
    }

    location = /logout {
        proxy_pass http://vmq_app;
    }

    location = /aaa.html {
        proxy_pass http://vmq_app;
    }

    location ^~ /admin/ {
        proxy_pass http://vmq_app;
    }

    location / {
        proxy_pass http://vmq_app;
    }
}

server {
    listen 443 ssl http2;
    server_name shop.example.com;

    ssl_certificate /etc/letsencrypt/live/shop.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/shop.example.com/privkey.pem;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    location / {
        proxy_pass http://dujiao_user;
    }

    location /api/ {
        proxy_pass http://dujiao_api/api/;
    }

    location /uploads/ {
        proxy_pass http://dujiao_api/uploads/;
    }
}

server {
    listen 443 ssl http2;
    server_name admin.shop.example.com;

    ssl_certificate /etc/letsencrypt/live/admin.shop.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/admin.shop.example.com/privkey.pem;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    location / {
        proxy_pass http://dujiao_admin;
    }

    location /api/ {
        proxy_pass http://dujiao_api/api/;
    }

    location /uploads/ {
        proxy_pass http://dujiao_api/uploads/;
    }
}
```

## 8. 部署步骤

### 8.1 创建目录

```bash
mkdir -p /opt/shared-postgres/initdb /opt/shared-postgres/data
mkdir -p /opt/vmq
mkdir -p /opt/dujiao-next/config /opt/dujiao-next/data/redis /opt/dujiao-next/data/uploads /opt/dujiao-next/data/logs
```

### 8.2 生成随机密钥

建议全部用十六进制随机串，避免引号与特殊字符问题：

```bash
openssl rand -hex 32
openssl rand -hex 24
```

### 8.3 写入文件

按本文档内容分别创建：

- `/opt/shared-postgres/.env`
- `/opt/shared-postgres/docker-compose.yml`
- `/opt/shared-postgres/initdb/01-init-multi-db.sh`
- `/opt/vmq/.env`
- `/opt/vmq/docker-compose.shared-pg.yml`
- `/opt/dujiao-next/.env`
- `/opt/dujiao-next/docker-compose.shared-pg.yml`
- `/opt/dujiao-next/config/config.yml`

然后给初始化脚本执行权限：

```bash
chmod +x /opt/shared-postgres/initdb/01-init-multi-db.sh
```

### 8.4 启动共享 PostgreSQL

```bash
cd /opt/shared-postgres
docker compose --env-file .env config >/dev/null
docker compose --env-file .env up -d
docker compose ps
```

### 8.5 验证数据库和用户已创建

```bash
docker exec -it shared-postgres psql -U postgres -d postgres -c '\l'
docker exec -it shared-postgres psql -U postgres -d postgres -c '\du'
```

预期应出现：

- 数据库 `vmq`
- 数据库 `dujiao_next`
- 用户 `vmq`
- 用户 `dujiao`

### 8.6 启动 VMQ

```bash
cd /opt/vmq
docker compose --env-file .env -f docker-compose.shared-pg.yml config >/dev/null
docker compose --env-file .env -f docker-compose.shared-pg.yml up -d
docker compose -f docker-compose.shared-pg.yml ps
```

### 8.7 启动 Dujiao-Next

```bash
cd /opt/dujiao-next
docker compose --env-file .env -f docker-compose.shared-pg.yml config >/dev/null
docker compose --env-file .env -f docker-compose.shared-pg.yml up -d
docker compose -f docker-compose.shared-pg.yml ps
```

### 8.8 安装并检查 Nginx 配置

```bash
nginx -t
systemctl reload nginx
```

### 8.9 健康检查

```bash
curl -I http://127.0.0.1:18080/
curl -I http://127.0.0.1:18081/health
curl -I http://127.0.0.1:18082/
curl -I http://127.0.0.1:18083/
```

公网访问地址：

- `https://vmq.example.com`
- `https://shop.example.com`
- `https://admin.shop.example.com`

## 9. 关键注意事项

### 9.1 PostgreSQL 初始化脚本只在首次初始化时执行

`/docker-entrypoint-initdb.d` 下的脚本只会在数据目录首次初始化时运行。

如果 PostgreSQL 已经用旧数据卷启动过，再修改 `initdb` 内容不会自动重跑。

### 9.2 修改密码要同步三处

如果后续修改数据库密码，需要同步修改：

- `/opt/shared-postgres/.env`
- `/opt/vmq/.env`
- `/opt/dujiao-next/config/config.yml`

### 9.3 业务端口只监听本机

本方案中所有容器端口都只绑定到 `127.0.0.1`。

建议：

- 外网只开放 `80/443`
- 内部容器端口只给宿主机与 Nginx 使用

### 9.4 VMQ 的反代信任配置

当前仓库对代理链比较敏感。

如果 Nginx 跑在宿主机，VMQ 中推荐：

```env
TRUSTED_PROXY_CIDRS=127.0.0.1/32
COOKIE_SECURE=1
```

如果 Nginx 也运行在 Docker 网络中，应将 `TRUSTED_PROXY_CIDRS` 改为对应 Docker 网段，例如：

```env
TRUSTED_PROXY_CIDRS=172.18.0.0/16
```

### 9.5 Dujiao-Next 的 `/api` 与 `/uploads` 反代不可省略

`user` 和 `admin` 前端通常都需要通过 Nginx 将：

- `/api`
- `/uploads`

转发到 `api` 容器。

这部分如果缺失，前端页面通常能打开，但接口请求和静态上传资源会异常。

### 9.6 生产环境建议固定镜像版本

不要长期使用 `latest`。

建议在正式环境中固定镜像版本标签，便于回滚和复现。

## 10. 参考

- 当前仓库 Compose：`docker-compose.ghcr.yml`
- 当前仓库 Nginx 模板：`docs/nginx/vmq.conf.example`
- 当前仓库部署文档：`docs/DEPLOYMENT.md`
- Dujiao-Next 官方 Docker Compose 文档：<https://dujiao-next.com/deploy/docker-compose>
- Dujiao-Next 官方配置模板：<https://raw.githubusercontent.com/dujiao-next/dujiao-next/main/config.yml.example>
