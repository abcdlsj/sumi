# DESIGN

Sumi 让 Human 与 Agent 在同一个 Space 中持续协作。Agent 是持续在线、可被新消息打断或转向的协作者；Agent 身份、Message、Task、Result 和 Memory 在 Run、Provider Session、Computer 生命周期之外持续存在。

本文件是 Sumi 唯一的设计文档，分「产品」「系统」「界面」三部分：产品部分定义要求与领域词汇，后面两部分引用这些词汇。代码不是事实来源；实现与要求冲突时必须修改实现，禁止用兼容层保留两套行为。

## 产品

### 领域词汇

以下词汇是代码、协议、API 和 UI 共用的规范语言。使用 _Avoid_ 中的词会引入第二套概念。

| 词 | 定义 | Avoid |
| --- | --- | --- |
| Member | 属于一个 Space 的协作者，Human 与 Agent 共用该模型 | Participant、Actor |
| Display Name | Member 在 Space 中的唯一寻址名称，由字母和下划线组成，不含空格 | Handle、Username |
| Human | 由人控制并通过账号进入 Space 的 Member | User |
| Agent | 具有持续身份、Role、Memory，并由一台 Computer 承载的 AI Member | Bot、Assistant |
| Role | Agent 在 Space 中承担的职责边界，不表示治理权限 | Persona |
| Memory | 归属于一个 Agent、跨 Task 和 Run 持续存在的本地知识 | Session、Context |
| Space | Members、Channels、Computers 和治理共同归属的协作边界 | Team、Workspace |
| Channel | Space 中一组 Members 共享的长期对话空间，DM 是特殊 Channel | Room、Group |
| DM | 恰好由两个 Members 组成的直接对话 Channel | Private Chat |
| Message | Member 发布在 Channel 主时间线或 Thread 中的协作内容 | Prompt、Event |
| Root Message | 发布在 Channel 主时间线的 Message，每条 Root Message 是一个 Thread 的根 | Top-level Message |
| Thread | 一条 Root Message 及其 replies 组成的讨论支线，继承 Channel 可见范围 | Session、Conversation |
| Attachment | 由 Member 上传并附加到 Message 的持久文件 | Blob |
| Action Message | Agent 完成领域 Action 时由同一事务创建的结构化 Message | System Message、Audit Event |
| System Notice | Server 因 Channel 成员关系变化写入时间线的结构化 Message | System Message |
| Task | 从一条 Root Message 原子创建的持续工作记录，可关联多个 Thread | Job、Workflow |
| Task Reference | 以 `!<seq>` 写在 Message 正文中的 Task 引用，seq 是全库自增短序号，各 Space 的 seq 因此不连续 | Task UUID、Task ID |
| Task Status | Task 的工作流位置，只取 TODO、In Progress、In Review、Done、Closed | Run Status |
| Source Thread | 创建 Task 的 Root Message 所定义的 Thread，必备且不可更换 | Origin Conversation |
| Linked Thread | 与 Task 中同一项工作直接相关的 Thread，Source Thread 是第一个 | Related Conversation |
| Focus | 一个 Run 当前处理的唯一 Thread；Run 绑定 Task 时必须是 Linked Thread | Scope、Current Task |
| Result | Task 对协作者公开的正式工作结论，是一条由 Task 指定的 Message | Run Output |
| Computer | 与 Space 配对、运行 Sumi daemon 并承载本机 Agents 的计算机 | Node、Worker |
| Computer Release | 由 Computer 所连接的 Server 指定、面向一个受支持平台且版本不可变的 daemon 发布物 | Latest Binary、Update Package |
| Update Policy | Computer 本地持久化的自动更新选择；Server 不得覆盖 | Force Update |
| Agent Home | 归属于一个 Agent，保存 Memory、workspace 和 Driver 私有状态的本地边界 | Workspace、Computer Home |
| Driver | Agent 用于推理和行动的可替换执行能力，取 ACP Driver 或 Builtin Driver | Engine、Model |
| ACP Driver | 经 Agent Client Protocol 驱动外部 coding agent 的 Driver，provider 取 codex、claude、pi | Codex Driver、app-server |
| Builtin Driver | Computer 进程内由 `sumi-builtin-agent` harness 执行的 Driver，不依赖外部 coding agent | Fake Driver |
| Provider Session | Computer 为一个 Agent 处理一个 Thread 或 Task 时保存的 Driver 对话缓存 | Agent Session |
| Run | Agent 围绕一个 Focus 完成一次有界处理的执行 | Job、Turn |
| Trigger | 要求某个 Agent 开始工作的事件，产生 Run，本身不是队列项 | Inbox Item、Claim |
| Waiting | Agent 没有活跃 Run 的执行状态，是正常状态 | Idle、Sleeping |
| Inbox | Member 接收待关注信息的持久入口 | Event Queue、Notification Stream |
| Inbox Item | Inbox 中一条等待处理、延后或重试的注意力事实 | Job、Event |
| Hard Item | 需要 Agent 明确处理的 Inbox Item，例如 DM、mention、reply | Urgent Message |
| Ambient Item | 由一段 Channel Activity 聚合形成的 Inbox Item，可以延迟处理 | Background Message |
| Yield | Agent 结束当前 Run 并保留未完成工作的明确决定，不改变 Task 状态 | Cancel、Pause |
| Access Level | Member 在 Space 中的治理级别，取 Owner、Admin、Member | Role、Agent Role |
| Permission | 授予 Member 执行一个特定 Action 的能力 | Role、Access Level |
| Owner | 对 Space 承担最终控制与恢复责任的唯一 Human Member | Super Admin |
| Admin | 可以授予 Human 或 Agent 的 Space 管理 Access Level | Agent Admin |
| Computer Token | Computer 首次配对时生成并长期持有、用于向 Server 证明身份的凭据 | API Key |
| Driver Token | daemon 为每个 Agent 从内存 secret 派生的本地 capability 凭据 | API Key |

### 产品不变量

- Human 与 Agent 使用同一套 Space、Channel、DM、Thread、Message、Attachment 模型。
- Task 必须从 Root Message 原子创建，Source Thread 绑定后不可更换；Thread reply 不能成为 Source。
- 一个 Task 只能有一个 Source Thread，Source Thread 在 Task 结束后仍不可被其他 Task 作为 Source 复用；一个 Task 可以关联多个 Linked Thread；一个 Run 只处理一个 Focus；一个 Agent 同时最多有一个 active Run。
- Task 创建与开始执行分离：绑定当前 Run 时创建即 In Progress；未绑定当前 Run 时保持 TODO，由 pending hard TaskActivity Item 驱动的首个 Run 进入 working 后推进为 In Progress。
- Task 创建后 title 与 assignee 不可修改；对 Task 的操作只有状态流转（TODO → In Progress → In Review → Done / Closed）、退回（In Review → In Progress，附一句要改什么）与 Close（记录原因）。
- Run 有界、无期限、不持有执行凭据；Server 不因时间改变 Run 状态；失败只由 Computer 上报。
- Provider Session 是 Computer 本地缓存，丢失或更换 Driver 不影响 Task、Message、Result、Inbox。
- Server 与 Computer 只通过版本化协议交换命令、快照、回执和查询。
- 路由和归属来自结构化事实（mention targets、Task link、Thread 订阅、Item strength），不解析 Message 正文。
- Agent 发布的普通 Message 不触发其他 Agent 的 ambient；mention、reply、DM 和 Task Activity 等显式 hard 路由仍然有效。
- Agent 对一次请求只回复一条包含答案的 Message，不发确认、复述或收尾总结；工作状态由界面显示，只有持续数分钟且带新信息（发现、决定、阻塞）的工作才发中途更新。
- 每个事实只有一个写入入口，一个领域命令只在一个事务中完成。
- Agent 不负责补写可由系统从请求上下文推导的关系。
- Agent 对需要跨 Run 长时间跟踪状态的工作必须创建 Task，并持续用 Source Thread、Linked Threads 和状态流转记录流程与进展；单次回复或同一 Run 内完成的工作不创建 Task。
- Channel 的 `channel_seq` 是该 Channel 内所有 Message（Root、reply、Action Message 和 System Notice）共用的唯一递增坐标。Thread 保持独立的 `thread_id`，由 Root Message 建立并包含该 Root 及其 replies；Channel 主时间线只展示 Root，Thread 视图展示该 Thread 的完整内容。`channel_seq` 是引用坐标，不是当前列表行号。
- Agent Run 的动态 Channel Activity 按 Agent + Channel 以 `through_seq` 做增量摄入；Computer 通过独立系统 query 在冻结的 `channel_snapshot_seq` 内读取增量。当前 Focus Thread 和 claimed Hard Item 保留原文，已摄入的普通活动不得在后续 Run 中重复追加。只有 Completed 或 Yielded Run 推进 `through_seq`；Failed 或 Canceled Run 保留待摄入活动。
- 运行中到达的 Hard Item 必须获得明确 delivery outcome。Builtin Driver 在模型调用、工具批次和最终完成屏障之间通过有序 mailbox 接收新 Item；返回 Accepted 后该 Item 必须进入当前 Run，否则返回 TooLate。ACP 没有回合内追加输入的能力，ACP Driver 对运行中的回合返回 Unsupported，不取消当前回合重发。TooLate 或 Unsupported 不使原 Run Failed，并自动释放该 Item，由下一个 Run 处理。
- Computer 默认安装所连接的 Server 指定的 stable Computer Release；普通使用者不配置密钥或安装目录。
- Computer 首次从临时下载路径运行时，把自身安装到用户级稳定路径并从该路径继续运行；更新只替换稳定路径中的 daemon 二进制，不修改或删除 Memory、workspace、Provider Session 与 Computer 身份。
- Computer Release 可以在 Run 活跃期间下载，但只能在没有活跃 Run 时激活；失败的同一 Release 不得自动重复激活。

### 范围边界

不做：

- 子任务、Task 依赖、工时、截止时间、优先级、审批流。
- 旧数据迁移、旧 API 兼容、Windows 运行支持。

### 验收底线

- 空 PostgreSQL 与空 Computer 本地状态能完成核心流程。

## 系统

### 运行边界

Server：

- 持有 Space、Member、权限、Channel、Message、Thread、Attachment、Task、Inbox Item、Run 状态。
- 执行领域状态转换与事务；持久化 outbox；投递 Computer command；提供 Browser API、SSE 和 Computer WebSocket。
- 不执行模型，不保存 Provider Session、Memory、workspace 或模型凭据。

Computer：

- 持有 Agent Home、Memory、workspace、Provider Session、Driver 进程、sandbox、本地 outbox 和凭据。
- 不持有 Server 事实的正式状态，不决定 Task 归属；缓存的 Run 快照只用于执行。

Agent CLI：

- 是当前 Run 内唯一的 Sumi 操作入口；Agent、Space、Task、Focus、Run 由 Driver token 推导，不要求 Agent 重复提交。
- 不保存领域状态。

Driver：

- 只把 Run 和 Session 语义映射到 provider；Driver 输出不能直接成为 Message、Result 或 Memory。
- Agent 的 Driver 配置为 `{kind: acp, provider}` 或 `{kind: builtin}`。Server 以 `agents.driver_kind`（acp、builtin）和 `driver_config_json.provider` 保存；新增 provider 不改 CHECK 约束。
- ACP Driver 为每个 Agent 常驻一个 ACP agent 进程（stdio JSON-RPC，协议 v1），Provider Session locator 是 ACP `sessionId`。provider 对应的进程命令固定版本：codex 为 `@zed-industries/codex-acp@0.16.0`，claude 为 `@zed-industries/claude-code-acp@0.16.2`，pi 为 `pi-acp@0.0.34`，经 `npx` 启动。Computer 配置 `[computer.acp.<provider>]` 的 `command`（argv 数组）替换该 provider 的命令，`env` 为进程追加环境变量，例如 `command = ["ak", "acp", "claude"]` 与 `env = { AK_CONFIG_DIR = "/Users/you/.config/ak" }` 让凭据由 ak 解析；`env` 不得覆盖下一条列出的 Sumi 变量。含 `env` 的配置文件必须仅本人可读。
- ACP 进程环境只含 `HOME`（Agent Home）、`PATH`、`LANG`、`SUMI_SOCKET`、`SUMI_DRIVER_TOKEN`、`[computer.acp.<provider>].env` 与 provider 私有项：codex 为 `CODEX_HOME=drivers/codex` 及 config 声明的 env_key；claude 为 `CLAUDE_CONFIG_DIR=drivers/claude` 及 daemon 环境中的 `ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`CLAUDE_CODE_OAUTH_TOKEN`；pi 为 `PI_CODING_AGENT_DIR=drivers/pi` 及 daemon 环境中的 `ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`GEMINI_API_KEY`、`OPENROUTER_API_KEY`。
- Computer 配置 `pi_agent_dir_source` 指向本机 pi agent 目录（通常是 `~/.pi/agent`）时，pi Agent 在 provision 与 configure 时从中复制 `settings.json`、`auth.json`、`models.json` 到 `drivers/pi`；`settings.json` 声明的 packages 由 pi 自行安装。`AGENTS.md`、skills、extensions 与 sessions 属于本机用户的个人上下文，不复制。
- ACP Driver 以全权限运行：session 建立后若 agent 提供全权限 mode（codex `full-access`、claude `bypassPermissions`）则切换到该 mode；`session/request_permission` 选择 allow 选项。Computer 不宣告 fs 与 terminal client capability。
- ACP 恢复 Provider Session 时，agent 宣告 `sessionCapabilities.resume` 则用 `session/resume`，否则宣告 `loadSession` 则用 `session/load` 并丢弃重放的 `session/update`；两者都不支持或请求失败时 Session 丢失。关闭 Session 仅在宣告 `sessionCapabilities.close` 时发送 `session/close`。
- ACP 回合以 `session/prompt` 响应结束：`end_turn` 为 Completed，`cancelled` 为 Interrupted，其余 stop reason 与错误为 Failed。interrupt 发送 `session/cancel`。

### 协议与投递

- 当前 Computer 协议 schema 为 v6，Server 与 Computer 只宣告当前版本；wire required field 变更时提升版本，不在同一版本中兼容两套 schema。
- 双方无共同版本时拒绝连接。
- Computer 每 15 秒发送 heartbeat 与 WebSocket ping；任一端 45 秒内未收到任何帧即判定连接失效并断开，Computer 随即重连。建连 20 秒、单帧发送 30 秒超时。连接持续 60 秒以上后，重连退避从最小间隔重新开始。Server 只在同一 Computer 没有更新的连接时把它标记为 offline。
- 写命令先持久化（稳定 command ID + 每 Computer 递增序号）再投递；Computer 先落本地再 ACK；重复命令按 ID 幂等。
- query 是独立请求-响应通道：不持久化、不重放、不进 command 序号。
- RunStart 的 Channel activity 查询被 Server 拒绝时，Computer 只对该命令回 Rejected，连接继续处理其余命令；Server 按拒绝结果补偿该 Run。传输失败才断开连接。
- 业务写入与 outbox 属于同一事务；外部发送失败由 outbox 重试。
- Computer 重连握手同步 daemon session ID、command watermark 和本地 Run 实况；上一连接会话的残留帧必须被丢弃。
- 所有 HTTP 写操作使用 idempotency key；started、delivery、result、receipt 使用稳定 event ID 幂等。

### Computer 更新

- Server 从配置的 stable release 目录只公开 manifest，不托管二进制。目录按 target 分子目录，Server 只响应 `/api/v1/computer-updates/stable/<target>/manifest.json`。manifest 包含 daemon version、protocol version、target、artifact 下载 URL 和 sha256；artifact 由发布流程上传到 GitHub Release。一台 Server 因此可以同时服务多个平台的 Computer。
- Computer 只从已连接 Server 读取 manifest，再从 manifest 中的 HTTPS URL 下载 artifact，并以 manifest 的 sha256 校验；sha256 不符的 artifact 不得写入 staging。生产传输使用 HTTPS。Server 是其所承载 Computer 的更新信任边界，不增加全局发行密钥。
- release Computer 首次从稳定路径之外运行时，把自身原子复制到 `~/.sumi/bin/sumi` 并从该路径重新执行；已存在的稳定副本优先，防止旧下载文件覆盖已更新版本。debug 构建不自动安装。
- Computer 在启动后检查更新，此后按配置周期检查。Update Policy 关闭自动更新时，Computer 不自动安装或检查发布物。
- Computer 在本地校验 target、版本和 sha256，再把 artifact 写入私有 staging 目录。下载和校验不得读取 Message、Attachment、Memory、workspace、Provider transcript 或 Secret 正文。
- 有活跃 Run 时只保留 staged artifact。没有活跃 Run 时，Computer 启动一次性 updater 并退出；updater 备份当前二进制与关闭后的 SQLite 文件，再替换二进制并启动新 daemon。
- 新 daemon 完成本地数据库、sandbox、Driver recovery 和 capability socket 初始化后写 readiness 标记 `update/ready-<version>`。readiness 超时或进程提前退出时，updater 恢复旧二进制与 SQLite 备份并重启旧 daemon。
- Computer 可由用户级服务管理器托管：macOS launchd agent `dev.sumi.computer`，Linux systemd user unit `sumi-computer.service`。服务以 `SUMI_COMPUTER_SERVICE=launchd|systemd` 声明托管方；服务只在非正常退出时重启。托管时 updater 脱离服务的进程组（launchd）或作为独立 transient unit 运行（systemd），daemon 以成功状态退出后，updater 经服务管理器启动、重启新旧 daemon，不自行 spawn。
- Computer 在 SIGINT 与 SIGTERM 时都按正常关闭流程断开 Server。
- Server 关闭时先向每条 Computer WebSocket 发送 `shutdown` 帧再关连接，并等这些连接写完最后一帧、完成关闭后才退出进程；等待有上限，单个 Computer 不响应不得拖住进程。
- Server 在 `/install.sh` 提供 Computer 安装脚本，用法为 `curl -fsSL <server>/install.sh | sh -s -- --server <server>`。脚本按平台从同一 Server 读取 stable manifest，从其中的 URL 下载 artifact 并校验 sha256，安装到 `~/.sumi/bin/sumi`，缺少 `~/.sumi/config.toml` 时写入 `server_url`，再写入并启动上述用户级服务。重复执行是幂等的：已安装的二进制交给 daemon 的 updater 更新，脚本不替换；服务定义不变时只确保服务在运行，不重启。未配对时脚本从日志 `~/.sumi/logs/computer.log` 打印配对链接。
- updater 在本地 journal 记录成功或回退。已经回退的 release 不得再次自动激活；新的 release 仍可尝试。
- 更新发现使用独立 HTTP 接口，不依赖 Computer WebSocket 是否能完成业务协议握手。

### Run

- 状态为 dispatched → working → completed / yielded / failed / canceled。不设置 queued、starting、finalizing、stopping。
- 失败错误码固定为 driver_error、driver_lost、computer_restarted、session_unavailable、agent_unavailable、invalid_command、unhandled_items、internal；Failed Run 必须带 error_code，其他终态不得带 error_code。
- Driver 临时错误在 Computer 内最多尝试 3 次（首次加 2 次重试）；最终失败才上报，Server 只对该 Run 计一次 Item retry。
- Computer 离线是可达性问题，不是 Run 失败；离线期间 Run 保持原状态。
- 一个 ACP 进程失败只让其 Agent 的 Run 以 driver_lost 失败；同一次轮询中其他 Agent 的完成照常投递，不使连接循环报错。
- `computer.max_concurrent_runs` 只是内存保护阈值，不是调度器；超过上限的 Run 排队等待，不因等待失败。
- Agent restart 先请求 active Run graceful stop；有限等待后按 computer_restarted 收尾并保留结果 outbox。
- yield 对本次 Items 逐条给出 handled、released 或 deferred；deferred 用 Item 的 available_at 表达。

### Inbox 与注意力

- Inbox Item 是持久注意力事实，不复制 Message 正文。
- hard Item 来源：DM、mention、`@all`、reply、Linked Thread 新消息、system 错误。必须显式处理，Driver 最终回复不构成处理。
- ambient Item 来源：Agent 订阅的 Thread 更新和所在 Channel 的活动。按 Agent + Thread 或 Agent + Channel 聚合，用 debounce 和 force 上限防止无限推迟。
- 同一 Message 对同一 Member 只生成一个最高强度 Item；发送者不为自己生成 Message Item。
- Human 可通过一次命令把所有 pending 的本人 Item 标记为 handled；assigned、deferred、dead 不进入该命令，Item 不复制 Message 正文的不变式不受影响。
- 一段对话只由一个 Run 回应：hard Item 与 working Run 属于同一 Agent 且同一 Focus Thread 时 attach 到该 Run。两者只有一方带 Task 时，在同一事务把另一方绑定到该 Task（Run 绑定时下发 RunTaskBound，TODO Task 按首个 Task Run 规则推进为 In Progress）；两者带不同 Task 或 Focus 不同时保持 pending 并发送 notice，notice 不泄露正文。
- Run 进入 working 时，同一 Agent、同一 Focus Thread 上 pending 的 hard Item 按同一规则 attach，补上 dispatched 期间到达的 Item。
- Run 失败时未处理 Items 返回 pending 并增加 retry_count；超过 max_retry_count 进入 dead，并创建不含正文的 system Item。system Item 自身进入 dead 时不再创建新的 system Item：一个无法处理的 notice 只终结一次，不自行续链。
- Agent 显式 release 的 Item 不增加 retry_count；重复 command、receipt 丢失和重复 result 不重复计数。
- 终端结果中的默认 Released 不覆盖同一 Run 已由 Server 记录的显式处置（handled/deferred）；冲突时以 Server 记录为准。
- Agent 对外暴露的 `last_error_code` 取该 Agent 最近一次终结 Run 的失败原因：其后若有 completed、yielded 或 canceled 的 Run，旧失败原因不再上报；pending Item 自身的 dispatch 错误在无更近失败 Run 时才作为兜底。
- dead Item 可由治理者 requeue 回 pending 并重置 retry_count；同一事务递增 requeue_count 并写 audit。
- Computer 拒绝 attach 或 start 时，Server 先完成补偿事务（释放 Item 或终结 Run），补偿成功后才 ACK command；重复回执不重复释放或计数。
- delivery receipt 持久化 delivery outcome、稳定 event ID 和 receipt 时间。Accepted Item 最终没有 disposition 时 Run 以 `unhandled_items` 失败；TooLate 或 Unsupported 记录为 released，不污染原 Run 失败状态。诊断可沿 message → inbox item → run item → delivery outcome → disposition → run result 查询。

### Task 与协作事务

- 同一 Root Message 最多一个 Task；相同 idempotency key 返回同一 Task。
- Agent CLI 的 `task create` 可携带单个 `--source` 指定 Source Thread，不指定时用当前 Focus；Source Thread 必须是 Root Message，且该 Thread 已存在 Task（含终态）时报 Conflict；`--thread` 可重复，在创建事务内完成 Linked Thread 关联；`task link-thread` 只用于对已存在 Task 补充关联。
- Agent 用 `--source` 创建不在当前 Focus 的 Task 时，Task 为 TODO，并在同一事务为 assignee 创建 pending hard TaskActivity Item（thread_id 与 task_id 指向新 Task）；当前 Run 结束后该 Item 经 claim 启动绑定新 Task 的 Run，Run 进入 working 时 TODO 推进为 In Progress。
- Related Thread 只能关联可见范围兼容的 Thread；一个 Thread 同时最多关联一个未结束 Task。
- 状态转换：TODO → In Progress → In Review → Done；In Review → In Progress（退回）；TODO / In Progress / In Review → Closed。
- 第一个 Task Run 进入 working 时，TODO 推进为 In Progress；In Progress 可直接进入 Done。
- Done 必须已有 Result Message；Closed 必须保存结构化原因；In Progress 和 In Review 必须有 assignee。
- Done 的 Result 二选一：发布一条新的 Result Message（`--result-file`），或指定完成者本人已发布在 Task 关联 Thread 中、未删除的文本 Message（Agent CLI `--result-message <message-id>`）。结论已作为回复发出时指定该回复，不重复发布。
- Review 可由除 assignee 外能读取 Task 的 Human 或 Agent 确认 Done 或退回 In Progress；退回必须带一句要改什么，Browser 在同一操作里先把这句话作为普通 reply 发到 Source Thread，再调用 request changes，服务端只改状态；完成时 Result 由 Browser 传入，不再写死文案。不保存 reviewer 字段，不使用 Permission。
- submit_review、done、close 只有一个事务入口，Browser 与 Agent CLI 共用。
- 普通 Message API 不能创建 Action Message；创建 Channel 或 Agent 的入口必须生成对应 Action Message。
- Channel 成员加入或离开与 System Notice 在同一事务写入。
- Agent CLI 的 `channel invite` 与 `channel remove` 分别执行 `channel.invite` 与 `channel.remove`；目标必须是同一 Space 中仍有效的 Member，成员关系变更、System Notice、Inbox Activity 和审计记录在同一事务写入。
- Run 默认只注入当前 Focus 所属 Channel 的 active Members；Space Members 与 Agent 所属的任意 Channel Members 通过 `space members` 和 `channel members` 只读命令按需查询。
- RunStart 只冻结 Focus 所属 Channel 的 `channel_snapshot_seq`。Computer 为每个 Agent + Channel 保存 `through_seq` 和单一可替换的活动投影，并通过独立 query 通道分页读取 `(through_seq, channel_snapshot_seq]`；缓存丢失时才从可见起点重建。只有增量投影进入当前 Run，失败或取消不推进，成功或 Yielded 才推进；动态投影替换旧版本，不把多版 Run Context 追加到 Provider Session。主动 `channel read` 仍可用于历史检索，不是正常协作的前置动作。
- 所有写操作只有 Server 一个事务入口；Agent CLI 与 Browser 不得各自实现一套终态事务。

### Provider Session 与 Memory

- Provider Session 按 (Agent, scope, generation) 复用。
- 复用由 Driver、workspace、Role、audience fingerprint 决定；不兼容时创建新 generation。
- 必须换新 generation：Task 终态、Linked Threads 成员集合不兼容、Driver/Role/workspace 变化、locator 丢失或 resume 失败、显式 reset。
- 不得单独触发换新：token 量达到阈值、Run 数量、固定时间、Server 或 daemon 重启、yield 等待。
- Session 丢失后 Computer 创建新 generation，并从 Server 事实、Agent Memory 和结构化 Run 结果重建执行上下文。
- Builtin Provider Session 持久化每次模型调用的 token usage 与嵌入方写入的上下文元数据；`sumi-builtin-agent` harness 把每次压缩记录（触发原因、边界、估算 source/summary token）写入 session metadata，供运行期观测与 harness 基准测试使用。
- `sumi-agent-core` 是通用 agent runtime，不拥有 prompt 与压缩策略；`sumi-builtin-agent`（Sumi Computer）与 `sumi-telegram-agent`（Telegram）各自实现 `ContextStrategy`，提示词完全独立。
- Builtin 上下文压缩按模型上下文窗口与触发比例（`computer.builtin.context_window_tokens`、`compaction_trigger_ratio`）预判触发，provider 返回上下文超限错误时再触发一次压缩重试；两种路径都写入压缩记录。
- Builtin 压缩保留最近 `compaction_keep_recent_tokens`（默认 20000）token 的原始消息，切割点只落在 user/assistant 消息上；切到 turn 中间时为 turn 前缀单独生成摘要，并在摘要后附加被压缩消息中的文件读写清单。
- Builtin 上下文用量估算优先使用 provider 最近一次调用上报的 input tokens，再按字符数估算其后追加的消息；没有用量数据时才全部按字符估算。
- `memory/MEMORY.md` 是每个 Run 开始必须读取的主文件；产生影响后续协作的新知识时，Agent 必须在相关对外动作前写入。
- Builtin 文件工具与 bash 以 Agent Home 根为路径基准：文件工具使用 `workspace/<path>` 或 `memory/<path>`，裸路径（`MEMORY.md`、`notes/<topic>.md`）默认落在 Memory 根，绝对路径仅接受落在 `workspace/` 或 `memory/` 内的；bash 使用同一路径，shell 写入允许 `workspace/`、`runs/`（`TMPDIR`）与系统临时目录 `/tmp`（macOS 沙箱放行 `/private/tmp`，Linux 沙箱挂载私有 `/tmp`），持久文件放 `workspace/`，`/tmp` 只作 scratch。macOS 系统 bash 3.2 的 here-doc/here-string 临时文件固定写 `/tmp` 且忽略 `TMPDIR`，因此放行 `/tmp` 是 heredoc 可用的前提；有 Homebrew bash（`/opt/homebrew/bin/bash` 或 `/usr/local/bin/bash`）时优先使用，其临时文件遵循 `TMPDIR` 落入 `runs/`。Memory CLI 的 `path` 相对 Memory 根（如 `MEMORY.md`、`notes/<topic>.md`）。
- Memory 与 workspace 不上传 Server；Server 只保存投影（文件名、大小、SHA-256、更新时间）并在需要时查询在线 Computer；正文读取设置 no-store。
- Memory 不复制 Message 历史或 Provider transcript；symlink 可能指向 Memory 根之外，投影和正文读取不跟随。
- Agent 退役保留身份、Message、Task、Result；Memory 和 workspace 可能丢失，UI 必须说明该限制。

### 独立 Telegram Agent

- Telegram update 只能在对话 worker 完成处理并持久化会话进度后推进 offset；仅放入内存队列不构成确认。同一 chat 串行处理，不同 chat 可并行。
- 多个 chat 共享的会话索引只有一个进程内写入入口；会话 locator 与已处理 message ID 在同一次持久化中更新，并发 chat 不得相互覆盖。
- 到期 scheduled task 在执行成功前保留可恢复状态；进程退出、turn 启动失败或执行失败后仍可重试。循环任务从当前时间推进到下一个未来周期，不逐次补跑离线期间的历史周期。

### Agent CLI

- 所有自动化调用使用 `--json`；stdout 只能输出一个 JSON envelope，诊断输出写入 stderr。
- 稳定错误码：invalid_argument、unauthenticated、permission_denied、not_found、conflict、context_changed、rate_limited、unavailable、internal。
- `details` 只包含错误处理需要的结构化标识，不得包含 Message、Attachment、Memory、Secret 或 Provider transcript 正文。
- Server 为每个 Run 记录已观察位置，Agent 不传序号。位置有三类：Channel 全量、Channel Root、Thread。RunStart 把 Focus Channel 全量位置设为 `channel_snapshot_seq`，把 Focus Thread 位置设为 Focus 最新序号；`thread read` 推进 Thread 位置，`channel read` 推进 Channel 全量位置。位置只能推进到本次实际返回的最大序号，且仅当原位置之后、该序号之前没有未返回的其他成员文本消息。Thread 的有效位置取 Thread 与所属 Channel 全量位置中的较大者；Channel Root 同理。
- Agent `message send` 在锁定目标 Channel 的同一事务内检查目标是否有该 Run 未观察的其他成员文本消息：回复检查目标 Thread，新 Root 检查目标 Channel 的 Root。目标没有观察位置时不检查。
- 存在未观察消息时不提交 Message、不处理 `--handle` Item、不记录 idempotency；重发时必须再次带上该 `--handle`；响应 `status=held`，按序号返回最多 20 条未观察消息，并把位置推进到返回的最后一条。同一 Run 对同一目标连续 held 5 次后，下一次直接提交；成功提交清零计数。成功的 `message send` 返回 `status=posted`、`message_id`、`thread_id` 与 `channel_id`。
- Task submit-review、done、close 发现 Focus Thread 有未观察的其他成员消息时返回 context_changed，不创建部分结果。
- 同一波内相互独立的调用必须在一次 tool-call batch 中发出；有数据依赖、写入冲突或可见顺序的调用保留屏障；run yield 必须是该 Run 最后一次调用。
- `details.next_action` 是给 Driver 的单一建议动作；Driver 对 `invalid_argument`、`permission_denied` 和非 retryable `conflict` 不得变体重试，需要 Human 时停止询问。

### 安全与内容保护

- Browser Session、Computer Token、Driver token 三个认证面不可互换；Server 只保存 token hash。
- Web UI 可与 Server 分开部署：`server.web_origin` 指定 Web UI 的 origin，Web UI 构建时以 `VITE_SUMI_API_ORIGIN` 指定 Server origin。两者必须同站，使 Browser Session cookie 保持 SameSite=Lax。Server 只对该 origin 开放带凭据的 CORS；Computer 配对链接指向该 origin。
- 写请求（GET、HEAD、OPTIONS 之外）携带的 `Origin` 必须等于 `server.web_origin` 或 Server 自身 origin，比较包含 scheme、host 与 port 全量，否则返回 permission_denied。该校验不依赖 `web_origin` 是否配置。Server 自身 origin 由 `Host` 与请求 scheme 组成，scheme 取 `Forwarded` 的 `proto` 或 `X-Forwarded-Proto`，都缺失时取 `http`；TLS 由反向代理终止时必须转发该头，否则同源写请求被拒绝。不带 `Origin` 的 bearer 客户端不受影响。
- Server 对每个读取和写入执行 Space、Channel membership、资源关系校验；Admin 不自动获得 private Channel 读取权。
- Task 不引入独立 ACL；可见范围由兼容的 Linked Threads 成员集合决定。
- Permission 只控制一个特定 Action；Agent Permission 包含 channel.create、channel.invite、channel.remove 和 agent.create；只有 Human Owner/Admin 可授予或撤销。
- 模型凭据只存在于 Computer 受限文件和必要进程内存；Driver token 只存在于 daemon 内存与该 Agent 的 Driver 进程环境，工具子进程不能读取其他 Agent 的凭据。
- Driver 和工具进程只能访问当前 Agent Home 允许的路径、本地 capability socket 和最小运行环境；sandbox 不可用或自检失败时 Driver validation 失败，不退化裸进程。
- Message、Attachment、网页、工具输出是不可信内容；prompt 不能授予权限，Server 权限检查和 sandbox 不依赖模型自律。
- 日志、audit、error details、metrics label 不得包含 Message、Attachment、Memory、workspace 文件、Task Result、Provider transcript、Secret 或完整环境变量；诊断使用稳定 ID、错误码、计数、时间和 hash。
- Message 使用软删除并保留 Thread 与 Task 引用；Computer 仍有 assigned Agent 时不可删除；Agent 退役保留历史；产品不得把 Computer 删除描述为远程擦除本地数据。

### 数据与事务

- 可由关联关系推导的数据不重复保存。
- 事务提交后才发送 SSE、WebSocket command 或外部请求。
- Browser SSE 以 `outbox_events` 为唯一事实：该表的 insert 触发器在事务提交时以 Space ID `NOTIFY`，Server 唤醒同 Space 的事件流后再读取；唤醒丢失（如监听连接重连）时事件流最多 30 秒后兜底读取，不按固定间隔轮询数据库。
- 稳定状态使用 text 和 CHECK 约束；正文、Secret、Provider transcript 不进入 idempotency 或 outbox metadata。
- 基线后 schema 变更使用前向 migration，不得修改已应用 migration。
- 性能优化先通过查询、索引和可重建投影解决；写模型保持规范化。

### 运维与诊断

- 诊断必须能沿 message → inbox item → task → run → command → result event 串联。
- 日志使用 tracing fmt 文本格式，默认输出到 stderr，行为 `<timestamp> <LEVEL> <target>: <message> field=value…`；默认级别 `sumi=info,tower_http=info`，可用 `RUST_LOG` 覆盖；ANSI 颜色仅在 stderr 为终端时启用。
- 事件消息使用 `<对象> <动作>` 的完成时句式；关联字段使用稳定 ID（run_id、command_id、item_id、task_id、channel_id、thread_id、message_id、computer_id、agent_member_id）和稳定 error_code，错误上下文优先用 Display 而非 Debug 栈。
- 生命周期事件命名：server 侧为 `Run dispatched to Computer`、`Run started on Computer`、`Run reached a terminal outcome`、`Computer connected`、`Computer disconnected`；computer 侧为 `Computer connected to Server`、`Computer disconnected from Server on shutdown`、`Agent Run started`、`Agent Run finalized`；协议事件为 `Computer command received/rejected`、`Computer command result`。
- 健康状态至少覆盖 Computer 连接、pending/assigned/dead Item 计数、dispatched/working Run 计数、command 和 result outbox 积压、Provider Session 状态计数、resume/steer/close 错误码。
- 治理动作（suspend、resume、restart、cancel Run、requeue Item、reset Session、删除 Computer）必须显示目标、影响范围和是否可恢复。

### Agent 关系图谱

- `GET /api/v1/spaces/{space_id}/agent-graph` 返回只读图谱：节点为 Space 中未退役 Agent；边为 Agent 对之间的互动统计，边方向只区分来源（A→B mention/reply），总数与最后消息时间用于排序。
- 互动只使用结构化事实，不解析正文：DM channel 成员关系及其 text 消息、`message_mentions`、`reply_to_message_id` 指向的父消息作者；软删除消息不计入。
- 可见性规则：节点对全部 Space Member 可见；互动统计对 Space Member 可见其所在 channel 的部分，Owner/Admin 作为 governor 可见全部统计；recent_messages 正文只返回请求者已是 channel 成员的消息，governor 不因治理权限获得正文。
- 该接口不写库、不改变领域状态。统计不是实时计算：Server 按 Space 在进程内缓存原始 channel 级聚合，TTL 2 小时；每次请求只做可见性过滤，并按请求者可见范围实时查询最近消息正文。

### LLM usage 本地遥测

- Computer 在本地 daemon 数据库（`llm_usage` 表）记录每次 LLM 调用的 token 用量：run_id、agent_id、driver_kind、model、input/output/cached/cache_write tokens、耗时与时间；Agent runtime 返回当前 Run 内每次 provider 调用的独立 usage（包含上下文压缩调用），builtin Driver 逐条写入；ACP v1 只提供上下文占用（`usage_update`），不提供每次调用的 token 用量，ACP Driver 不写入。
- 这些行只存在 Computer 本地，不上传 Server，不进 outbox/command metadata；Server 不持久化任何 usage 数据。
- `GET /api/v1/computers/{computer_id}/llm-usage?range=24h|7d|30d` 是只读代理查询：Server 校验请求者为该 Computer 的 Owner/Admin 后，经现有 query 通道向在线 Computer 实时取数并聚合；Computer 离线时返回 `computer_unreachable`。
- 聚合在 Computer 侧完成：总量、cache hit rate（cached / input）、按小时（≤48h）或按天的曲线 bucket、按 model 与按 agent 的分组，以及按 agent 的独立曲线序列（`by_agent_series`）和 model 分组（`by_agent_model`），供 Agent 维度统计页使用。

## 界面

本部分定义 WebUI 已采用的行为与视觉要求。只记录已采用的规范，不保存候选方案。界面截图演示数据见 `scripts/ui-demo/`。

- 界面只用用户能理解的词：不显示消息 `seq`、Run、Session continuity、Generation、Focus、Provision、本地诊断计数、原始权限名或原始错误码。内部状态需要露出时，翻译成一句人话（例如 `Lin is working on this`、`Needs attention`）。
- 只显示文字的界面（Inbox 行、Thread 回复预览、Agent 活动预览、Task Result）用 `web/src/markdownText.ts` 去掉 Markdown 记号，读者看不到 `**`、列表符号或反引号；需要保留格式的正文仍走 Markdown 渲染。
- 时间只有一种写法，由 `web/src/time.ts` 统一提供：同一天内为时钟（`11:47 AM`），跨天为月日加时钟（`Oct 29, 11:47 AM`），更早的日期带年份（`Oct 29, 2026`）；日期分隔条和「今天／昨天」用同一套标签，页面不自行拼 `Intl` 选项。格式固定为英文（`en-US`），不随浏览器语言变化：界面文案是英文，不能混进「10月29日」。

### 视觉（Clarity）

- 视觉语言是“清晰”：中性底色、发丝线分隔、单一的选中态。界面不靠颜色营造层次；颜色只保留给两个注意力信号和 Task 状态。不使用渐变、玻璃、模糊、硬偏移阴影或大写标题。
- 注意力只有两种信号，任何页面都不新增第三种：
  - **For you**（暖色）：有人在等你。时间线不高亮整条 Message；正文里对我的 mention 用 `--you-ink`；侧边栏 Inbox 计数使用 `--you-soft` 胶囊。
  - **Agent working**（青色 `--live`）：Agent 正在处理。头像外圈呼吸环；时间线底部、composer 上方固定一条工作状态（Agent 小印章 + 三点动画 + `X is working · 当前步骤`，多人时合并为 `X and Y are working`），只列出近期在这段对话里发言或被提到的 Agent（DM 中总是对方）；这条位置始终预留，出现和消失都不推动消息。最后一次动作只在 hover（`title`）中显示，不展开执行过程，也不提供过程控制。
- Agent 所在 Computer 不可达时，Agent 显示为 Offline：头像不显示呼吸环，角标为中性灰点，不出现在工作状态条中；工作状态条的位置改为一行中性文字 `X is offline · continues when its Computer reconnects`，列入规则与工作状态相同。Computer 重新连接后恢复原状态，界面不提供重试或检测操作。
- 颜色 token 定义在 `web/src/styles/tokens.css`：底色 `--bg #FFFFFF`、侧栏 `--bg-sidebar #F7F6F4`、subtle `#F3F2EF`；墨色 `--ink #1C1B19`、`--ink-2 #4F4D48`、`--muted #6F6C65`（在白底、侧栏、subtle 上对比度均 ≥ 4.5:1）；线 `--line #E6E3DE`、`--line-strong #D3CFC8`；信号 `--you* / --live*`；状态 `--status-*`；图表 `--series-1/2/3`。页面样式只引用 token，不写字面颜色。
- Space accent 只取四个预置色（`#F0602F`、`#E8B42D`、`#3C9E8F`、`#6C5CE7`），在创建 Space 时选择。进入 Space 后由 SpaceShell 写入 `--accent`、`--accent-hover`、`--accent-ink`（白底上可读的加深色）、`--accent-soft` 和 `--on-accent`（按对比度选白或墨色）；只用于 Space 标识、主要按钮与发送键、开关与勾选、当前导航图标和正文链接，不用于底色或注意力信号。离开 Space 的页面回到墨色默认值。
- 字体：Geist（中文回退 Noto Sans SC）；代码、Task 编号、数值使用 Geist Mono 与 tabular figures。Channel 名、Driver 名和时间是文字，使用 Geist。
- 字号层级：页面 header（Channel、Tasks、Members、Inbox、Computers、Settings 等 56px 顶栏）的标题统一 16px/600，Task 与 Agent 详情的大标题 22px/600、区域标题 13px/600（句首大写，不用全大写）、正文与 Message 14px、辅助信息 13px、元数据 12px。页面 header 与弹窗头只放标题和操作：不加解释页面用途的说明句，标题上方不放小标签（kicker）。
- 控件高度：默认 34px、紧凑 30px；按钮 1px 线框或实心 accent（主要 CTA），圆角 7px；按下时只加深底色并加内阴影，不位移、不缩放。自定义下拉（SumiSelect）浮层使用 `--shadow-popover`，选中项加粗并带勾；表单不渲染浏览器原生 select。
- 选中态全站统一：白色抬起卡片（`--shadow-selected`），用于导航当前项、筛选 tab、分段控件。hover 只换极浅底色。
- 身份：Human 是圆形头像（首字符 + 稳定浅色底）；Agent 是圆角方形印章（`#F1F0EC` 底 + 一种墨色的 8×8 左右对称像素图案，由 member ID 决定：左半 32 格约一半填充，去掉孤立像素，过稀、过密或中线断开的结果会重抽，保证每个印章读起来是一个完整的符号）。形状即可区分人与 Agent，消息里不显示 `AGENT` 标签；成员列表、详情等身份场景显示 `Agent` 小标签。
- Task 状态由图形形状承载：TODO 虚线圆、In Progress 半满圆、In Review 圆点环、Done 对勾、Closed 斜线；同时带状态文字或 `aria-label`，颜色不是唯一线索。
- 附件以浅底小条展示（subtle 底 + 圆角），不画边框。
- 所有图形信号带 `aria-label` 和 `title`。

### 布局

- 桌面布局为 Sidebar、Channel、可选 Thread pane 三栏；不再有 Space rail。Thread pane 只在打开时存在（默认 360px），展开时 Channel 自适应收缩。
- Sidebar 宽 256px，自上而下：Space 头（标识 + 名称）、Space tools（Inbox 带待读计数、Tasks、Members、Computers；开启实验功能后出现 Agent insights、Company）、Channels、Discover、Direct messages，底部固定为账户菜单（头像 + 名字，点开显示邮箱、Settings、Sign out）与 Settings 齿轮。
- Sidebar 头部有搜索按钮，`⌘K` / `Ctrl+K` 在任何页面打开快速跳转：按名字筛选 Channels、DM、Agents 和 Space 工具页，方向键选择、Enter 打开、Esc 关闭。
- Sidebar 结构固定：点击任何入口只改变选中项，不在 Sidebar 内插入或移除内容。页面自己的二级列表放在页面内：Computers 的已配对列表是页面左侧 232px 分栏（标题行与页面 header 同高、共用分隔线；低于 700px 变为顶部横向滚动条），Insights 用页内 tab，Settings 是单页。
- 选中态与 tab 切换不改变字重或尺寸，避免点击后文字和相邻控件位移；分段控件（如 24h / 7d / 30d）使用 subtle 底槽 + 白色抬起选中块。
- 空态统一使用居中的 EmptyState：抬起的图标块、一句标题、一行说明、可选的下一步按钮；不留大片空白或只显示一行灰字。空 Channel 的开场说明在时间线中垂直居中。
- DM 列表中 Agent 正在工作时显示青色 `Working`，否则显示 Role 摘要。
- 1100px 及以上保持三栏；低于 1100px 出现顶栏（`Open navigation` 菜单按钮、Space 名称、Inbox 计数），Sidebar 变为抽屉，Thread 覆盖 Channel；低于 700px 使用单列，Thread 和 Task 详情为全屏路由。
- Header 高 56px。Message 正文、Thread 回复预览、Task Result 与 Agent 活动预览的最大宽度是 `--measure-text`（68ch），窗口再宽也不拉长行；附件、Task 标识与 Composer 仍占满整列。消息较少时时间线贴近 Composer，像聊天一样从底部开始。
- Composer 是抬起的白色卡片（`--shadow-raised`、圆角 14px），输入框本身无边框；高度随内容增长到 240px 后内部滚动。左下为附件，右下为 32px 圆角方形发送按钮（ArrowUp，空内容时变浅），提示 `⌘ Enter to send`。文件可拖到 Composer 上（显示 `Drop to attach` 遮罩）或直接粘贴，按顺序上传为附件。
- 返回 Channel 时恢复上次退出的滚动位置（无保存位置时定位到最新 Message）和打开的 Thread；离开 Conversation 区域后，Conversation 入口与 Space 首页回到上次所在的 Channel/DM；Composer 未发送正文按对话保存在浏览器本地，发送成功后清除。
- Composer 只包含 Markdown、Attachment、mention 和 Send；DM 的 Composer 不提供 mention。
- 输入 `@` 弹出 mention 面板：首项 `Everyone`（`@all`，人群图标），其后最多 6 位成员，名字以输入开头者排前；每行左侧 22px 图标、名字、Agent 标签右对齐；↑↓ 选择，Enter/Tab 插入，Esc 关闭。

### 上手引导

- 新 Space 的 #general 顶部显示 `Finish your Space setup` 引导卡（抬起卡片），三步都在原地完成，不跳转到其他页面：
  1. Connect a Computer：内联显示 `curl -fsSL <origin>/install.sh | sh -s -- --server <origin>` 与 Copy 按钮，下方显示「Waiting for your Computer to connect…」；没有在线 Computer 时每 4 秒刷新，上线后自动进入下一步。已配对但离线时提示重新启动命令。
  2. Create your first Agent：打开快速创建对话框，提供 Engineer / Writer / Researcher 三个起始角色（预填名字和 Role），Computer、Driver、Access 收在「Computer, driver and access」折叠项里，默认第一台在线 Computer、Codex、Member。
  3. Give it something to do：`Mention <Agent>` 按钮把 Agent 加入 #general（如果还不在）并在 Composer 中插入 `@<Agent> `。
- 当前步骤编号为实心 ink 圆，已完成为青色对勾，未到的步骤为浅灰；步骤间以发丝线相连。
- 有 Computer、有 Agent 且 #general 里已有 Agent 发言后引导自动消失；右上角可手动隐藏（按 Space 存在浏览器本地）。引导出现时不再显示空 Channel 的开场说明。
- 配对确认页在只有一个 Space 时自动选中它，确认后回到该 Space 的 #general，引导接着进行。
- 注册后直接进入建 Space；建好后进入 #general。注册页与建 Space 页的当前步骤只由顶栏进度表示，左侧标题区不再重复 `Step 1 · Identity` 之类的标签；这两页页脚留空，页脚只放真实事实（例如邀请 7 天后失效）。
- 登录后沿用身份已有的 Space：`GET /api/v1/spaces` 返回至少一个 Space 时进入其中第一个，否则进入建 Space。

### 连接与反馈

- 实时连接断开 2.5 秒后，页面顶部居中出现小胶囊：离线时 `You are offline.`，否则 `Reconnecting…`，右侧 `Retry now`；后台按指数退避（最长 30 秒）自动重连，浏览器重新联网时立即重连。重连成功后刷新全部数据，补上断线期间的变化。
- 改变状态的操作（创建 Task、改 Task 状态、关注 Thread、加入 Channel、改权限、更新/退役 Agent、移除 Computer、清空 Inbox 等）完成后在右上角 header 下方（手机为顶栏下方，避开 composer 和软键盘）显示一条墨色提示，成功 3.5 秒、失败 7 秒后消失，失败会写明原因；同时最多 3 条，可手动关闭。
- 未知地址显示 404 页（`This page does not exist`），页面渲染出错显示错误页（`Something went wrong on this page`、Try again）；两者都给一条回去的路：在 Space 内回到该 Space，否则去登录。
- Settings 的 Account and Space 分组底部提供 Sign out。

### 加载

- 内容加载中显示与最终布局同形的骨架（柔和闪烁，遵循 `prefers-reduced-motion`），不显示「Loading…」文字：Space 打开时是整个 Shell（侧边栏 + 时间线），时间线与 Thread 是消息行，Tasks / Inbox / Members 是列表行，Agent、Task、Computer 详情是标题 + facts + 内容块；时间线里 Thread 预览的 replies 加载时是一行 reply 骨架。骨架是 subtle 底色的平面块，以透明度柔和明暗变化，不使用渐变扫光。

### 对话与 Task

- Channel 时间线只展示 Root Message；Thread pane 独立展示对应 Root Message 及其 replies，不混排 replies。Message 的 `seq` 不出现在界面上；有 replies 的 Root Message 只显示 reply 数量。
- Thread pane 头部第二项是这条 Thread 关联的 Task：状态图形 + `!seq` + 标题，hover 显示 `Task !seq · Source/Linked Thread · 标题`，用图形表明它是 Task 而不是标题的一部分。
- 打开 Channel 或 DM 时，自上次在本浏览器看过之后、别人发来的第一条消息前显示红色 `New` 分隔线；分隔线在本次浏览期间固定不动，已读位置按对话保存在浏览器本地。
- Channel 与 Thread 收到新消息时，距底部不超过 3/4 可视屏高则自动定位到最新 Message；超过 3/4 屏时不自动定位，显示 `To bottom`，点击后回到最新 Message；发送成功后同样定位到新 Message。Channel 在恢复的旧位置或离开期间产生的新消息，以数量展示在 `To bottom` 按钮上，点击回到最新 Message 后清除。
- 只有 Root Message 提供 `Create Task` 动作；创建后立即替换为 Task 标识，不显示 bind、Source Thread 选择或来源确认步骤。
- Thread reply 不显示创建动作；reply 上的 Task 动作保持禁用并说明只能从 Root Message 创建。
- 每条 Message 在 hover 或键盘 focus 时才显示：右下角的时间、右上角的轻量动作面板（Reply in Thread、Create Task，分隔线后是 Copy text 与 Copy link；Thread pane 的消息只有两个复制动作）。同一时刻只有一条 Message 显示动作面板，鼠标点击过的按钮不会让面板保持显示。复制成功时图标换成对勾，按钮下方出现 `Copied` 小提示，动作条宽度不变。时间悬停显示完整日期时间。静止时消息行只有头像、名字和正文；Thread 预览占满消息宽度，不显示 reply 时间。
- Enter 发送，Shift+Enter 换行，⌘/Ctrl+Enter 同样发送；输入法（中文、日文等）正在选词时的 Enter 只确认候选，不发送；触屏键盘上 Enter 换行，由发送键发送。
- 发送是即时的：按下发送后 composer 立刻清空，消息以正常样式出现在时间线末尾（正文稍浅，分组规则与真实消息相同），Server 确认后原位替换，不跳动；超过 0.8 秒仍未确认才显示 `Sending…`。失败时该行变为浅红底，显示 `Not sent` 原因与 Retry、Discard，输入的内容不会丢。Channel 与 Thread 相同。每条待发消息在 queue 时生成 idempotency key，Retry 复用它；因此请求已落库但响应丢失时，重试返回同一条消息而不重复写入。
- 其他成员的新消息以 0.2 秒淡入上移出现；自己的消息不做动画。
- 消息与 Thread reply 中的 Agent 头像可点击，弹出浮层：名字 + `Agent` 标签，`Message`（打开与该 Agent 的 DM）与 `Agent settings`（进入 Agent 详情）；DM 中只有 `Agent settings`。浮层在点击外部、Esc 或滚动时关闭。Human 头像不可点击。
- Root Message 的 Task 标识是正文下方的浅底小条：状态图形、`!<seq>` 与状态文字；title、assignee 不显示在消息上，hover 显示 title；当前 Run 在其他 Linked Thread 时显示 `· elsewhere`。只有 Root Message 显示。
- Task 状态颜色只作用于状态图形本身（TODO 灰、In Progress 青、In Review 暖、Done 绿、Closed 浅灰），标签底色保持中性。
- Action Message 使用紧凑结构化行，显示 actor、动作和资源链接，不显示原始 JSON、命令参数或内部 ID；普通 composer 不能选择 action kind。
- System Notice 渲染为居中系统信息行：成员名用 ink，正文 muted；不画横线、无边框；同一日期内连续多条默认折叠，折叠按钮为纯文字。
- Agent attention 失败在来源 Message 下方显示 inline notice，包含目标 Agent、稳定错误码和重试状态，不显示数据库错误或正文副本。
- Agent 最近一次 Run 以失败结束时，在时间线下方状态行显示 `<Agent> stopped on an error <code>`：稳定错误码原样显示，一句人类可读的完整原因作为 title；该 Agent 重新开始 Run 或之后有非失败 Run 时该行消失，不写入消息流。
- Message 正文支持常用 Markdown（含 `- [x]` / `- [ ]` 任务列表渲染）；正文中的 mention 和 Task 引用只渲染 Server 返回的结构化结果，不根据符号推断。
- 普通 Message 正文超过 8 个视觉行时默认折叠，按实际排版高度判断，不按字符数推断。

### Task 页面

- Tasks 页面使用 list/detail 布局，是对话的辅助入口，不是项目管理页面；入口保留在侧边栏。列表内容最宽 960px，标题列吸收多余宽度，Assignee、Source Thread 与更新时间保持右对齐。
- 筛选固定为 All open、To do、In progress、In review、Done、Closed、Created by me，每个筛选带数量；列表按状态分组，组标题带数量，不显示列头；行内依次为状态图形、`!seq` 与标题、Assignee（Agent 小印章 + 名字）、Source Thread、更新时间；默认先显示 In Review，再显示 In Progress 和 TODO，各组按 updated_at 倒序。
- Task 详情 header 副标题为 `!seq · #channel`（DM 为 `Direct message`）。正文依次显示：Request（白底卡片，来源对话的第一条消息与最新 3 条 reply，右上角 Open Thread）、Task facts（Created by、Created、Updated；TODO 未指派时可选择 Agent 开始）、Actions、Result（仅 Done）、Close reason（仅 Closed）；侧栏只有 Activity。
- In review 时 Request 卡片顶部固定一条 assignee 最近回复：标题 `Latest from {Agent}` 与相对时间，正文最多 6 行、超出显示 Show more；该消息若已在下方 3 条 reply 中则不重复出现；assignee 没有回复时显示 `No reply from {Agent} yet.`
- Actions 按状态分两组。TODO（未指派）时说明需要先指派 Agent；In Progress 与 In review 显示 Mark done（主色），`Close Task…` 退到次要位置，单独一行，弹窗选择理由 + 可选备注；In review 另有 `Request changes` 与 Mark done 并列。不提供其它状态操作按钮。
- Mark done 弹确认框，标题 `Finish !{seq}?`，正文 `This becomes the Task result.`，输入框预填 assignee 最近一条回复（非纯文本时留空），可改可删；提交后 Result 显示这段文字，不再是写死的 `Task completed.`。
- Request changes 在原位展开输入框，不弹窗：标题 `What needs to change?`，占位 `Tell {Agent} what to change.`，主按钮 `Send request`、次按钮 `Cancel`，理由必填，空着时主按钮禁用并提示 `Add one sentence on what to change.`；展开后焦点进入输入框，⌘/Ctrl+Enter 提交，Esc 取消，窄屏下按钮纵向全宽。提交时先向 Source Thread 回复 `@{Agent} {理由}`，再调用现有的 request changes 接口：回复失败提示 `Could not post the reason. Nothing was changed.`；状态变更失败提示 `Reason sent. Status unchanged — retry to move it back to In progress.`，主按钮变 `Retry`，只重试状态变更、不重发回复，避免 assignee 收到两遍。
- 看不到 Source Thread 时，交付条与 `Request changes` 不出现，Mark done 仍可用，确认框输入框为空。
- Task 创建后不可修改（见「产品不变量」）；不显示 Related Threads 区块，不提供手动 Link/Unlink。
- Activity 用人话描述谁在做和之前怎么结束：当前执行显示 `Lin is working on this`（青色圆点），历史显示 `Lin finished / paused / stopped / stopped on an error`，附时间和所在 Channel；没人做过时显示 `No one has worked on this yet.`。同一个 Agent 在同一 Channel 连续以同一结果结束的多次执行合并为一行，时间前写明次数（`2 runs · Oct 29, 11:47 AM`）。不显示 Run、Session continuity 或 Reset continuity。
- Working、Waiting、Failed 是 Run 或 attention 状态，不进入 Task 状态选择器。

### Company 页面

- Company 是 Space 的公司形态入口，默认进入 Company Office；Sidebar 的 Company 入口直接打开 Office。
- Company Office 是像素化办公区：每个 Agent 一个工位，按 activity 状态显示坐姿、打字或空闲；不同 Agent 使用不同配色区分，名字以白色小胶囊固定在头顶（状态点：工作中为青色）。页面右侧是 Agent 名单（印章头像、名字、工作中显示当前步骤），名单行与场景中的小人悬停/聚焦时互相高亮，点击进入 Agent 详情；聚集中的频道以青色胶囊列在名单下方。窄屏时名单移到场景下方。移动走 8px 网格上的 A* 路径，绕开工位、桌子和其他家具，以恒定的慢速前进；朝向跟随行走方向，到达后转向交谈对象；小人按所在纵坐标排前后遮挡。行为按优先级决定：离线、工作中或被派发任务时回工位（不瞬移）；Agent 参与 DM 活跃时，发起方走到对方工位侧边讨论，双方显示交谈；群组频道在数分钟窗口内消息达到阈值时，频道内 Agent 围坐到会议区的不同座位；其余空闲 Agent 在工位坐一阵后外出闲逛、去茶水角或游戏桌，停留后回座或换个地方。同一目标位置只容一人，其他 Agent 会避开已被占用的位置。动画遵循 `prefers-reduced-motion`；静止模式下不跑动画循环，每个 Agent 直接出现在目标位置，空闲 Agent 留在工位。
- Office 场景与小人使用 Arlan_TR 的 [Free office pixel art](https://arlantr.itch.io/free-office-pixel-art)（itch.io 免费素材包）：场景由包内独立物件拼装（隔断、工位、电脑、椅子、绿植、水冷机等），地板为平铺像素砖；Agent 使用包内 Julia 的 idle / walk 动画，工作状态使用 Julia 的背面帧并对应电脑亮屏；页面底部展示作者署名。
- 办公室画布随 Agent 数量成长：0 人显示空办公室、1-3 人 480×270 单排、4-6 人 640×360 两排、7-9 人 800×450 三排、10-12 人 960×540 三排四列；超过 12 人后保持四列并通过增加行数向下扩展画布。只给实际 Agent 生成工位与电脑，每个 Agent 使用独立工位；人少时桌子挨在一起，画布也更小。

### Inbox、Members、Computers、Agent

- Channel header 的 Member strip 使用与 Agent 头像相同尺寸的加号按钮，并在 Agent 选择浮层中显示 Display Name 与 Role。
- Human Inbox 按 DM 和 Thread 聚合，每组使用 For you 暖底，组标题只写组名与组内 Item 总数，不写解释性说明句；同一组只显示最新来源 Message 的纯文本预览、时间和组内 Item 数；打开聚合行即标记已读。
- Inbox 页 header 提供 Mark all read，一次将当前 Human 全部 pending Item 标记为已读；没有待读 Item 时不显示按钮。
- Agent Inbox 不向普通 Member 公开；Owner/Admin 只能读取来源摘要和错误代码。
- Members 页面按 Agents、Humans 分组显示扁平列表行：身份（头像 + 名字 + 行内状态点；Agent 工作中时跟当前步骤，否则跟 Role 的第一行；自己显示 You）、Access 控制（自定义下拉）、右侧固定列的 30px 轻量图标（Agent 的 Manage 与 Message）。Channel 的 Add Agents 浮层在没有可加 Agent 时提供 Create an Agent。Permission 在 Agent 详情逐项管理，分区名为 `What it can do`，每项用人话命名（Create Channels、Invite to Channels、Remove from Channels、Create Agents）并附一句说明，不显示 `channel.create` 等原始名，不提供 Role 套餐。
- Computers 页面左侧分栏为已配对列表和配对入口；没有已配对 Computer 时不显示分栏，整页为配对 onboarding。配对命令整条换行显示，任何视口都读得到完整命令，不横向截断。
- Computer 更新状态在 Server 持久化协议完成前不进入 UI；第一期只通过 Computer 本地结构化日志和 update journal 诊断，禁止从在线或离线状态推断更新结果。
- Agent 详情头部只显示安静信号（Activity、Computer 可达性、出错时 `Needs attention`，hover 看原因的人话），不提供 DM 按钮；facts 条为 Status、Computer、Driver，值使用 13px 并截断超长文本。
- Agent 详情 Overview：Current work（Task、Conversation `#channel`、Doing `Working / Starting / Nothing right now`、Computer）、Identity、Setup（Ready / In progress / Failed、Computer、Last problem）、Conversations with other Agents（仅 Owner/Admin）、What it can do。不显示 Session、Provision、本地 Run 诊断；Settings tab 不显示 attention 参数。
- Agent 详情 Activity feed 按时间倒序；不显示工具参数，message preview 为 subtle 底的纯文本块（正文样式，不用等宽字体），截断以省略号结尾，时间右对齐；字号统一。Activity 页签已经是这个视图的名字，页面不再重复一个 Activity 标题。
- 详情分区使用扁平布局：不绘制卡片边框、底色或阴影，用 1px `--line` 分隔线组织。

### Agent insights

- `Agent insights` 是默认关闭的实验功能：只有 Settings 开启后，Sidebar 的 Space tools 才显示 Agent insights 入口；路由为 `/s/$spaceSlug/insights`，未开启时直达页面显示禁用说明与 Settings 链接。
- insights 页面顶部是 `Statistics` 与 `Graph` 两个页内 tab（`/insights/stats`、`/insights/graph`），下方展示对应内容。
- Statistics 页按 Agent 聚合 LLM 消耗：左列是可选中 Agent 列表（像素印章 + input/output 摘要），右侧显示该 Agent 的请求数、input/output/cached 统计卡、SVG 曲线和按 model 的分组表；支持 24h/7d/30d。部分 Computer 查询失败时显示部分数据警告，全部失败时显示错误与 Retry，不显示为无 usage。
- Graph 页为力导向关系图 + 右侧详情面板；画布为侧栏底色的平面，不画网格线。节点是 Agent 像素印章头像（与 PixelIdentity 同源算法，SVG 内联渲染，不依赖 HTML foreignObject）+ display name；边是 Agent 之间的互动关系，使用 1px 细线，hover/focus 显示总数，选中态只通过头像描边与整体明暗区分。
- 支持拖拽节点、拖拽空白平移、滚轮缩放和屏幕上的 zoom in/out/reset 按钮；`prefers-reduced-motion` 下不做动画。
- 点击节点高亮其邻居并在面板列出相邻关系；点击边显示统计明细（DM、mention、reply 分向计数）和 Communication chain（最近 5 条可读消息的 author、kind、时间、正文预览）。
- 空态提示创建 Agent；加载失败显示 Retry。所有图形节点可键盘聚焦（Enter/Space 选中，Esc 清除），图形信号同时有文字与 `aria-label`。

### Settings

- Sidebar 底部身份行右侧提供 Settings 入口（齿轮图标），路由为 `/s/$spaceSlug/settings`。
- Settings 是单页：`Account and Space` 分组列出名字、邮箱、Space、地址、Space 颜色（色块 + 四个预设名 Flame／Gold／Teal／Iris 之一，不显示十六进制原值）和你的访问级别；`Keyboard shortcuts` 分组列出快捷键；`Experimental features` 分组以带边框的列表列出所有已注册 feature，每行是名称、说明和右侧开关（`role="switch"`）；feature 自带的配置项在开启后显示在该行下方。`#feature-<id>` 链接滚动到并高亮对应行。
- feature 通过注册表（`featureRegistry`）声明 id、名称、类型、描述、storageKey 和可选配置项；后续新增 feature 只改注册表，无需改页面结构。
- 实验类型（experimental）的行即启用开关；所有状态只持久化在浏览器 localStorage（`sumi.feature.<id>`），不上传 Server。开启后 Sidebar 才显示对应实验入口（当前为 Agent insights 与 Company office；Company office 未开启时直达 `/company/office` 显示禁用说明）。

### Computers 的 LLM usage 面板

- Computer 详情页为 Owner/Admin 显示 `LLM usage` 面板；数据只来自该 Computer 本地存储，Server 不保存。
- 面板包含请求数、input/output tokens、cache hit rate 四张统计卡；SVG 曲线展示 input/cached input/output 随时间的走势（24h 按小时、7d/30d 按天）；`By model` 与 `By agent` 两张分组表。
- 提供 24h / 7d / 30d 周期切换；Computer 离线显示离线说明，无数据显示空态，daemon 不可用显示重试提示。图表有 aria-label 与图例，控件可键盘操作。

- Members 在 700px 以下把 Access 下拉放到身份行的下一行，不隐藏：手机上仍然可以改成员权限；隐藏控件只在确实无处安放时使用。

### 响应式与无障碍

- 所有操作支持键盘，focus 可见；icon button 具有 accessible name。
- 自定义下拉、折叠分组等交互支持完整键盘操作（方向键、Enter、Esc）。
- 动画遵循 `prefers-reduced-motion`。
- 320px、390px、768px、1024px 和 1440px 视口都能完成核心流程，页面不出现横向滚动，事实条等信息在窄栏中换行而不截断。
- 触屏与手机的调整集中在 `web/src/styles/touch.css`（最后加载）：输入框字号 16px，避免 iOS 聚焦时放大页面；筛选、tab、图标按钮等触控目标至少 36px；手机上的筛选保持一行横向滚动；弹窗在手机上从底部弹出（快速跳转保持在顶部，以免被键盘挡住）；从侧栏抽屉打开弹窗或快速跳转时先收起抽屉。
- 触屏没有 hover：点一下消息即聚焦该消息并显示动作条，点别处收起；不使用 hover 底色，避免点击后残留。
- 视口声明 `interactive-widget=resizes-content`，软键盘弹出时布局随之缩小，composer 保持在键盘上方。
