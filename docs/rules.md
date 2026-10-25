# 配置化规则模块

规则文件位于 `rules/*.json`。服务端读取、校验并编译它们；前端通过 `GET /api/v1/rules` 获取控件元数据。增加当前效果协议内的规则，只需添加 JSON 文件并重载后端，不需要修改 Go 或前端源代码。

## 增加一个规则

项目提供了可运行的 [禁止出零示例](../examples/rules/no_zero.json)：

```sh
cp examples/rules/no_zero.json rules/no_zero.json
# 直接运行的服务：向实际服务进程发送 SIGHUP
kill -HUP <uno-server进程号>
```

刷新大厅即可看到「禁止出零」。它默认关闭，房主开启后才应用到新房间。前端同时支持 boolean、integer、select 选项，不需要预先知道模块名或选项键。规则说明来自房间保存的配置，手牌高亮、颜色、摸牌、跳过和质疑的可用性来自服务端合法动作。

## 模块结构

每个文件恰好包含一个 JSON 对象。模块名和选项键使用小写字母开头的小写字母、数字、下划线，最长 64 字符；所有 JSON 字段名区分大小写，不允许重复或未知字段。

| 字段 | 含义 |
| --- | --- |
| `name` | 唯一模块 ID，不依赖文件名 |
| `version` | 正整数版本；修改玩法时应递增 |
| `order` | 执行/展示顺序，小值优先；相同值按 name 排序 |
| `title`、`description`、`group` | 前端展示的标题、说明、分组提示 |
| `required` | 必选模块，不能被房主关闭；必须同时默认启用 |
| `default_enabled` | 新建房间默认启用 |
| `options` | 通用控件元数据，省略时为空 |
| `constraints` | 通用选项约束，供前后端共同校验 |
| `checks` | 更复杂的选项校验，只在服务端执行 |
| `steps` | 按顺序执行的条件、效果或拒绝 |
| `summary` | 保存到房间的规则说明，可按选项条件显示 |

一个给数字 0 增加下家摸牌效果的模块：

```json
{
  "name": "zero_draw",
  "version": 1,
  "order": 100,
  "title": "零牌罚摸",
  "description": "数字 0 可以让下家摸牌。",
  "group": "扩展规则",
  "default_enabled": false,
  "options": [
    {"key":"amount","type":"integer","label":"下家摸牌数","default":2,"min":1,"max":5}
  ],
  "steps": [
    {"when":"action == \"play\" && card.kind == \"number\" && card.number == 0","set":{"draw_next":"options.amount"}}
  ],
  "summary": [{"text":"打出数字 0 时，下家立即摸 {{amount}} 张牌。"}]
}
```

`draw_next` 表示立即让下家摸牌；`add_penalty` 表示增加待结算罚牌池，两者含义不同。这个例子不改变匹配，也不自动跳过下家。如果需要跳过，可在同一个 set 中增加 `"turn_steps":"2"`。

## 选项与约束

- `boolean`：default 必须为布尔值，渲染开关。
- `integer`：提供 min/max/default，范围为 0–1,000,000 内的整数。
- `select`：提供 `choices:[{"value":150,"label":"1.5 倍"},...]`，值可以是布尔、整数或字符串，所有选择类型必须一致。default 必须属于 choices。
- `depends_on:[{"key":"parent","equals":true}]`：同模块内的选项依赖；循环依赖会被拒绝。依赖不满足时，请求只能使用该选项默认值。前端保留用户偏好，提交时恢复未生效项的默认值。
- `constraints:[{"kind":"at_least_one","fields":["same_color","same_value"],"message":"至少开启一种匹配。"}]`：至少一个指定布尔选项为真。
- `checks:[{"expression":"options.amount < 5","message":"摸牌数必须小于 5。"}]`：表达式只允许读取 options，结果必须为布尔值。服务端检查更复杂组合；默认配置也必须通过检查。

未提供的选项使用文件默认值，未知选项、null、类型错误、越界数值和不满足的约束均被拒绝。字符串值最长 128 字符；整数选项（包括 select 中的整数）限制在 ±1,000,000 内，避免浏览器精度差异。

## 表达式

采用受限 Go 风格表达式，编译时完成字段和类型检查；布尔运算短路，常量会折叠。不提供任意函数、循环、反射、脚本执行、时间、随机、文件和网络访问。

允许 `true/false`、十进制整数、字符串、括号，及以下运算：

- 逻辑：`!`、`&&`、`||`。
- 比较：`==`、`!=`；整数还支持 `<`、`<=`、`>`、`>=`。
- 整数：`+`、`-`、`*`、`/`、`%`、一元负号。除零和 int64 溢出拒绝整个动作。

| 字段 | 类型与含义 |
| --- | --- |
| `options.<key>` | 该选项的布尔/整数/字符串值 |
| `action` | start/play/draw/pass/challenge/uno/uno_penalty |
| `phase` | lobby/playing/finished |
| `chosen_color` | 请求选择的颜色；keep 已转换成当前实际颜色 |
| `active_color` | red/yellow/green/blue，开局前可能为空 |
| `pending_draw`、`pending_kind` | 罚牌池数量与类型；无罚牌时类型可能为空 |
| `alive_players`、`hand_size` | 未淘汰人数、当前动作发起者手牌数 |
| `has_active_color` | 动作发起者是否持有当前颜色的普通牌 |
| `drawn_card_id` | 摸后选择阶段的卡牌 ID，其他阶段为 0 |
| `card.id/kind/color/number/wild` | 当前出牌；非 play 动作为零值，需先判断 action |
| `top.exists/kind/color/number/wild` | 当前弃牌顶牌；开局前可能不存在 |
| `effects.<field>` | 前面模块/步骤产生的效果计划 |

卡牌 kind 为 number/skip/reverse/draw_two/wild/wild_draw_four；万能牌的物理颜色为 wild。上下文不提供其他人的完整手牌或牌堆顺序。

## 条件与效果

每一步可设置 `when`，省略表示 true。一步只能包含 set 或 reject 之一；`reject` 立即拒绝整个动作，前面步骤也不会提交。set 的每个值都是表达式字符串，不是 JSON 布尔或数字。

同一个 set 内的表达式读取同一个先前计划，再一起写入，JSON 键顺序不影响行为。模块及步骤按顺序运行，后面的步骤可以覆盖前面的效果；为组合增加数量时应显式写 `"draw_next":"effects.draw_next + options.amount"`。

| 效果字段 | 类型 | 最终合法范围/行为 |
| --- | --- | --- |
| `allow` | boolean | 当前牌是否可以出；不绕过持牌、轮次等引擎约束 |
| `turn_steps` | integer | 1–10，推进存活座位的步数；初始为 1 |
| `reverse` | boolean | 反转出牌方向 |
| `add_penalty` | integer | 0–108，增加待结算罚牌池 |
| `draw_next` | integer | 0–108，立即让下家摸牌 |
| `hand_limit` | integer | 0 表示关闭；引擎允许 2–108，且必须大于开局手牌数；当前标准配置选项限制为 8–108 |
| `keep_color` | boolean | 万能牌可否选择当前颜色 |
| `allow_challenge` | boolean | 是否启用 +4 质疑 |
| `challenge_multiplier` | integer | 启用质疑时 100–300，150 表示 1.5 倍 |
| `play_drawn_card` | boolean | 普通摸牌后是否保留出牌/跳过机会 |
| `play_any_after_draw` | boolean | 保留机会时是否可出原有手牌 |

引擎仍统一保证身份、轮次、108 张牌守恒、UNO 窗口、牌堆回收、罚牌结算及最终效果范围。有人出完手牌后，不再允许叠加或反转延长罚牌链；无质疑窗口时立即结算当前罚牌，有最后 +4 质疑窗口时只保留摸罚牌/质疑，结算后判胜。禁牌引发的自动摸牌不会获得普通摸后出牌阶段；漏喊 UNO 的专用罚摸与普通摸牌分别计数。增加新卡牌、交换手牌、插队、新命令等尚不存在的效果，需要扩展 Go 引擎协议和不变量测试；配置不能任意修改聚合内部状态。

summary 的 when 只能读取 options；text 支持 `{{option_key}}`，选择项替换成 choices 的 label。用于描述当前房间，不能写依赖实时牌局状态的说明。

## 目录加载、重载与容器

`UNO_RULES_DIR` 默认 `rules`，相对工作目录读取；Docker 镜像默认 `/etc/unour/rules`。库的 `game.NewRegistry()` 使用同一组嵌入式默认 JSON，生产服务器始终从目录加载。

启动时目录错误直接报错。SIGHUP 先完整读取和校验候选目录，全部成功才替换目录指针；错误写入服务器诊断日志，旧目录继续服务。每目录最多 32 个模块、每文件 64 KiB、总计 256 KiB；不接受 JSON 符号链接。每模块最多 32 个选项和 32 个步骤，每表达式最多 1 KiB、256 个节点、24 层。编译缓存同时限制为 2,048 项和约 32 MiB 的估算内存。

多文件更新时，先在临时目录准备好完整规则，再原子替换版本目录/切换挂载目录后发送 SIGHUP，避免读到一半写入的数据。一次未成功重载不会改变线上目录。

镜像内规则随后端发布更新；希望只改宿主机文件时，使用可选覆盖文件：

```sh
# 路径必须指向包含完整有效规则的目录，运行用户需要只读访问。
export UNOUR_RULES_DIR=/opt/unour/rules
# 第一次挂载需要重建 api 容器；常规启动也保留同样的 -f 参数。
docker compose -f compose.yaml -f compose.rules.yaml up -d api
# 后续修改此目录内文件：向真正的 Go 子进程发送信号（compose 使用 init）。
docker compose -f compose.yaml -f compose.rules.yaml exec -T api sh -c 'kill -HUP $(pidof uno-server)'
```

宿主机规则应由部署账户维护，容器只读挂载。CI 只传应用镜像，启用挂载前须先把 compose.rules.yaml 同步到服务器。在服务器 .env 设置 `COMPOSE_FILE=compose.yaml:compose.rules.yaml` 与 UNOUR_RULES_DIR 后，现有 CI 的普通 docker compose 调用也会保留挂载；只在手工命令中临时加 -f 会在下次自动发布时丢失挂载。其他覆盖文件也应加入 COMPOSE_FILE。发布时无需重新构建前端。

## API 与房间快照

`GET /api/v1/rules` 无需登录，返回 `{revision,modules}`；仅返回控件元数据，不下发执行步骤。建房和人机请求发送 `catalog_revision` 与 `rules:[{name,version,options}]`。服务端按同一目录快照校验、排序并补齐默认选项；目录已变化时返回 `409 rule_catalog_changed`，前端重新读取并要求用户确认后再次创建，不自动重发写操作。

`room.rule_descriptions` 返回已启用模块的静态说明；`room.state.legal_actions` 只列出当前玩家自己的合法 play/card_id/color、draw、pass、challenge。观战者和非当前玩家得到空数组。它是界面提示，动作提交仍会校验最新状态与版本。

每个新房间持久化完整的模块定义和已选选项。重载配置只影响新房间；当前牌局、重启恢复、再来一局与 Bot 推演均使用房间冻结的定义。存储编解码器不依赖当前目录。房间封装快照格式提升到 v2，此次不提供旧房间格式迁移；升级前已清空旧房间即可，账户表保持不变。回滚到旧二进制前须处理它不认识的新房间快照。
