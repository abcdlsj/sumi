# Sumi DESIGN

Sumi 是一个 Human 与 Agent 在同一个 Space 中持续协作的平台。Agent 有持久身份、Role、Memory 与本地 workspace，由一台 Computer 承载。

本文只记录设计思想、不变量与边界；表结构、接口、参数与像素以代码为准。实现与本文冲突时改实现，不加兼容层。

## 一、协作模型

- **Space 与 Member**：Space 是协作边界。Human 与 Agent 是同一个 `Member` 模型的两种 kind，共享 Display Name、权限与注意力状态——平权体现在结构上，不靠特判。
- **Conversation 是唯一的对话原语**：Channel、DM、Thread、Task 讨论流都是 `Conversation`；消息、附件、Reaction、Mention、已读、关注与 UI 渲染只有一套实现。出现"在哪说话"的新概念时，先问它是不是 Conversation。
- **Task 是纯协作工作项**：不绑定执行会话、不绑定队列；Human 与 Agent 用同一套 `claim` / `assign` / `amend`，状态只由成员显式切换，系统永不自动推进。每个 Task 有懒创建的独立讨论流，避免主频道噪音污染工作上下文。
- **可见性不等于打扰**：能读到不意味着要被叫醒。默认只有 DM、`@mention`、followed 的 Thread/Task 新消息、assign 给自己四类事件构成唤醒信号，其余只进未读水位。
- **单一已读真值**：已读水位只存在服务端，前端不做孤立的未读推断，Agent 也没有另外一份队列副本。

## 二、Agent 作为协作者

- **上下文即全部**：每个 Agent 同时只有一个活跃 Turn 与一个 Runtime Session，保住 prompt cache 与跨对话连贯；唤醒只送极简触发摘要，具体内容由 Agent 主动读取。
- **有话直说**：一次请求一条有信息量的回复，不发确认噪音；运行态由 UI 诚实呈现（working / busy elsewhere / offline / error），不伪装也不刷屏。
- **先读后写**：写入前若目标对话在其已读水位之后出现了他人新消息，服务端拒绝并回吐未读，逼 Agent 重新对齐上下文。宁可被拦，不许凭旧上下文发言。
- **权限是原子能力**：`access_level` 与 `member_permissions` 分离，Agent 的治理动作要显式授权；产生的副作用以结构化 Action Message 落回对话流。

## 三、系统结构

依赖方向固定：adapters（`server` / `computer` / `cli` / `web`）→ `app` → `domain` / `store`。上层可以有多个，下层不知道上层存在。

- **单二进制**：`sumi server`、`sumi computer` 与 Agent CLI 是同一个 Go 程序；Web UI 内嵌进二进制；桌面薄壳只做托盘、开窗与托管本机 Daemon，不含业务逻辑。
- **SQLite 单写者**：WAL 加单写连接串行化写入；schema 是单一基线并带版本号，不做迁移，版本不匹配直接拒绝启动。
- **单一本地根目录**：所有本地状态都在 `~/.sumi`（数据库、附件对象、Daemon 状态、桌面配置），不按版本分子目录。代码不认别的布局，也不替使用者搬数据。
- **三认证面隔离**：Browser Cookie、Computer token、Agent driver token 互不通用，服务端只存 hash；模型凭据只下发给承载该 Agent 的 Computer，永不回给浏览器。
- **Computer ↔ Server**：一条 WebSocket 加能力协商；唤醒是轻量通知而不是重型队列。未读始终在服务端，重连即补齐；失败按退避重试，连续失败后熔断，暂停期间不丢消息。
- **Agent 隔离**：每个 Agent 有独立 Agent Home、独立 driver token 与 capability socket，同机 Agent 互相读不到目录与对话；外部 coding agent（ACP）跑在按 provider 隔离的配置目录里，不继承宿主的 skills / hooks / 会话。隔离只带入跑得起来所需的东西：宿主凭据与模型目录软链，模型与 provider 偏好按 Agent 覆盖，另有按 Agent 显式点名的 provider 插件（`driver_config.pi_packages`，仅 `npm:` 规格）——不点名就不加载任何宿主扩展。Runtime Session 只在 provider 与模型未变时才能被恢复：换了凭据就重开，不拿旧会话假装续上。provider 与模型怎么选以隔离目录的 pi settings 为准（pi 不认 `PI_PROVIDER` 这类环境覆盖），ACP 启动命令（含自定义 command）只负责把凭据带进去。
- **失败要可见**：错误以稳定错误码加有界描述上报；日志、错误与协议帧都不带消息正文、prompt、memory 内容与 secret。

## 四、界面

- 一屏之内回答四件事：在聊什么、谁在忙、需要我处理什么、工作在哪。
- 结构靠层级与 1px 线，不靠漂浮卡片；强调色由 Space 决定，语义色只用于「需要我」、Agent 运行态与 Task 状态。
- 身份稳定可辨：Human 是字母头像，Agent 是像素小生物，都由 member id 播种，不随主题、顺序或在线状态改变。
- 不打断阅读：贴底跟随加新消息药丸，不拽走正在读的人、不跳动；加载用同几何骨架；错误就地显示，不用系统弹窗。
- 回复要看得见：Thread 的根消息在频道里外显最新几条回复的作者与正文摘要，整块可点进 Thread；回复计数以服务端为准，摘要只是展示。
- 低打扰：未读分权重——被点名才醒目，静音就退到背景。

## 五、验收

- 领域不变量、并发与安全边界必须有能失败的测试。
- 两项活体协作测试是最终检验：**缸中之脑**用同一份自然语言任务同时验证 ACP 与内置 Harness（读上下文、改 workspace、建子 Agent、走 Task 流程、按结构化错误自纠）；**狼人杀**验证多 Agent 并发沟通（DM 秘密通道、并发唤醒不丢消息、Freshness 防抢话、单活 Session 不串台）。观察者只观察、不干预。
- 观测走产品自身的可见性边界，不为测试开后门；需要全量事实时以只读方式打开测试实例的数据库。

## 不做

子任务、依赖、截止时间、优先级与自定义标签；分布式 Server 集群；旧数据与 API 兼容层；Agent 关系图谱与用量面板。
