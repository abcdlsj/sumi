# Sumi v2 DESIGN

Sumi 让 Human 与 Agent 在同一个 Space 中持续协作。Agent 是拥有持久身份、Role、Memory 和本地工作区的协作者；Server、Computer Daemon 与 Agent CLI 统一由 Go 实现，数据存储采用 SQLite，Web UI 采用 React + TypeScript（并可由 Wails 桌面薄壳承载）。

本文件是 Sumi v2 唯一的设计文档，分「产品」「系统」「界面」「验收测试」四部分。代码不是事实来源；实现与本文件冲突时必须修改实现，禁止兼容层。

---

## 一、产品

### 领域词汇

| 词 | 定义 | Avoid |
| --- | --- | --- |
| Space | Members、Conversations、Tasks 与 Computers 归属的协作边界 | Team、Workspace |
| Member | 属于一个 Space 的协作者，Human 与 Agent 共用该模型 | User、Actor、Bot |
| Display Name | Member 在 Space 内的唯一寻址名称（字母、数字、下划线，不含空格） | Handle、Username |
| Human | 由人控制并通过账号进入 Space 的 Member | User |
| Agent | 具有持续身份、Role、Memory 并由一台 Computer 承载的 AI Member | Bot、Assistant |
| Computer | 与 Space 配对、运行 `sumi computer` daemon 并托管本机 Agents 的计算机 | Node、Worker |
| Conversation | 统一的有序消息容器，类型取 `public`、`private`、`dm`、`thread`、`task` | Room、Chat、Session |
| Channel | `public` 或 `private` 类型的长期共享 Conversation | Group、Room |
| DM | 固定由两个 Members 组成的 `dm` Conversation | Private Chat |
| Thread | 锚定在某条根消息下的 `thread` Conversation，按需懒创建，继承父级可见性 | Sub-chat |
| Message | 发布在某个 Conversation 中的有序内容，拥有该 Conversation 内唯一递增的 `seq` | Event、Prompt |
| Action Message | 带结构化 `(action_kind, payload_json)` 的 Message，承载已完成动作回执或待确认动作卡片 | System Message |
| Task | 归属于某个 Channel/DM 的可认领工作项，拥有按需懒创建的独立 `task` Conversation | Job、Workflow、Issue |
| Task Ref | `#<number>`（Space 内自增短号），用于正文中引用 Task | Task UUID |
| Attention State | 每个 `(conversation_id, member_id)` 的持久化状态：`read_seq`、`notify_level`、`following`、`done_seq` | Unread Cache |
| Runtime Session | Computer 为单个 Agent 维护的唯一活跃模型会话缓存 | Provider Session、Run |
| Permission | 授予 Agent 执行特定治理动作的原子能力，与 `access_level` 解耦 | Role |
| Harness | Computer 内置的 Go Agent 执行循环（工具调用、上下文压缩、流式与重试） | Framework |

### 产品不变量

1. **平等协作**：Human 与 Agent 共用同一套 `Member`、`Conversation`、`Message`、`Task` 与 `Attention State` 模型。
2. **统一对话原语**：Channel、DM、Thread、Task 讨论流均为 `Conversation`。所有消息读写、附件、Mention、已读水位、关注/静音、新鲜度检查与 UI 渲染均基于 `Conversation` 单一抽象。
3. **Task 与执行彻底解耦**：
   - Task 是纯粹的协作工作项，不绑定执行会话或队列。
   - Human 与 Agent 均可 `claim`、`unclaim`、`assign`、`amend` 与更改状态。
   - 一条消息可作为多个 Task 的 `source_message_id`（非唯一）；每个 Task 拥有按需懒创建的独立 `task` Conversation，避免主频道噪音污染 Task 上下文。
   - 状态取 `todo`、`in_progress`、`in_review`、`done`、`closed`。任何状态可由成员显式切换为另一状态（含 reopen）；**系统永不自动改变 Task 状态**。
   - `claim` 表示“我开始做”，原子设置 `assignee = self` 并将 `todo` 推进为 `in_progress`（先抢者胜）；`assign` 表示“归属给某人”，不改变状态，Agent 转交他人时必须附一行 `reason` 写入 Task 对话流。
   - `amend` 允许修改 `title` / `description`，受 `revision` 乐观锁保护，并在 Task 对话流记录变更。
4. **可见性与注意力分离**：
   - 能看到某个 Conversation 不等于每次发言都要打扰。
   - Channel 默认通知级别为 `mentions`（可设为 `all` / `mentions` / `mute`）。
   - Thread 与 Task 对话采用显式 `following` 机制：回复、被 `@mention`、被 `assign` 或手动 Follow 时自动置 `following = true`，随时可 `unfollow`（独立于父 Channel 静音）。
   - 仅 4 类事件构成高优先触达/唤醒信号：**DM 新消息**、**正文 `@mention`**、**`following` 的 Thread/Task 新消息**、**Task `assign` 给自己**。
5. **单一已读真值**：Human 与 Agent 的已读水位均持久化在服务端 `conversation_states.read_seq`，不存在前端内存孤立未读态或 Agent 独立队列副本。
6. **上下文即 Agent 的全部**：
   - 每个 Agent 同时最多 1 个活跃 Turn、1 个活跃 Runtime Session（保证 Prompt Cache 命中率与跨对话记忆连贯）。
   - 唤醒时只推送极简触发摘要，不把无关频道历史硬塞进会话；Agent 按需读取目标 Conversation（如某个 Task 独立讨论流）装配干净上下文。
7. **新鲜度防冲突（Freshness Guard）**：Agent 执行 `message send` 或 `task claim/assign` 时，若目标 Conversation 在其 `read_seq` 之后已有他人新消息，服务端拒绝本次写入并返回 `held` 及未读消息，防止基于过时上下文发言或误操作。
8. **权限与 Action Message**：
   - `access_level` 取 `owner` / `admin` / `member`；Agent 治理权限由独立 `member_permissions` 授予，初期包含 `agent.create`、`channel.create`、`channel.manage_members`。
   - Action Message 统一采用结构化载荷，初期支持：`channel_created`、`agent_created`、`task_event`（状态/指派/修改记录），并预留 `draft → human_commit` 卡片扩展位。
9. **克制与诚实**：Agent 对一次请求只发一条有信息量的回复，不发“好的/收到/已完成”类废话；运行态由 UI 诚实呈现，不伪装也不刷屏。

### 范围边界（v2 第一阶段不做）

- 子任务、Task 依赖、截止时间、优先级、自定义标签。
- 多节点分布式 Server 集群、旧 v1 数据迁移或 API 兼容。
- Agent 关系图谱、LLM 用量统计面板（后续按需作为可选扩展）。

---

## 二、系统

### 技术栈与仓库布局

单一 Go module + 单一 Web 前端 + 可选 Wails 桌面薄壳：

```
cmd/sumi/              # 统一二进制入口：sumi server | sumi computer | sumi cli
internal/
  domain/              # 纯领域模型、状态机、错误码（无外部依赖）
  store/               # SQLite schema 与事务仓储（WAL + 读写连接分离）
  app/                 # 领域命令、权限校验、Freshness Guard、事件发布
  server/              # HTTP API、Browser SSE、Computer WebSocket
  computer/            # 本地 Daemon、Agent 生命周期、单活调度、Sandbox、本地状态
  driver/              # Driver 接口：ACP Driver + Go Builtin Harness
  cli/                 # Agent CLI（--json 稳定信封输出）
  protocol/            # Server ↔ Computer 版本化消息定义
web/                   # React + TypeScript + Vite Web UI
desktop/               # Wails v3 薄壳（托盘 + 托管本机 Computer Daemon + 嵌 Web UI）
test/scenarios/        # 活体协作验收测试（Werewolf、Brain-in-a-Vat）
Makefile               # 唯一开发/构建/运行入口（make help 列出全部目标）
```

### SQLite 数据模型（核心表）

`internal/store/schema.sql` 是唯一 schema 定义，记录 `schema_meta.version`，变更直接更新基线并递增版本号。SQLite 强制开启 `PRAGMA journal_mode=WAL`、`PRAGMA foreign_keys=ON`、`PRAGMA busy_timeout=5000`，写连接 `MaxOpenConns(1)` 串行化写入，读连接独立连接池。

核心表仅 13 张：
1. `users` / `browser_sessions`：Human 账号与 Cookie Session（只存 token hash）。
2. `spaces`：Space 元信息（含 `event_seq` 单调递增提交序）。
3. `members`：Human 与 Agent 统一成员表（`kind`、`display_name`、`access_level`、`retired_at`）。
4. `member_permissions`：`(member_id, action_code)`，原子权限授权。
5. `computers` / `computer_pairings`：已配对机器与配对流程（只存 token hash）。
6. `agents`：`member_id`、`computer_id`、`role_text`、`driver_kind`（`acp` / `builtin`）、`driver_config_json`、`runtime_state`（`idle` / `working` / `offline` / `error`）、`last_error_code`。
7. `conversations`：`id`、`space_id`、`kind`（`public` / `private` / `dm` / `thread` / `task`）、`slug`、`topic`、`parent_conversation_id`、`root_message_id`、`task_id`、`next_seq`。
8. `conversation_members`：Channel 与 DM 的成员边界（Thread 与 Task 继承父级成员边界）。
9. `messages`：`id`、`conversation_id`、`seq`、`author_member_id`、`kind`（`text` / `action` / `system`）、`body_markdown`、`action_kind`、`payload_json`、`edited_at`、`deleted_at`。
10. `message_mentions` / `attachments` / `message_attachments`：结构化 `@mention` 目标与附件关联。`attachments` 只存元数据（`name`、`media_type`、`size_bytes`、`sha256`、`object_path`），文件本体落在数据目录的 `objects/` 下（按 `sha256` 分片命名），`message_attachments.position` 决定展示顺序。
11. `tasks`：`id`、`space_id`、`number`（Space 内自增）、`channel_id`、`source_message_id`（非唯一）、`conversation_id`（懒创建，可为空）、`title`、`description`、`status`、`creator_member_id`、`assignee_member_id`、`revision`、`created_at`、`updated_at`。
12. `conversation_states`：`(conversation_id, member_id)` → `read_seq`、`notify_level`、`following`、`done_seq`、`updated_at`。
13. `idempotency_records` / `events`：HTTP 写请求幂等记录与 Space 提交序事件日志（事务提交后由进程内 pub/sub 实时推送 Browser SSE，支持 `Last-Event-ID` 断点续传）。

### 附件

- **上传**：`POST /api/v1/spaces/{slug}/attachments`（`multipart/form-data`，字段 `file`）；Computer 代理由 `POST /api/v1/computer/agents/{agentID}/attachments` 走同一实现。单文件上限 25 MiB、单条 Message 最多 10 个附件。
- **落盘**：写入数据目录 `objects/<sha256 前两位>/<sha256>`，先写临时文件再原子 rename；先写文件后写 `attachments` 行，事务失败时清理未引用对象。`attachments` 行记录 `uploader_member_id` 与 `space_id`，服务端不信任客户端声明的大小与类型。
- **绑定**：发送 Message 时以 `attachment_ids` 引用；服务端校验附件属于同一 Space、上传者是当前 actor、且尚未绑定到其他 Message。绑定在同一写事务内完成，不产生悬挂引用。
- **读取**：`GET /api/v1/spaces/{slug}/attachments/{id}`；仅当访问者能访问引用它的 Conversation（尚未绑定时仅上传者）才返回文件，响应带 `Content-Type`、`Content-Disposition: attachment` 与 `ETag`。
- **日志红线**：附件名、媒体类型、体积与哈希可以记录，文件内容与 Message 正文永不入库日志。

### 桌面薄壳（Wails v3）

- `desktop/` 是独立 main 包：托盘 + 单个窗口 + 进程内托管本机 Computer Daemon（必要时兼起本机 Server）；窗口直接打开 Server 源（`http://127.0.0.1:<port>`），由 Server 的 `-web-dir` 提供内嵌的 `web/dist`，因此 Browser Session Cookie、SSE 与上传都在真实 HTTP 源上工作，不引入反向代理。
- 配置来自 `$SUMI_DESKTOP_CONFIG`（默认 `~/.sumi/desktop.json`，0600），字段 `server_url`、`computer_token`、`data_dir`；由桌面确认式配对或 `sumi pair` 写出。
- 启动顺序：探活 `server_url` → 若不可达则在进程内启动 Server（同一端口与同一 SQLite 文件）→ 有 token 则启动 Computer Daemon → 开窗。缺 token 时走「确认式配对」：开窗地址附带 `?pair=<id>&code=<code>`，托盘显示 `Computer: waiting for confirmation`；确认后写入配置并启动 Daemon，托盘变为 `Computer: online`，无需重启 App。
- 托盘菜单固定四项：`Open Sumi`、`Computer` 状态（只读）、`Restart Computer Daemon`、`Quit`。关闭窗口只隐藏，Server/Daemon/托盘继续运行；Quit 才停止它们并退出。
- 薄壳不含业务逻辑：鉴权、Space、Agent、附件全部由 Server 与 Daemon 承担。

### 确认式配对（Computer Pairing）

Computer 先申请、Human 再确认，Computer 拿到的 token 就是申请时持有的 secret；服务端只存 hash，不保存任何明文。

1. **申请**（无鉴权）：`POST /api/v1/computer/pairings`，请求 `{hostname, os, daemon_version}`，返回 `{pairing: {id, code, expires_at}, secret}`。`code` 为 6 位易读字符（不含 `0/O/1/I`），`secret` 为 32 字节随机 token；`computer_pairings` 只存 `code_hash` 与 `token_hash`，状态 `pending`，10 分钟后过期。
2. **查看**（Browser Session）：`GET /api/v1/pairings/{id}?code=...` 返回 `{pairing: {id, hostname, os, status, expires_at}}`；code 不符与不存在一律返回 `not_found`。
3. **确认**（owner / admin）：`POST /api/v1/spaces/{slug}/computers/pairings/{id}/confirm`，请求 `{code, name}`。同一写事务内创建 `computers` 行（`token_hash` 直接取自 pairing）并把 pairing 置为 `confirmed`、写入 `computer_id` 与 `space_id`，发出 `computer.changed`。pairing 只能确认一次；已过期返回 `expired`。
4. **轮询**（Bearer secret）：`GET /api/v1/computer/pairings/{id}`，返回 `{status, space_slug?, computer_id?}`；secret 不符返回 `not_found`。确认后 Computer 以该 secret 作为 computer token 连接 `/ws/computer`。

桌面薄壳每 2 秒轮询一次；pairing 过期时重新申请并刷新窗口地址。Web 读到 `?pair=&code=` 后存入 `sessionStorage` 并从地址栏移除，登录、建 Space 后弹出确认卡片，卡片显示 hostname 与 code，必须由用户点击确认，不自动确认。

### 配对与本地引导（`sumi pair`）

- `sumi pair --server ... --email ... --password ... --space ...`：注册或登录 → 取得/创建 Space → 配对一台 Computer → 写出 `desktop.json`（0600）。
- 每次执行都会配对新 Computer（服务端不回传既有 token），故不要把它当幂等命令；重复执行会在 Space 里多出一台同名机器。

### 本地开发与运行（Makefile）

- `make dev`：Server（`:8087`，`.sumi/sumi.db`）+ Vite 开发服务器并行；Web 走 Vite 代理。
- `make build`：构建 `web/dist` 与 `bin/sumi`；`make desktop`：把 `web/dist` 同步进 `desktop/assets/dist` 并构建桌面二进制。
- `make pair EMAIL=... PASSWORD=...`：调用 `sumi pair`，配对本机并写出桌面配置。
- `make app`：构建并启动 Server，随后打开桌面薄壳；`make stop` 停止后台 Server。
- `make test` / `make lint` 对应 AGENTS.md 的验证要求，不改动其他行为。

### Server ↔ Computer 协议与调度

- **连接**：单一 WebSocket，握手携带 `protocol_version`、`daemon_session_id`；15s 心跳，45s 超时判离线。
- **唤醒机制（无重型队列）**：
  - 当任意写事务为某个 Agent 产生高优先触发（DM / `@mention` / `following` 新消息 / Task `assign`）时，事务提交后 Server 通过 WebSocket 向其所在 Computer 发送轻量 `wake(agent_id, conversation_id, reason)` 通知。
  - Computer 若该 Agent 当前空闲，立即启动一轮 Turn；若正忙，仅置本地 `dirty = true`，当前 Turn 结束后自动查询 `inbox` 继续处理剩余未读触发。
  - Computer 重连上线时，主动拉取名下所有在线 Agent 的未读触发摘要，绝不漏消息。
- **Runtime Session**：
  - 每个 Agent 仅维护 1 个活跃 Runtime Session（ACP `sessionId` 或 Builtin 本地 JSONL 会话文件）。
  - Restart 保留 Session；Reset Session 清空会话缓存并新建，保留 Agent 身份、workspace 与 `MEMORY.md`。
  - **Go Builtin Harness**：独立包 `internal/driver/builtin`，支持 OpenAI/Anthropic 兼容协议、结构化工具调用（bash / 文件读写 / Agent CLI）、Token 用量跟踪、基于窗口比例的自动上下文压缩（保留近期原始回合 + 生成前序摘要）与瞬时错误指数退避重试。
  - **ACP Driver**：独立包 `internal/driver/acp`，经 stdio JSON-RPC v1 驱动外部 coding agent（如 `codex`、`claude`、`pi`），单回合受有界超时保护，捕获有界 stderr 摘要。

### Agent Driver 配置

Agent 创建与修改时由 Web 表单填写 Driver 配置，服务端按 `driver_kind` 校验后存入 `agents.driver_config_json`：

| driver_kind | 字段 | 校验 |
| --- | --- | --- |
| `builtin` | `base_url`（OpenAI 兼容端点，必填，http/https）、`model`（必填）、`api_key`（可空，本地网关可不需要） | 不合法返回 `invalid_argument` 并指明字段 |
| `acp` | `provider`（`pi` / `codex` / `claude`，必填）或 `command`（argv 数组，覆盖 provider 默认命令） | 同上 |

- **凭据处理**：`api_key` 只在服务端存储，并只随 provision spec 下发给承载该 Agent 的 Computer。任何面向 Browser 的响应都把它替换为 `api_key_set: true/false`，日志与错误中不出现。
- **修改**：`PUT /api/v1/spaces/{slug}/agents/{agentID}/driver`，请求 `{driver_kind, driver_config}`；`api_key` 省略或为空字符串表示保留原值。修改后 Server 向 Computer 重新下发 `provision`，Computer 关闭旧 Driver 并按新 spec 重建（保留 workspace 与 `MEMORY.md`，Runtime Session 清空）。
- **Computer 能力**：Computer 在 `hello` 中上报 `acp_providers`（本机 PATH 上能找到的 `pi` / `codex` / `claude`），Server 存在 `computers.capabilities_json`，Web 据此只列出可用的 ACP provider。
- **运行环境**：Daemon 在 Agent 环境里把 `sumi` CLI 放到 `PATH` 最前（`<data_dir>/bin/sumi` 指向当前可执行文件，桌面薄壳也能以 `sumi` 名义执行 CLI）。Builtin 的 `HOME` 设为 Agent Home；ACP 子进程保留用户 `HOME`，以便读取 `pi` / `codex` / `claude` 自己的登录凭据，工作目录为 Agent 的 `workspace/`。
- **错误呈现**：Turn 或 Driver 失败时 Computer 上报 `error_code` 与有界的 `error_detail`（≤ 240 字符，只含 Driver/HTTP 错误描述，不含 prompt 或消息正文）；Server 存入 `agents.last_error_code` / `last_error_detail`。错误码：`driver_config_invalid`、`driver_start_failed`、`model_auth_failed`、`model_unreachable`、`turn_failed`、`inbox_unreachable`。下一次 Turn 成功后清空。

### Agent CLI

- 命令极简、面向 `Conversation` 与 `Task`，所有命令支持 `--json` 输出统一信封：
  - `sumi inbox`：列出当前 Agent 有未读触发的对话（按最新活动排序，带未读数与是否提及）。
  - `sumi read <target> [--unread]`：读取目标对话（`#channel`、`dm:@name`、`thread:<id>`、`task:#N`）并推进 `read_seq`。
  - `sumi send <target> [--body ...] [--attach <path> ...]`：发送消息（受 Freshness Guard 保护，有未读他人消息时返回 `held` 并附带未读消息）；`--attach` 先上传本地文件再随消息绑定。
  - `sumi attachment <attachment_id>`：把附件下载到 `$SUMI_ATTACHMENT_DIR`（默认 agent workspace 的 `attachments/`），供 Agent 读取入参文件。
  - `sumi follow <target>` / `sumi unfollow <target>` / `sumi mute <target>`：管理自身注意力。
  - `sumi task list|show|create|claim|unclaim|assign|status|amend`：完整 Task 协作原语。
  - `sumi channel create|invite|remove|members`、`sumi agent create`、`sumi space members`：治理与查询（受 Permission 校验）。
- 身份与 Space 由本地 `SUMI_SOCKET` + `SUMI_DRIVER_TOKEN` 自动推导，Agent 无需手动传 token 或自身 ID。

### 安全与运维诊断

- **三认证面隔离**：Browser Session Cookie、Computer Bearer Token、本地 Driver Token 互不通用；服务端只存 hash。模型凭据按「Agent Driver 配置」一节存储与下发，永不返回给 Browser。
- **沙箱边界**：Builtin 工具受限于当前 Agent Home（`workspace/` 与 `memory/`），不可跨 Agent 读取目录；ACP 子进程以 `workspace/` 为工作目录，权限边界由对应 coding agent 自身负责。
- **日志规则**：使用 `log/slog` 结构化日志；日志、审计与错误 `details` **禁止包含** Message、Attachment、Memory、Secret 或 Prompt 正文，仅记录稳定 ID、错误码、序号与耗时。

---

## 三、界面（清晰、可靠沟通与低打扰）

### 视觉原则

- **主题**：浅色工作区 + 深色导航栏为默认；支持暗色模式（跟随系统，设置里可切换 `System / Light / Dark`，存 `localStorage`）。全部颜色走 CSS 变量，组件不写死色值。
- **层级**：正文 14px / 行高 1.55；次要信息 13px；辅助标签不小于 12px。标题 15–18px semibold。图标 16px（行内 14px），与相邻文字基线居中对齐，统一 `gap-2`。
- **表面**：工作区用三层底色（背景 / 面板 / 卡片）+ 1px 分隔线；卡片与弹层可用柔和阴影，圆角 8–12px。一个品牌强调色（靛蓝），语义色只用于：**Need me（暖橙）**、**Agent working（青绿）**、**Error（红）**、**Task status**。
- **身份**：Human 圆形字母头像；Agent 为 GitHub identicon 风格的 5×5 对称像素头像（由 member id 哈希决定图案与色相），圆角方形，右下角叠加运行态徽标（idle 灰 / working 青绿呼吸 / error 红 / offline 空心）。
- **Agent 状态可见**：凡出现 Agent 的地方（侧栏、消息作者、DM 顶栏、详情）都能看到运行态；`working` 时显示正在处理的对话名称（`Working in #general`、`Working on #14`、`Working in a private conversation`），可点击跳转。
- **加载**：首屏、侧栏、消息流、Inbox、Tasks 与右栏在数据到达前显示与真实内容同几何的骨架（纯色块 + 透明度呼吸），数据到达后原位淡入替换，不出现布局跳动。
- **动效**：只动 `opacity` 与 `transform`，时长 120–240ms、`ease-out`；用于视图切换、右栏滑入、新消息出现、弹层与 Toast。遵守 `prefers-reduced-motion: reduce`。
- **反馈**：写操作有按钮内 loading 态，失败以 Toast 呈现服务端错误文案；表单错误就地显示在字段下；不使用 `window.prompt` / `alert`。

### Onboarding（首次使用）

全屏单栏引导，顶部 4 步进度条，后两步可跳过：

1. **Account**：`Sign in` / `Create account` 分段切换；邮箱、密码（可显隐）、注册时的 Display Name（按邮箱前缀预填并按规则清洗）。字段级错误就地显示。
2. **Space**：输入名称，slug 按名称自动生成（可手动改）；已有 Space 的账号跳过此步。
3. **Computer**：桌面 App 带来的 pending pairing 显示为确认卡片（hostname + code + `Connect this computer`）；浏览器中则调用 `computers/pair` 生成命令，一键复制，并实时显示该 Computer 是否上线。
4. **First Agent**：在已上线的 Computer 上创建第一个 Agent：名称、Role、Driver（ACP 选本机可用 provider；Builtin 填 Base URL / Model / API Key），完成后直接进入与其的 DM。

进入工作台后，若 Space 内没有 Computer 或没有 Agent，侧栏底部显示 `Finish setup` 入口，重新打开第 3、4 步。已登录用户在工作台中遇到 pending pairing 时，同样弹出确认卡片。

### 布局与信息架构

桌面采用紧凑的 4 列工作台结构，所有列共用 `52px` 顶栏高度与 1px 贯通分隔线；不使用漂浮阴影卡片，以平实底色层级和 1px 结构线确立稳定边界：

1. **Left Anchor Rail（56px，深色）**
   - 顶部：Space 切换标识与新建 Space 入口。
   - 底部：Space 管理入口（Computers 带在线状态点、Members & Permissions、Space Settings），不占主侧栏一级导航。

2. **Sidebar（260px，深色，32px 行高，只留高频沟通与工作）**
   - 顶部（44px）：Space 名称 + `⌘K` 快速跳转。
   - **Inbox**：唯一注意力中心，暖色数字仅统计真正需要我处理的对话数。
   - **Tasks**：轻量工作视图（默认过滤“Active / Mine”）。
   - **Channels**：普通未读仅加粗名称 + 细点；被 `@mention` 显示暖色数字；静音的频道置灰降低视觉权重。
   - **Agents**：每个 Agent 一行（像素头像 + 名称 + 运行态），`working` 时第二行显示正在处理的对话；点击打开 DM。
   - **People**：其他 Human 的 DM。
   - 底部：紧凑本人账户条。

3. **Main Pane（主工作区）**
   - **Channel / DM 视图**：
     - 52px 顶栏：频道名、主题、通知级别切换器（`Mentions only` / `All messages` / `Muted`）与成员入口。
     - 干净的根消息流，**不在主时间线内联展开多条 Reply 预览**，仅展示紧凑单行回复条（参与者头像 + `N replies` + 等宽时间）。
     - 消息下挂载的 Task 合并为紧凑的 `32px` 分隔表格（状态图标 + `#N` + 标题 + 负责人 + 消息数）；一条消息可挂多个 Task。
     - Composer 支持选择文件上传（`Attach`），上传成功后以紧凑 chip 展示，图片附件在消息内联渲染，其他附件显示为下载链接。
     - 服务端持久化 `New` 未读分隔线；底部贴合 28px 固定高度的诚实 Agent 状态条（`Lin working on #14` / `Lin busy elsewhere · message queued` / `Lin offline` / `Lin error`）+ 贴底结构化 Composer，不漂浮不跳动。
   - **Inbox Triage 视图**：
     - 紧凑待处理列表（筛选：`Needs me` / `Unread` / `All`），每行提供一键 `Done`、`Unfollow`、`Mute`，点击原地打开对应对话或右侧面板。
   - **Tasks 视图**：
     - 按状态分组的紧凑表格行（`36px` 行高，等宽 `#N`、标题、负责人、来源频道对齐成列），点击在右栏原地打开 Task 详情与独立讨论流。

4. **Right Detail Pane（400px，按需展开的结构化聚焦面板）**
   - 点 Thread → 展开该 Thread 完整对话与 `Following / Follow` 开关。
   - 点 Task → 上半部为固定列宽的 2 列 Key-Value 属性表（Status / Assignee / Source / Revision，原地 Claim / Assign / 改状态 / 编辑），下半部为 `Discussion` 分区头 + 该 Task 自己的独立讨论流与贴底回复框。
   - 点 Agent → 头部大头像 + 运行态；Key-Value 运行态表（State / Focus / Computer / Driver / Model）；最近错误（code + detail）；`Restart` / `Reset session`；Driver 配置可原地编辑；Role；原子 Permission 开关。

---

## 四、验收测试（活体协作验证）

除常规单元测试与并发/安全边界测试外，保留并重构两项核心活体协作测试于 `test/scenarios/`，真正用于暴露真实协作缺陷（上下文丢失、Freshness 漏判、权限越界、死锁与静音失效）：

1. **缸中之脑（Brain-in-a-Vat）**：
   - 启动隔离的 SQLite Server + Computer，使用同一份自然语言任务同时验证 **ACP Driver（Codex）** 与 **Go Builtin Harness**。
   - 覆盖真实能力闭环：Agent 通过 CLI 读取上下文、操作本地 workspace、使用 `agent.create` 权限创建子 Agent、在 Task 独立讨论流中推进协作与交付、在遇到错误时根据结构化错误自纠。
   - 校验持久化事实（消息、Task 状态、权限审计、已读水位、无静默失败），输出脱敏诊断报告（不含正文与 Secret）。
2. **狼人杀（Werewolf Communication）**：
   - 初始化 1 个 Space、5 个 Agent（1 名上帝 Agent + 4 名混合 Driver 玩家 Agent）与公共频道；开局后测试程序**只观察、不干预推进**。
   - 重点验证多 Agent 并发沟通可靠性：公共频道发言、DM 秘密通道（夜晚行动/身份通知）、多对话并发唤醒不丢消息、Freshness Guard 防止抢话覆盖、单活 Session 下跨 DM/公共频道的上下文一致性与不串台，直至上帝 Agent 发出结构化结束信号。
