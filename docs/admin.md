# 管理后台

仅通过手动访问站点的 `/admin` 进入，游戏导航不提供入口。入口隐藏不承担权限校验，全部管理 API 均验证独立管理员会话。未设置 `UNO_ADMIN_CONFIG` 时，管理 API 返回 404。

## 密码配置

管理员密码独立于玩家账户，使用 16–128 个 Unicode 字符，建议使用密码管理器生成。玩家密码仍为 8–14 个 Unicode 字符，保留空白。配置工具从标准输入读取密码，文件只保存 PBKDF2-HMAC-SHA256（600000 次、随机 32 字节盐）的结果，无默认管理密码。不要把明文放入命令参数、环境变量、Git、CI 或日志。

在 Bash 中创建配置，`read -s` 避免回显，`printf` 不增加换行：

```bash
mkdir -p .local
chmod 700 .local
read -rsp '管理密码（16–128 字符）: ' unour_admin_password
printf '\n'
printf '%s' "$unour_admin_password" | go run ./cmd/admin-config -out .local/admin.json
unset unour_admin_password
UNO_ADMIN_CONFIG="$PWD/.local/admin.json" make run
```

工具以 0600 创建文件并拒绝覆盖。服务只接受属于运行用户或 root 的普通文件，权限须为 0600 或 0400；拒绝符号链接、弱哈希和未知 JSON 字段。配置目录应为 0700，文件不应位于 Web 静态目录。修改密码时生成新文件，停服后替换配置，再启动服务；进程重启使全部管理员会话失效。

## Docker 启用

镜像内包含 `unour-admin-config`。在后端 Compose 目录的 Bash 中执行，文件由容器用户（UID 10001）写入持久卷：

```bash
read -rsp '管理密码（16–128 字符）: ' unour_admin_password
printf '\n'
printf '%s' "$unour_admin_password" | docker compose run --rm -T --no-deps --entrypoint unour-admin-config api -out /data/admin.json
unset unour_admin_password
```

成功后在服务器本地 `.env` 增加 `UNO_ADMIN_CONFIG=/data/admin.json`，执行 `docker compose up -d --no-build --no-deps --wait api` 重建 API 容器。正式环境维持 `UNO_SECURE_COOKIES=true`，通过 HTTPS 使用后台。不要将配置交给前端容器。配置位于数据卷中，容器重建后保留；数据库备份不含配置时应另行安全备份，或重新生成密码。

CI 只发布镜像，不生成、上传或更改管理密码，也不自动启用管理后台。服务器上先同步包含 `UNO_ADMIN_CONFIG` 的 Compose，再按上述步骤启用。

## 功能与一致性

- 用户：搜索、封禁/解封、撤销所有设备会话、重置密码；保留账户和用户名，不提供永久删除。封禁和密码重置事务递增凭证版本，拒绝同时进行的旧登录请求；解封不复活旧会话。
- 房间：分页列表，每秒刷新当前牌局，查看所有手牌和可用牌堆；普通玩家和观战接口继续隐藏他人手牌。
- 编辑：替换时与牌堆或其他活跃玩家交换现有物理牌；移牌可发给另一玩家或放回牌堆底部；加牌从牌堆移入。总牌数保持 108，弃牌历史和顶牌不可修改，淘汰玩家不可编辑，仅进行中的牌局可编辑。最后一张手牌和当前回合刚摸到的牌只能替换。
- 并发：请求同时带游戏版本和房间修订号，状态改变时返回 409；界面刷新后可重新提交，不自动重试写操作。改牌递增游戏版本并拒绝旧 bot 决策，重置电脑软推断；数量变化取消旧 UNO 窗口，历史质疑证据保留。
- 审计：用户变更及改牌和审计记录在同一个 SQLite 事务中提交，审计失败则回滚。记录不包含密码、盐、密码哈希、Cookie 或令牌。

管理会话最长 1 小时，使用 HttpOnly、SameSite=Strict Cookie；写请求还需专用 CSRF 请求头。每个来源地址 5 分钟最多登录 5 次，密码计算并发受限；按可信代理配置取得来源地址。退出立即撤销当前会话，前端不把管理凭证保存在 localStorage。

## API

前缀 `/api/v1/admin`：`POST /login`、`GET /session`、`POST /logout`；`GET /users`、`PUT /users/{id}/status`、`PUT /users/{id}/password`、`POST /users/{id}/revoke`；`GET /rooms`、`GET /rooms/{id}`、`POST /rooms/{id}/cards`；`GET /audit`。列表支持 `limit`（1–100，默认 20）、`offset`，用户列表另支持 `q`。

换牌 JSON 示例（版本以当前详情为准）：

```json
{"operation":"swap","card_id":12,"replacement_id":30,"expected_version":8,"expected_revision":10}
```

移牌使用 `operation=move` 和 `to_player_id`，目的玩家为空表示牌堆底部。写操作需 `X-Admin-CSRF`，从登录或 `/session` 响应获取，仅保存在内存。
