# 发布约定

本文档描述当前仓库的发布分支策略、版本号约定与 GHCR 镜像标签含义。

## 1. 分支策略

当前仓库不维护长期存在的 `release` 分支。

默认约定如下：

- `master`：默认集成分支，也是当前 GitHub 默认发布入口
- 功能开发：建议在独立分支完成后再合并回 `master`
- 正式版本：通过给 `master` 上的提交打 `vX.Y.Z` 标签完成

也就是说，正式发布以：

- `master`
- `vX.Y.Z`

这两条线为准，而不是长期维护单独的 `release` 分支。

如果未来需要冻结版本做短期修复，可以临时创建类似：

```text
release/v1.2.0
```

但当前自动化流程并不依赖该分支。

## 2. 版本号约定

当前采用语义化版本风格：

```text
vX.Y.Z
```

例如：

- `v1.0.0`：首个稳定版本
- `v1.1.0`：新增功能但兼容已有部署
- `v1.1.1`：仅修复 bug，不改变发布接口预期

推荐：

- 影响部署方式、镜像标签或外部接入行为时，升级次版本或主版本
- 仅修正 CI、文档、小范围缺陷时，升级补丁版本

## 3. GHCR 标签含义

当前工作流会发布以下几类镜像标签：

- `ghcr.io/cvinit/vmq-go:latest`
- `ghcr.io/cvinit/vmq-go:master`
- `ghcr.io/cvinit/vmq-go:sha-...`
- `ghcr.io/cvinit/vmq-go:vX.Y.Z`

建议理解为：

- `latest`：默认分支的最新公开版本，适合测试或快速部署
- `master`：当前默认分支构建结果，适合跟踪主线
- `sha-*`：精确定位某一次提交构建结果
- `vX.Y.Z`：正式发布版本，生产环境优先使用

生产环境建议固定到：

```text
ghcr.io/cvinit/vmq-go:v1.0.0
```

而不是长期追踪 `latest`。

## 4. 标准发布步骤

### 4.1 合并到 master

先确保目标提交已经进入 `master`，并且 GitHub Actions 的 `ci` 与 `docker-publish` 已通过。

### 4.2 打版本标签

示例：

```bash
git checkout master
git pull --ff-only origin master
git tag v1.0.0
git push origin v1.0.0
```

### 4.3 自动完成的动作

推送标签后，GitHub Actions 会自动：

- 构建并发布 `ghcr.io/cvinit/vmq-go:v1.0.0`
- 保留多架构支持：`linux/amd64` 与 `linux/arm64`
- 创建对应 GitHub Release

## 5. Debian 服务器版本升级建议

在 Debian 服务器上，正式版本建议固定 `VMQ_IMAGE`：

```bash
VMQ_IMAGE=ghcr.io/cvinit/vmq-go:v1.0.0 docker compose -f docker-compose.ghcr.yml up -d
```

如果你使用仓库附带脚本，可参考：

- [`scripts/deploy-ghcr.sh`](../scripts/deploy-ghcr.sh)
- [`scripts/update-ghcr.sh`](../scripts/update-ghcr.sh)
