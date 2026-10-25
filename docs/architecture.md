# 架构、规则扩展与资源边界

```text
cmd/server                 组合根、HTTP 生命周期、后台任务
    ├── internal/httpapi   REST DTO/严格解析/CORS/错误映射
    │       └── room / account 用例接口
    ├── internal/room      房间/会话/权限/时钟/存储端口
    │       └── game      命令与游戏规则
    ├── internal/account   身份/凭证/会话/头像/存储端口
    ├── internal/bot       公开观察/隐藏信息采样/搜索策略
    ├── internal/botrunner 到期队列/有界计算/版本校验提交
    ├── internal/store/sqlite → room.Store + account.Store（默认）
    └── internal/store/memory → room.Store（测试/嵌入）

examples/rules → game 的公开规则协议
```

## 游戏聚合

`game.Game` 只维护一局游戏，不知道 HTTP、房间 ID、会话 token、数据库和锁。`Join` 用于大厅；`Apply(Command)` 处理状态机。时间由可信调用者通过 Command.At 提供，随机洗牌通过 Options.Shuffle 注入；默认使用 crypto/rand。

每个操作按以下顺序执行：身份/阶段/回合与版本检查 → 候选状态拷贝 → 规则规划 → 效果范围验证 → 卡牌流动/回合更新 → 淘汰/胜负 → 一次提交。规则拒绝、洗牌失败和非法命令都不提交候选状态。事件只描述公开动作，不含摸到的牌或手牌。

卡牌总量受 108 张约束，每房最多 10 人，规则最多 32 条。普通操作对本局状态做有界拷贝，无全局扫描或跨房间锁。这样使失败原子性与扩展规则隔离比局部手动回滚更容易验证；实际成本由 benchmark 测量。

状态视图显式构造、拷贝：没有通过 `json:"-"` 隐藏字段后直接把聚合交给 JSON 编码的做法。规则 Context 同样只提供值或只读查询。不可变规则可以跨对局共享，带有可变内部状态的自定义规则必须自行处理并发，并避免在 Evaluate 中产生外部副作用。

## 规则协议与优先级

Rule 的唯一 ID 防止重复注册。Registry 是显式对象，工厂将一条 RuleSpec 的 JSON options 编译成不可变规则。Build 不持有 Registry 锁执行工厂，避免工厂阻塞全局注册/其他房间创建。

`Evaluate` 在每种动作上执行，必须先检查 `ctx.Action()` 是否适用。Context 能查询出牌、顶部、当前颜色、罚牌、玩家手牌数量和是否有某颜色；不能写入聚合。Effects 提供：允许出牌、推进步数、反转、增加罚牌、让下家摸牌、手牌上限、保留颜色、质疑倍率、摸后可出牌。合法性由引擎最终检查，规则不能制造负摸牌或非法推进步数。

列表顺序就是执行顺序。基础规则先提出匹配/动作效果，后续规则可细化它们；任意 veto 立即终止。纯效果计算使规则之间的先后关系显式可测试。一个典型扩展：

```go
registry := game.NewRegistry()
if err := rules.Register(registry); err != nil { return err }
store, err := sqlite.Open(sqlite.Config{
    Path: "data/unour.db", MaxRooms: 1000,
    Codec: room.NewCodec(registry, nil),
})
if err != nil { return err }
defer store.Close()
service, err := room.New(store, room.Config{Registry: registry})
```

其中 rules 是 `unour/examples/rules`，sqlite 是 `unour/internal/store/sqlite`。规则编解码器与房间服务必须使用同一套 Registry。然后在创建房间的 rules 列表末尾追加 `{"name":"no_zero"}` 即可。注入测试 Shuffle 时，也要同时传给 Codec 与 GameOptions；函数本身不写入快照。

现有效果协议覆盖匹配限制、罚牌、跳过、转向、质疑和淘汰等 UNO 规则。交换手牌、插队或全新命令需要新增明确的领域效果/命令原语及相应不变量测试，再让规则组合使用；不开放任意状态指针，不以反射和通用 map 修改游戏内部字段。新的卡牌种类同样需要补充类型、牌堆及对应原语。这样扩展能力与卡牌守恒、回合约束能同时保留。

## UNO 专用罚牌

出牌从两张变成一张时，生成以该次提交版本为 ID 的窗口和服务器时间 +2 秒期限。声明和处罚均以本人的会话身份执行，不接受目标玩家参数。窗口记录保留最近的完成类型：同操作同窗口重复请求无副作用；声明与罚牌相互排斥。

UNO 处罚不调用正常的 drawTurn，因此不改变当前回合或罚牌池。手牌因其他操作变化会取消旧窗口，结束比赛会关闭所有窗口。真人由前端负责两秒交互与超时上报，客户端漏报不会自动补罚。电脑的声明与漏喊由统一的持久化队列处理，仍调用同一个领域命令；不为每位玩家创建计时 goroutine。

## 房间与存储

Store 的 Record 是内部聚合，包含 Game、房主、令牌摘要和规则配置。引用只在回调期间使用，不能逃逸。Read 是只读用例；Transact 和 Delete 在同进程内串行处理同一房间。API State 使用 Read，不会重写游戏或延长 TTL。

Memory 用目录 RWMutex 定位 entry 后立即释放目录锁，再获取单房间 Mutex。任何等待房间锁的代码都不持有目录锁；删除/回收的顺序则是先 entry 后目录，防止死锁。删除标记使已取到旧 entry 的等待者在删除后返回不存在，不会继续操作已失效的房间。回收使用 TryLock，跳过活跃回调；完成回调后再更新时间。

Memory 的 Transact 不回滚任意 Record 写入；用例先完成可能失败的验证，Game 的变更由自身保证原子性。SQLite 每次从已提交行重建私有候选 Record，失败或 panic 丢弃候选，既不改数据库，也没有共享游戏缓存需要回滚。

### 完整快照

game.Checkpoint 和 room.RecordCodec 分别提供版本化游戏快照与房间封装。保存所有牌的身份/位置、回合、方向、当前颜色、罚牌、+4 出牌时的质疑证据、UNO 原期限与去重结果、胜者、游戏版本、开局参数、规则 ID、RuleSpec、房主、创建时间和会话摘要。Restore 校验卡牌与标准牌堆的完整对应关系、唯一身份、规则顺序和状态机约束。

未来洗牌仍使用注入的 Shuffle 或默认密码学随机源；已有牌堆顺序原样恢复。规则是无状态的策略，由保存的 RuleSpec 与当前 Registry 重建。后续改变规则语义时需维护工厂/ID 的版本兼容性。未知快照/数据库版本、缺失规则和损坏数据均报错，不会丢弃房间后创建空状态。快照只供受信任存储使用，HTTP 始终返回过滤过的 View。

### SQLite 提交与并发

SQLite 采用 database/sql + go-sqlite3，WAL、synchronous=FULL、5 秒 busy_timeout。四个只读连接处理查询，单写连接只承担短事务提交。数据库路径按文件名编码，不拼接用户 SQL；所有查询参数化。

每次写操作：获取可取消的房间键锁 → 加载已提交行 → 在数据库写锁之外执行回调与编码 → BEGIN IMMEDIATE → generation/revision CAS 更新 → Commit → 返回。不同房间的规则计算可以并行，SQLite 的物理写入按数据库限制串行。键锁按等待者引用计数回收，不为历史房间永久保留锁。

generation 是创建时的随机身份，revision 是存储修订号，独立于对局 version。两者同时比较防止跨实例覆盖，也防止房间删除、同名重建后旧操作覆盖新房。竞争失败返回 409 version_conflict，不自动重复调用回调。容量检查与创建在同一写事务内完成。

清理按最后成功写入时间查询，并通过 generation/revision/时间条件删除。本进程正在执行写回调的房间会跳过；其他进程与清理冲突时由 CAS 阻止旧数据复活。TTL 默认为 0，不启动自动清理。显式开启 TTL 后，读取不续期，停机时间计入期限。

数据库 PRAGMA user_version 当前为 4；初始化 schema 在事务中执行，未知高版本拒绝启动。启动扫描并校验现存快照和电脑调度投影。文件权限为 0600，新建目录为 0700。此适配器支持本地文件部署；未来切换服务型数据库可复用 RecordCodec 与 Store.Read/Transact 边界，并实现相应的事务与冲突控制。

连接与 WAL 的依据参见 [go-sqlite3](https://github.com/mattn/go-sqlite3)、[SQLite WAL](https://www.sqlite.org/wal.html)、[SQLite 事务](https://www.sqlite.org/lang_transaction.html)。

## 网络与部署

HTTP 适配器依赖 Rooms 用例接口。电脑调度同样通过房间用例调用领域命令；未来 WebSocket、RPC 也应沿用此边界，并返回按玩家过滤的 View。当前 REST 客户端通过 GET 状态同步，响应中的事件不是持久化消息队列。

默认启动一个进程。日志记录 method/path/status/duration，不记录 Authorization、Cookie 或请求体。CORS 允许来源显式配置，账户使用 HttpOnly Cookie，房间还需要隔离的随机 Bearer token。普通房间由首次加入者主持；人机房创建、真人入座、电脑入座和开局原子完成，创建真人为初始房主；主动退出时房主权限在房间事务中转交。

对公网开放时，可在反向代理层接入 TLS、连接/请求速率限制，再按产品需要添加房间邀请策略；这些机制放在适配器/房间用例边界，不改变 UNO 核心。

## 电脑决策与后台调度

`internal/bot` 只能接收 `Observation`：按本人过滤的 `game.View`、公开弃牌记忆和规则配置。真实 `Game`、持久化 `Checkpoint`、其他手牌和真实牌堆均不传入。未知牌从标准牌库扣除已知物理牌后采样，对手摸牌和选色仅提供软证据；主动摸牌不等于确定无色。每次搜索迭代重新采样，树边合并信息集动作与可用次数；各玩家的 UCB 使用自己的奖励，避免电脑组成一个针对真人的阵营。启发式只作为先验和模拟策略。

采样产生独立的假想对局，再由同一规则 Registry 和 game 引擎推演。共享规则必须不可变；模拟使用自己的随机源。真实游戏的洗牌与隐藏状态不影响给定观察和种子的决策。公开 `bots` 资料与私有记忆分开构造，HTTP 不序列化内部 Record。

`room` 持久化电脑配置、公开记忆、回合行动期限和每个 UNO 窗口的计划反应。`next_bot_at` 是快照的有索引派生列，Create/Transact 与快照在同一数据库事务中写入；只读队列查询不更新游戏或 TTL。无电脑的房间没有队列项。

`botrunner` 每 100ms 拉取到期房间，默认两名 worker，同一房间不会被本进程重复调度。先读取脱离存储的任务，再释放锁计算，用版本与当前任务校验提交；真人动作或另一项 UNO 提交使旧决策失效，重读后重新决定。失败任务持久化延后，避免堵住其他任务；窗口已过期时声明改为处罚。停服取消搜索并等待 worker 退出后，才关闭数据库。该队列按当前单进程 SQLite 部署设计；增加多实例需补任务租约，游戏提交的 CAS 不能代替跨进程任务调度。

## 账户边界

`internal/account` 不依赖 HTTP、房间或规则；SQLite 实现账户仓储。账户与首次会话原子提交，用户名规范键有唯一索引，头像内容寻址，公开资料与密码记录分别构造。密码使用标准库 PBKDF2-HMAC-SHA256（600000 次、32 字节随机盐），长度为 8–14 个 Unicode 字符。

HTTP 先验证 HttpOnly Cookie，再校验可选 X-Account-ID 以防浏览器标签页身份漂移，然后用 room.WithAccount 限定房间凭证。房间聚合持久化账户到座位的绑定；重入复用座位并轮换令牌。账户资料由 HTTP 批量补全到 profiles，不进入游戏聚合。头像变动不会伪造游戏版本；客户端单独检测资料变化。

头像在有界工作槽中解码、裁切、缩放、重编码，原始图片不落盘；数据库只保留仍被引用的规范 PNG。更换或恢复默认头像在同一事务修改资料并回收无引用图片。规则扩展继续只依赖 game 协议。

## 房间生命周期与游戏聚合

房间成员和当前局座位分开保存。Spectators 表达观战与下局预约，Departed 保留当前局退出者的牌数守恒所需状态。规则引擎提供原子 Rematch 和 Leave，账户认证、同意名单、主持权和生命周期属于 room 层，HTTP 仅做字段解析和身份适配。

再来一局使用 round 与 roster_version 拒绝跨局或旧名单的同意；报名/退出改变名单时撤销已有同意，纯观战变化不会撤销。最后一票和重新发牌同事务提交，电脑记忆和调度一起重置。Game 的版本跨局单调递增；房间 revision 汇总牌局和成员变化，防止轮询旧响应覆盖新投票。Codec 校验身份互斥、席位容量、离开者凭证撤销和投票资格，持久化所有生命周期字段。


## 管理边界

`internal/admin` 读取私有密码哈希文件，管理独立且短期的内存会话；`internal/credential` 共享既有密码哈希格式。账户管理通过单独的 ManagementStore 端口更新封禁状态、凭证版本和会话，房间管理通过 AdministrationStore 端口进行完整手牌读取和带审计的事务。玩家接口不依赖这些管理端口，也不复用管理 DTO。

引擎的可信 `EditCards` 仅交换或移动既有牌，不允许改写牌值、弃牌或淘汰玩家，所有操作保留牌组守恒。房间版本使旧 bot 计算失效，公开记忆重置后继续从正常观察学习；审计与房间快照同事务提交。管理会话与密码哈希不会序列化进房间或公共资料。

管理目录只投影公开元数据，不恢复全部游戏引擎，也不把手牌、会话摘要加载到列表 DTO。统计、筛选与分页在一个 SQLite 读快照中完成。管理写操作携带房间 lifetime 的 `instance`，删除再校验阶段，提交仍使用 generation/revision CAS；批量操作逐房间事务并返回每项结果。永久删除仅保留不含快照的审计记录，在途写入不能恢复已删除房间。

加入锁定是房间成员策略，不进入 UNO 规则引擎；已有账户可恢复房间凭证。管理移出与主动离开共享成员移除流程，统一处理弃权、房主、会话、预约与同意名单；禁止移出 Bot 或最后一名真人。锁定、移出、编辑与删除均要求成功保存对应审计才可提交。
