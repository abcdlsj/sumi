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
desktop/               # Wails 薄壳（托盘 + 托管本机 Computer Daemon + 嵌 Web UI）
test/scenarios/        # 活体协作验收测试（Werewolf、Brain-in-a-Vat）
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
10. `message_mentions` / `attachments` / `message_attachments`：结构化 `@mention` 目标与附件关联。
11. `tasks`：`id`、`space_id`、`number`（Space 内自增）、`channel_id`、`source_message_id`（非唯一）、`conversation_id`（懒创建，可为空）、`title`、`description`、`status`、`creator_member_id`、`assignee_member_id`、`revision`、`created_at`、`updated_at`。
12. `conversation_states`：`(conversation_id, member_id)` → `read_seq`、`notify_level`、`following`、`done_seq`、`updated_at`。
13. `idempotency_records` / `events`：HTTP 写请求幂等记录与 Space 提交序事件日志（事务提交后由进程内 pub/sub 实时推送 Browser SSE，支持 `Last-Event-ID` 断点续传）。

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

### Agent CLI

- 命令极简、面向 `Conversation` 与 `Task`，所有命令支持 `--json` 输出统一信封：
  - `sumi inbox`：列出当前 Agent 有未读触发的对话（按最新活动排序，带未读数与是否提及）。
  - `sumi read <target> [--unread]`：读取目标对话（`#channel`、`dm:@name`、`thread:<id>`、`task:#N`）并推进 `read_seq`。
  - `sumi send <target> [--body ...]`：发送消息（受 Freshness Guard 保护，有未读他人消息时返回 `held` 并附带未读消息）。
  - `sumi follow <target>` / `sumi unfollow <target>` / `sumi mute <target>`：管理自身注意力。
  - `sumi task list|show|create|claim|unclaim|assign|status|amend`：完整 Task 协作原语。
  - `sumi channel create|invite|remove|members`、`sumi agent create`、`sumi space members`：治理与查询（受 Permission 校验）。
- 身份与 Space 由本地 `SUMI_SOCKET` + `SUMI_DRIVER_TOKEN` 自动推导，Agent 无需手动传 token 或自身 ID。

### 安全与运维诊断

- **三认证面隔离**：Browser Session Cookie、Computer Bearer Token、本地 Driver Token 互不通用；服务端只存 hash，模型凭据不出 Computer。
- **沙箱边界**：Builtin 工具与 ACP 子进程受限于当前 Agent Home（`workspace/` 与 `memory/`），不可跨 Agent 读取凭据或目录。
- **日志规则**：使用 `log/slog` 结构化日志；日志、审计与错误 `details` **禁止包含** Message、Attachment、Memory、Secret 或 Prompt 正文，仅记录稳定 ID、错误码、序号与耗时。

---

## 三、界面（克制、可靠沟通与低打扰）

### 视觉原则（Clarity）

- 中性纸感底色、1px 发丝线分隔、单一选中态，无渐变/模糊/多余装饰。
- 颜色只留给三个明确语义：**Need me（暖色）**、**Agent working（青色）**、**Task status（图形 + 克制语义色）**。
- 身份由形状区分：Human 圆形头像，Agent 圆角方形像素印章。

### 布局与信息架构

桌面采用三栏结构，严格克制一级导航：

1. **Left Sidebar（240px，只留高频沟通与工作）**
   - 顶部：Space 名称菜单（点开进入 Members / Computers / Settings 管理页，不占一级导航）+ `⌘K` 快速跳转。
   - **Inbox**：唯一注意力中心，暖色胶囊只统计真正需要我处理的对话数（不被静音或普通未读污染）。
   - **Tasks**：轻量工作视图（默认过滤“指派给我 / 我关注的进行中 Task”）。
   - **Channels**：普通未读仅加粗名称；被 `@mention` 显示暖色数字；静音的频道置灰降低视觉权重。
   - **Direct Messages**：Human 与 Agent 同等展示，附简洁状态点（Idle / Working / Offline / Error）。

2. **Main Pane（主工作区）**
   - **Channel / DM 视图**：
     - 干净的根消息流，**不在主时间线内联展开多条 Reply 预览**，仅展示紧凑单行回复条（参与者头像 + `N replies` + 最近时间）。
     - 消息下挂载的 Task 以紧凑单行条展示（状态图标 + `#N` + 标题 + 负责人头像）；一条消息可挂多个 Task。
     - 服务端持久化 `New` 未读分隔线；底部固定一行诚实的 Agent 状态条（`Lin is working` / `Lin is busy elsewhere · queued` / `Lin is offline` / `Lin needs attention`），出现与消失不引起消息跳动。
   - **Inbox Triage 视图**：
     - 左侧待处理对话列表（筛选：`Needs me` / `Unread` / `All`），右侧原地打开对话直接回复；
     - 每行提供一键 `Done`、`Mark unread`、`Unfollow`、`Mute channel`，不强制跳离分流流程。

3. **Right Detail Pane（按需展开的聚焦面板，380–480px）**
   - 点 Thread → 展开该 Thread 完整对话与 Follow/Unfollow 开关。
   - 点 Task → 展开 **Task 卡片（原地 Claim / Assign / 改状态 / 编辑标题描述）+ 该 Task 自己的独立讨论流**，不打断主频道上下文。
   - 点 Agent → 展开 Agent 真实运行态（当前状态、正在处理哪条对话、最近状态流转、Restart / Reset Session、Permission 管理）。

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
