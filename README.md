# unour

可组合规则的 UNO Go 后端，使用 SQLite 持久化，最低 Go 1.22.5。游戏逻辑、房间用例、存储和 HTTP 分离；没有全局房间表或全局路由状态。

## 运行

构建需要 `CGO_ENABLED=1` 和 C 编译器（例如 gcc），SQLite 已随 [go-sqlite3 驱动](https://github.com/mattn/go-sqlite3) 提供，无需安装数据库服务。

```sh
go run -buildvcs=false ./cmd/server
```

默认监听 `:8088`。前端在其他 origin 时显式配置允许来源：

```sh
UNO_ADDR=127.0.0.1:8088 \
UNO_ALLOWED_ORIGINS=http://localhost:5173 \
go run -buildvcs=false ./cmd/server
```

| 环境变量 | 默认值 | 含义 |
| --- | --- | --- |
| `UNO_ADDR` | `:8088` | HTTP 监听地址 |
| `UNO_DB_PATH` | `data/unour.db` | SQLite 文件路径，相对路径基于工作目录 |
| `UNO_MAX_ROOMS` | `1000` | 数据库房间数量上限；已有房间不会因降低上限而被删除 |
| `UNO_ROOM_TTL` | `0` | 默认关闭自动删除；正值表示距最后成功写操作的保留时间，读取不续期 |
| `UNO_ALLOWED_ORIGINS` | 空 | 逗号分隔的完整 origin，例如 `https://uno.example.com`，无结尾 `/` |

`GET /healthz` 返回 `{"status":"ok"}`。服务有请求超时、请求体上限、结构化日志、SIGINT/SIGTERM 优雅退出和空闲房间回收。

构建/运行显式关闭 VCS stamping，因此从源码目录或压缩包运行时不依赖父目录的 Git 状态。

默认数据库为 **`data/unour.db`**，首次启动自动建表。房间、规则、原会话令牌摘要和完整对局都持久化；重启后，前端使用原 token 读取状态即可继续游戏。UNO 窗口保留原期限和去重记录，停机时间计入期限，已经处理的处罚不会再次执行。

部署时保持相同工作目录，或用绝对路径固定数据库位置：

```sh
UNO_DB_PATH=/var/lib/unour/unour.db make run
```

数据库采用 WAL 与 FULL 同步提交，操作提交成功后才返回成功。查询状态走独立只读连接池，不重写整局快照。文件包含所有人的手牌等敏感状态，默认目录/文件权限分别为 0700/0600，已加入忽略列表。

默认不自动清理房间；显式设置 `UNO_ROOM_TTL=24h` 后，超过该时间未成功写入的房间会被删除，停机时间同样计入。需要长期保留时维持默认 `0`。备份时先停止服务，再复制整个数据目录。容器部署应将该目录挂载到持久卷。

当前部署方式为一个服务进程和本地 SQLite 文件。扩展及数据库事务边界见 [架构与扩展](docs/architecture.md)。

## API

所有响应使用 JSON，删除成功返回空的 `204`。创建、加入和健康检查以外的操作需要：

```http
Authorization: Bearer <加入房间时返回的 token>
```

玩家 ID 由后端生成，身份从 token 解析。首次加入者为房主。token 只在加入响应返回一次，请由前端保存；服务端仅保存 SHA-256 摘要。

| 方法 | 路径 | 请求体 | 作用 |
| --- | --- | --- | --- |
| POST | `/api/v1/rooms` | `{"id":"table"}` | 创建房间；省略 id 则随机生成 |
| POST | `/api/v1/rooms/{id}/players` | `{"name":"Alice"}` | 加入，返回玩家 ID、token、房间状态 |
| GET | `/api/v1/rooms/{id}` | 无 | 获取该会话的私有视图 |
| DELETE | `/api/v1/rooms/{id}` | 无 | 房主删除房间 |
| POST | `/api/v1/rooms/{id}/start` | 可省略 | 房主开局，至少两人 |
| POST | `/api/v1/rooms/{id}/play` | `{"card_id":17,"color":"red"}` | 出牌；color 仅用于万能牌 |
| POST | `/api/v1/rooms/{id}/draw` | 可省略 | 无罚牌时摸一张；有罚牌时承受整个罚牌池 |
| POST | `/api/v1/rooms/{id}/pass` | 可省略 | 摸牌后放弃打出刚摸到的牌，结束回合 |
| POST | `/api/v1/rooms/{id}/challenge` | 可省略 | 质疑最近可质疑的 +4；目标由后端确定 |
| POST | `/api/v1/rooms/{id}/uno` | `{"window_id":9}` | 在期限前确认喊 UNO |
| POST | `/api/v1/rooms/{id}/uno-penalty` | `{"window_id":9}` | 两秒到期后，本人漏喊 UNO 罚摸一张 |

POST 的非空请求体需要 `Content-Type: application/json`。创建与加入成功返回 `201`，动作成功返回 `200`。未知字段、非对象、多个 JSON 值和超过 16 KiB 的请求体会被拒绝。旧版 `/join` 等接口已移除。

### 示例

```sh
curl -s http://localhost:8088/api/v1/rooms \
  -H 'Content-Type: application/json' -d '{"id":"table"}'

curl -s http://localhost:8088/api/v1/rooms/table/players \
  -H 'Content-Type: application/json' -d '{"name":"Alice"}'

curl -s http://localhost:8088/api/v1/rooms/table/players \
  -H 'Content-Type: application/json' -d '{"name":"Bob"}'

# 将返回的 Alice token 放入这个变量。
alice_token='加入响应中的 token'

curl -s -X POST http://localhost:8088/api/v1/rooms/table/start \
  -H "Authorization: Bearer $alice_token"

curl -s http://localhost:8088/api/v1/rooms/table \
  -H "Authorization: Bearer $alice_token"
```

加入返回 `{player_id, token, room}`；动作返回 `{room, events}`；读取返回房间本身。`room` 结构为：

```json
{
  "id": "table",
  "host_id": "后端生成的玩家 ID",
  "created_at": "2026-10-25T12:00:00Z",
  "rules": [{"name":"matching"},{"name":"actions"},{"name":"draw"},{"name":"challenge"}],
  "state": {
    "phase": "playing",
    "version": 3,
    "you": "后端生成的玩家 ID",
    "players": [],
    "turn": "当前行动玩家 ID",
    "direction": 1,
    "active_color": "red",
    "draw_pile_count": 93,
    "discard_count": 1,
    "pending_draw": 0,
    "can_pass": false
  }
}
```

`players` 每项包含 `id/name/hand_count/eliminated`，只有本人包含 `hand`。卡牌是 `{id,color,kind,number?}`。数字 0 的 `number` 字段可省略，按 0 处理。颜色为 `red/blue/green/yellow/wild`，类型为 `number/skip/reverse/draw_two/wild/wild_draw_four`。有弃牌时返回 `top_card`，万能牌的物理颜色始终是 `wild`，实际匹配颜色看 `active_color`。

状态阶段为 `lobby/playing/finished`。结束后 `winner` 返回胜者 ID；所有人淘汰时无胜者。成功胜利是正常响应，不以错误表示。最后一张是罚牌时，先等待罚牌或质疑结算。

### 并发与重试

所有动作请求可以附带 `expected_version`，例如 `{"card_id":17,"expected_version":12}`。过期版本返回 `409 version_conflict`，前端重新读取状态再决定操作。不同玩家的操作也会增加版本。

普通出牌和摸牌不提供请求 ID 去重；网络结果不确定时，应先读取状态，或者携带原版本重试，避免再次执行。**UNO 罚摸与声明用 window_id 独立去重**；同一操作重复上报不会再次改状态。UNO 操作建议只传 window_id，不传整房间 expected_version，避免被其他玩家正常行动干扰。

## 前端两秒 UNO 判定

玩家打出一张牌后，如果剩一张，本人对应的 `players[i].uno` 为：

```json
{"id":9,"deadline":"2026-10-25T12:00:02Z"}
```

后端没有自动罚牌计时器。前端负责监听空格/中央点击，并上报结果；后端检查窗口、身份和期限：

1. 收到新窗口，保存 `window_id`，开始两秒交互。
2. 玩家按空格或点击中央，立即 POST `/uno`，传该 window_id。
3. 未响应时 POST `/uno-penalty`，传同一 window_id。服务端确认期限已过后只摸一张，**不推进回合，也不清空其他玩家正在承担的罚牌池**。
4. 网络重试可使用相同 window_id；已声明、被取消或过时的窗口不能再次处罚。玩家后续手牌变化会取消旧窗口。

以前端收到响应后再计时两秒，可以避免本地时钟偏差导致提前处罚。若提前请求收到 `uno_too_early`，等期限后重试。声明以服务器收到时为准；前端对网络延迟需要提供清晰反馈。前端漏报不会由服务器自动补罚。

## 规则配置

省略 `rules` 时使用默认四条规则。显式传 `rules` 会**替换整套规则列表**，请保留需要的基础规则。禁止空列表、重复 ID、未知规则和无效选项。

```json
{
  "id": "house-rules",
  "rules": [
    {"name":"matching","options":{"same_color":true,"same_value":true}},
    {"name":"actions","options":{"skip_draw":true,"keep_color":true}},
    {"name":"draw","options":{"play_drawn_card":true}},
    {"name":"challenge","options":{"failure_multiplier":200}},
    {"name":"stacking","options":{"mixed":true,"different_colors":true,"reverse":true}},
    {"name":"hand_limit","options":{"limit":25}}
  ]
}
```

| 规则 | 默认选项 | 行为 |
| --- | --- | --- |
| matching | same_color=true，same_value=true | 按当前颜色、数字或动作类型匹配；万能牌可选色 |
| actions | skip_draw=false，keep_color=false | Skip、Reverse、+2、+4；skip_draw 使被跳过者额外摸一张；keep_color 允许万能牌传 color=`keep` |
| draw | play_drawn_card=true | 普通摸一张后可出刚摸到的牌或 pass；设 false 则摸完直接结束回合 |
| challenge | failure_multiplier=150 | 允许 +4 质疑；150/200 分别为失败承担 1.5/2 倍罚牌池 |
| stacking（可选） | mixed=false，different_colors=false，reverse=false | 同类叠加；mixed 允许 +2/+4 混合；different_colors 允许异色 +2；reverse 允许匹配的 Reverse 反转罚牌方向 |
| hand_limit（可选） | limit=25 | 手牌达到上限淘汰；上限必须大于开局手牌数 |

质疑成功时，最近 +4 出牌者承担当前罚牌池，质疑者保留回合；失败时质疑者承担倍率罚牌并结束回合。证据取自 +4 出牌前的手牌与当时当前颜色，不能自选质疑目标。没有 challenge 规则时，持有当前颜色的玩家不能打 +4。

标准 108 张牌；弃牌回收保留顶部，所有 ID 不变。普通摸牌没有可用牌时结束回合；罚牌池不足时只摸现有数量并结算。UNO 专用罚摸要求实际存在一张，否则拒绝且可以稍后重试。

## 新增规则

实现 `game.Rule` 的 `ID()` 与 `Evaluate(game.Context,*game.Effects)`，在组合根自己的 `game.Registry` 注册工厂，将它注入 `room.Config.Registry`。客户端创建房间时按名称使用该规则。

[可运行的 no_zero 示例](examples/rules/no_zero.go) 与集成测试证明：添加一个规则 veto 不需要修改游戏命令、HTTP 路由或存储。支持范围和新增效果原语的方法见 [架构文档](docs/architecture.md)。

## 验证

```sh
make check  # 全量测试、go vet、竞态检测
make build
make bench
make fuzz
```

测试包括完整对局、牌数守恒、非法命令原子性、规则组合、质疑证据、UNO 窗口、并发容量、独立房间并发、TTL、鉴权与 HTTP 全链路，以及 SQLite 重启恢复、会话恢复、提交失败回滚和并发提交冲突。

结构化错误为 `{"error":{"code":"...","message":"..."}}`。常见状态码：400 无效输入、401 会话无效、403 无权限、404 房间不存在、409 状态/回合/版本/UNO 窗口冲突、413 请求体过大、415 内容类型错误、422 规则拒绝、503 房间容量已满。
