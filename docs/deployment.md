# Docker 与 1Panel / OpenResty 部署

本文及 OpenResty 模板中的 `203.0.113.10` 是文档示例地址，安装时替换为自己的服务器地址。真实部署地址仅放在 GitHub Secret `UNOUR_DEPLOY_HOST` 和服务器本地配置中。

前端与后端保留两个 Git 项目，服务器目录分别是 `/opt/unour/unour` 和 `/opt/unour/unour-web`。后端根目录的 `compose.yaml` 管理两个容器；现有 1Panel OpenResty 负责 HTTPS，再代理到 `127.0.0.1:18080`。

```text
https://csac.chat → OpenResty :443 → web :8080 → api :8088 → SQLite
http://203.0.113.10 → https://csac.chat
```

## 启动与更新

两个仓库放在相邻目录。从后端目录执行：

```sh
cp .env.example .env
# 编辑 .env：填写正式 origin、发布镜像编号和前端目录。
docker compose config --quiet
docker compose build
docker compose up -d --no-build --wait
docker compose ps
```

预先在相同架构构建镜像时，可通过 `docker image save` / `docker image load` 传输镜像，然后执行 `up --no-build`。`UNOUR_RELEASE` 提供共同的默认镜像编号；`UNOUR_API_RELEASE`、`UNOUR_WEB_RELEASE` 非空时分别覆盖对应镜像。自动部署使用 `commit SHA-run number-attempt` 作为独立编号，同一提交重建不会覆盖旧镜像标签。更新前先备份数据库；数据库结构升级后，回退旧程序需要同时考虑数据库兼容性。

仅 web 发布本机端口，api 不发布端口。两个应用容器均以非 root 用户运行，应用镜像只读，SQLite 使用稳定的 `unour_data` Docker 数据卷。容器停止、重启、重新创建都保留该卷。不要使用 `docker compose down -v` 删除正式数据。

`UNO_SECURE_COOKIES=true` 用于正式 HTTPS 站点。IP 和 HTTP 域名入口跳转到正式 HTTPS 域名，避免产生不同站点的账户/房间会话。独立本地容器联调可以显式覆盖为 `false`，并设置相应本地 `UNOUR_ORIGINS`。

Compose 的 `UNO_TRUSTED_PROXIES` 只信任隔离 Docker 网络的私网代理。外层 OpenResty 用 Cloudflare 官方 IP 网段校验 `CF-Connecting-IP` 并重写 `X-Real-IP`；web 只在本机监听，将该地址传给 api。因此登录限额按客户端计算。普通直接启动的 Go 服务默认不信任代理头。若更改网络拓扑，必须同时检查可信代理范围和最外层地址头覆盖规则。

## GitHub Actions 自动部署

后端仓库为 `chemsource/UNOur`，前端为 `VasilyZa/UNOur-web`。两仓 `.github/workflows/ci.yml` 在 main 推送和 Pull Request 时运行检查；只有 main 推送或在 main 手动运行 workflow 才会部署。

- 后端执行 Go 测试、SQLite 集成测试、race、vet、接收程序测试、编译和 Docker 构建。
- 前端检出后端 main，实际启动 Go + 临时 SQLite，执行类型检查、测试、格式检查、生产构建和 Docker 构建。
- 检查失败不发布。两仓独立更新各自镜像，共用服务器发布锁，避免互相覆盖；成功发布的 workflow run number 阻止旧任务覆盖新版本。

每仓配置 `UNOUR_DEPLOY_HOST`、`UNOUR_DEPLOY_KEY`、`UNOUR_KNOWN_HOSTS` 三个 Actions Secrets，分别保存服务器地址、组件专用 Ed25519 私钥和经过已验证 SSH 连接取得的服务器主机公钥。工作流不写服务器 IP。连接以专用 `unour-deploy` 用户强制校验主机密钥，不使用服务器密码。公钥使用 `restrict,command="sudo -n /usr/local/sbin/unour-ci-receive api"` 或 `web`，不能获得交互 shell、端口转发或发布另一个组件。

接收程序验证命令、上传大小及 SHA256，再载入镜像、备份 SQLite、原子更新 `.env` 对应版本，等待容器和应用健康。失败时恢复之前的镜像并返回错误。旧镜像和备份保留；数据库不自动恢复旧备份，数据库升级应向前、向后兼容。构建和测试均运行在 GitHub 托管 runner 上。

服务器固定配置 `/etc/unour/deploy.json`：

```json
{
  "repo_dir": "/opt/unour/unour",
  "releases_dir": "/opt/unour/releases",
  "backup_dir": "/opt/unour/backups",
  "project": "unour",
  "health_url": "http://127.0.0.1:18080/healthz",
  "lock_file": "/run/lock/unour-deploy.lock"
}
```

接收程序从 `deploy/ci_receive.py` 安装到 `/usr/local/sbin/unour-ci-receive`，root 持有、权限 0755；配置为 root 持有、权限 0600。首次启用前需先启动基线容器。公钥放在 root 持有的 `/etc/ssh/authorized_keys/unour-deploy` 中；sudoers 仅允许上述两个完整命令，并保留 `SSH_ORIGINAL_COMMAND` 供接收程序校验。SSH 只为此账号启用密钥认证、禁用密码/PTY/转发；账号不加入 docker 组。私钥仅通过 `gh secret set` 的标准输入写入对应仓库，不提交到 Git。

专用账号使用 `/bin/sh` 作为 shell，主目录 `/var/lib/unour-deploy` 由 root 持有且不可由账号写入。SSH 和 sudoers 示例分别为 `deploy/unour-ci.sshd.example`、`deploy/unour-ci.sudoers.example`。SSH 示例安装为 `/etc/ssh/unour-ci.conf`，从主配置**末尾** include，避免 Match 块影响后续全局选项；修改后执行 `sshd -t` 和 `visudo -cf /etc/sudoers.d/unour-deploy`，均通过后才平滑重载 SSH。

自动发布只传输应用镜像，源代码以 GitHub 为准。更改 Compose、接收程序或服务器配置时，管理员须先同步并验证这些基础设施文件。后端接口有不兼容变化时，应先安排兼容过渡，再独立发布前端。

两仓 Actions 日志显示构建和发布结果；服务器 `/opt/unour/releases/.deploy-state-api.json`、`.deploy-state-web.json` 记录成功版本与上一镜像，每次成功另有 `ci-*.json` 记录。保留当前和上一镜像，定期按需清理更早的 UNOur 镜像与备份。

人工回退时，先暂停 workflow 并获取全局发布锁，备份数据库，将 `.env` 对应的 `UNOUR_API_RELEASE` 或 `UNOUR_WEB_RELEASE` 改成旧标签，再执行 `docker compose up -d --no-build --no-deps --wait api` 或 `web` 并检查 `/healthz`。不要只改共同的 `UNOUR_RELEASE`，它会被组件专用版本覆盖。完成后恢复 workflow；后续新推送会继续部署。

## OpenResty 与证书

`deploy/openresty.conf.example` 对应新增的 `/opt/1panel/www/conf.d/unour.conf`。配置引用的文件均位于已有 `/opt/1panel/www` 挂载中；不替换 1Panel 的主配置或默认证书。

### 首次签发

以下命令以 root 在后端目录执行。先将域名解析到本机并开放 80、443，启动前后端容器。尚无证书时，先安装只有 HTTP 验证入口的配置，不能直接引用不存在的 HTTPS 证书：

```sh
cd /opt/unour/unour
install -d -m 0755 /opt/1panel/www/sites/unour/acme/.well-known/acme-challenge
install -d -m 0700 /opt/1panel/www/sites/unour/letsencrypt
cp deploy/openresty-http.conf.example /opt/1panel/www/conf.d/unour.conf
OPENRESTY_CONTAINER=$(docker ps -q --filter label=com.docker.compose.service=openresty)
# 确认只匹配到一个实例；多实例时填写本站点对应的容器名称。
docker exec "$OPENRESTY_CONTAINER" nginx -t
docker exec "$OPENRESTY_CONTAINER" nginx -s reload
printf 'unour-acme-check\n' > /opt/1panel/www/sites/unour/acme/.well-known/acme-challenge/unour-probe
curl --fail http://csac.chat/.well-known/acme-challenge/unour-probe
rm /opt/1panel/www/sites/unour/acme/.well-known/acme-challenge/unour-probe
```

确认公网返回探针内容后再签发；启用 Cloudflare 代理时，验证路径也必须可公开访问。Certbot 需要可写的 webroot 挂载：

```sh
docker run --rm --name unour-certbot \
  --volume /opt/1panel/www/sites/unour/acme:/var/www/acme \
  --volume /opt/1panel/www/sites/unour/letsencrypt:/etc/letsencrypt \
  certbot/certbot:v5.8.0 certonly --non-interactive \
  --agree-tos --register-unsafely-without-email \
  --webroot --webroot-path /var/www/acme --cert-name csac.chat -d csac.chat
python3 deploy/update-cloudflare-ips.py
cp deploy/openresty.conf.example /opt/1panel/www/conf.d/unour.conf
docker exec "$OPENRESTY_CONTAINER" nginx -t
docker exec "$OPENRESTY_CONTAINER" nginx -s reload
curl --fail --resolve csac.chat:443:127.0.0.1 https://csac.chat/healthz
curl --fail https://csac.chat/healthz
install -m 0644 deploy/unour-certificate.service deploy/unour-certificate.timer /etc/systemd/system/
systemctl daemon-reload
sh deploy/renew-certificate.sh --dry-run
systemctl enable --now unour-certificate.timer
```

逐条检查命令结果，签发失败时保持 HTTP 配置并排查验证路径。此安装使用无邮箱 ACME 账户；也可在首次签发时将该选项替换为 `--email` 并填写运维邮箱。

`update-cloudflare-ips.py` 从 Cloudflare 官方公开 API 读取 IPv4、IPv6 网段，校验 CIDR 后原子写入 `cloudflare-realip.conf`，仅信任这些来源提供的 `CF-Connecting-IP`。原始 IP 请求不信任该头。定期更新网段或调整代理时，重新运行该脚本，并在 `nginx -t` 成功后重载 OpenResty。

### 续期与目录

Let’s Encrypt 使用 HTTP-01 webroot，持久化目录为：

- 验证文件：`/opt/1panel/www/sites/unour/acme`
- ACME 账户、证书和私钥：`/opt/1panel/www/sites/unour/letsencrypt`
- 有效证书：`letsencrypt/live/csac.chat/fullchain.pem`
- 私钥：`letsencrypt/live/csac.chat/privkey.pem`

证书由 Certbot 和 `unour-certificate.timer` 管理，不依赖面板证书列表。定时器每天检查两次，更新后校验 OpenResty 配置并平滑重载。脚本只在重载成功后记录已加载证书的指纹；重载失败会返回错误，下次运行会继续尝试，证书没有变化且已经加载时不重载。站点应持续保留 HTTP 验证路径。

```sh
systemctl list-timers unour-certificate.timer
systemctl status unour-certificate.service
journalctl -u unour-certificate.service --since '7 days ago'
sh deploy/renew-certificate.sh --dry-run
```

服务和 timer 文件来自 `deploy/`，安装到 `/etc/systemd/system/` 后通过 `systemctl enable --now unour-certificate.timer` 启用。运行中的 OpenResty 默认按 Compose 的 `openresty` 服务标签定位；多实例环境可给 service 设置 `OPENRESTY_CONTAINER`。

## 数据备份

```sh
# 以下备份命令以 root 执行。
cd /opt/unour/unour
sh deploy/backup.sh /opt/unour/backups
```

该命令通过 SQLite 在线备份接口读取一致快照，包含已提交的 WAL，不需要停止对局。一次性容器读取同一数据卷，不启动第二个游戏进程。脚本用 root 创建 UID/GID 10001 的备份目录；若目录已存在，其所有者必须是 UID 10001，并具有目录读、写、执行权限，供非 root 容器写入。备份目录默认权限 0750、文件权限 0600。将备份另外复制到服务器之外才能防范整机或磁盘故障。

恢复时先停止 api，保留原卷和最新备份，再将选定备份恢复到数据目录并正确处理原 WAL/SHM；不要在游戏运行中直接覆盖数据库。首次上线使用空数据卷，不复制本地测试数据库。

## 1Panel 与日常检查

容器 `unour-api-1`、`unour-web-1` 和卷 `unour_data` 可在 1Panel 容器管理中查看。此部署通过服务器上的 Compose 文件和独立 OpenResty vhost 管理，不直接写入面板内部数据库；面板的网站与证书列表可能不列出这份手动配置。站点改动以配置文件为准。

```sh
cd /opt/unour/unour
docker compose ps
docker compose logs --tail=100 api web
curl --fail http://127.0.0.1:18080/healthz
docker exec "$(docker ps -q --filter label=com.docker.compose.service=openresty)" nginx -t
```

日志使用 Docker local 驱动并限制滚动大小。镜像、数据库卷、证书及源文件目录应分别保留；发布材料不包含 Git 凭证、SSH 密码或本地测试数据。
