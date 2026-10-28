# UI_DESIGN

本文件定义 WebUI 已采用的行为与视觉要求。产品要求见 `DESIGN.md`。

本文件只记录已采用的规范，不保存候选方案。界面截图演示数据见 `scripts/ui-demo/`。

## 视觉（Clarity）

- 视觉语言是“清晰”：中性底色、发丝线分隔、单一的选中态。界面不靠颜色营造层次；颜色只保留给两个注意力信号和 Task 状态。不使用渐变、玻璃、模糊、硬偏移阴影或大写标题。
- 注意力只有两种信号，任何页面都不新增第三种：
  - **For you**（暖色）：有人在等你。提到我或 `@all`、且作者不是我的 Message 整行使用 `--you-row` 浅暖底，作者行显示 `For you`；正文里对我的 mention 用 `--you-ink`；侧边栏 Inbox 计数使用 `--you-soft` 胶囊。
  - **Agent working**（青色 `--live`）：Agent 正在处理。头像外圈呼吸环；该 Agent 最新一条 Message 下方显示 `X is working on this` 与三点动画；当前步骤只在 hover（`title`）中显示，不展开执行过程，也不提供过程控制。
- 颜色 token 定义在 `web/src/styles/tokens.css`：底色 `--bg #FFFFFF`、侧栏 `--bg-sidebar #F7F6F4`、subtle `#F3F2EF`；墨色 `--ink #1C1B19`、`--ink-2 #56544F`、`--muted #8F8C86`；线 `--line #EEEDEA`、`--line-strong #DFDDD8`；信号 `--you* / --live*`；状态 `--status-*`；图表 `--series-1/2/3`。页面样式只引用 token，不写字面颜色。
- Space accent 只取四个预置色（`#F0602F`、`#E8B42D`、`#3C9E8F`、`#6C5CE7`），只用于 Space 标识本身，不染色界面。
- 字体：Geist（中文回退 Noto Sans SC）；代码、seq、数值使用 Geist Mono 与 tabular figures。
- 字号层级：页面标题 17px/600、区域标题 13px/600（句首大写，不用全大写）、正文与 Message 14px、辅助信息 13px、元数据 12px。
- 控件高度：默认 34px、紧凑 30px；按钮 1px 线框或实心 ink（主要 CTA），圆角 7px。自定义下拉（SumiSelect）浮层使用 `--shadow-popover`，选中项加粗并带勾；表单不渲染浏览器原生 select。
- 选中态全站统一：白色抬起卡片（`--shadow-selected`），用于导航当前项、筛选 tab、分段控件。hover 只换极浅底色。
- 身份：Human 是圆形头像（首字符 + 稳定浅色底）；Agent 是圆角方形印章（`#F1F0EC` 底 + 由 member ID 生成的 8×8 对称像素）。形状即可区分人与 Agent，消息里不显示 `AGENT` 标签；成员列表、详情等身份场景显示 `Agent` 小标签。
- Task 状态由图形形状承载：TODO 虚线圆、In Progress 半满圆、In Review 圆点环、Done 对勾、Closed 斜线；同时带状态文字或 `aria-label`，颜色不是唯一线索。
- 附件以浅底小条展示（subtle 底 + 圆角），不画边框。
- 所有图形信号带 `aria-label` 和 `title`。

## 布局

- 桌面布局为 Sidebar、Channel、可选 Thread pane 三栏；不再有 Space rail。Thread pane 只在打开时存在（默认 360px），展开时 Channel 自适应收缩。
- Sidebar 宽 256px，自上而下：Space 头（标识 + 名称）、Space tools（Inbox 带待读计数、Tasks、Members、Computers；开启实验功能后出现 Insights、Company）、当前页面的上下文分组（如已配对 Computers、Settings feature 列表）、Channels、Discover、Direct messages，底部固定为当前身份与 Settings 齿轮。
- DM 列表中 Agent 正在工作时显示青色 `Working`，否则显示 Role 摘要。
- 1100px 及以上保持三栏；低于 1100px 出现顶栏（`Open navigation` 菜单按钮、Space 名称、Inbox 计数），Sidebar 变为抽屉，Thread 覆盖 Channel；低于 700px 使用单列，Thread 和 Task 详情为全屏路由。
- Header 高 56px。Message 内容占满 Channel 可用宽度；消息较少时时间线贴近 Composer，像聊天一样从底部开始。
- Composer 是抬起的白色卡片（`--shadow-raised`、圆角 14px），输入框本身无边框；高度随内容增长到 240px 后内部滚动。左下为附件，右下为 32px 圆角方形发送按钮（ArrowUp，空内容时变浅），提示 `⌘ Enter to send`。
- 返回 Channel 时恢复上次退出的滚动位置（无保存位置时定位到最新 Message）和打开的 Thread；离开 Conversation 区域后，Conversation 入口与 Space 首页回到上次所在的 Channel/DM；Composer 未发送正文按对话保存在浏览器本地，发送成功后清除。
- Composer 只包含 Markdown、Attachment、mention 和 Send；DM 的 Composer 不提供 mention。

## 对话与 Task

- Channel 时间线只展示 Root Message；Thread pane 独立展示对应 Root Message 及其 replies，不为 Channel 列表重新编号或混排 replies。Message 的 `seq` 是 Channel 内 root 与 reply 共用的全局坐标，界面以 `Channel sequence` 说明该语义；有 replies 的 Root Message 显示 reply 数量和已加载的最新 reply seq。
- Channel 与 Thread 收到新消息时，距底部不超过 3/4 可视屏高则自动定位到最新 Message；超过 3/4 屏时不自动定位，显示 `To bottom`，点击后回到最新 Message；发送成功后同样定位到新 Message。Channel 在恢复的旧位置或离开期间产生的新消息，以数量展示在 `To bottom` 按钮上，点击回到最新 Message 后清除。
- 只有 Root Message 提供 `Create Task` 动作；创建后立即替换为 Task 标识，不显示 bind、Source Thread 选择或来源确认步骤。
- Thread reply 不显示创建动作；reply 上的 Task 动作保持禁用并说明只能从 Root Message 创建。
- 每条 Message 在 hover 或键盘 focus 时才显示：左侧 gutter 的 `seq`（连续消息显示时间）、右上角的轻量动作面板（Reply to thread 与 Task 动作，白底浮起小条）。静止时消息行只有头像、名字、时间和正文。
- Channel 消息与 Thread reply 中的 Agent 头像可点击，直接打开与该 Agent 的 DM；DM 消息中的头像不可点击。
- Root Message 的 Task 标识是正文下方的浅底小条：状态图形、`!<seq>` 与状态文字；title、assignee 不显示在消息上，hover 显示 title；当前 Run 在其他 Linked Thread 时显示 `· elsewhere`。只有 Root Message 显示。
- Task 状态颜色只作用于状态图形本身（TODO 灰、In Progress 青、In Review 暖、Done 绿、Closed 浅灰），标签底色保持中性。
- Action Message 使用紧凑结构化行，显示 actor、动作和资源链接，不显示原始 JSON、命令参数或内部 ID；普通 composer 不能选择 action kind。
- System Notice 渲染为居中系统信息行：成员名用 ink，正文 muted；不画横线、无边框；同一日期内连续多条默认折叠，折叠按钮为纯文字。
- Agent attention 失败在来源 Message 下方显示 inline notice，包含目标 Agent、稳定错误码和重试状态，不显示数据库错误或正文副本。
- Message 正文支持常用 Markdown（含 `- [x]` / `- [ ]` 任务列表渲染）；正文中的 mention 和 Task 引用只渲染 Server 返回的结构化结果，不根据符号推断。
- 普通 Message 正文超过 8 个视觉行时默认折叠，按实际排版高度判断，不按字符数推断。

## Task 页面

- Tasks 页面使用 list/detail 布局，是对话的辅助入口，不是项目管理页面；入口保留在侧边栏。
- 筛选固定为 All open、To do、In progress、In review、Done、Closed、Assigned to me；列表按状态分组，组标题带数量，不显示列头；行内依次为状态图形、`!seq` 与标题、Assignee、Source Thread、更新时间；默认先显示 In Review，再显示 In Progress 和 TODO，各组按 updated_at 倒序。
- Task 详情依次显示：Task facts（Created by、Updated；TODO 未指派时可选择 Agent 开始）、Actions、Source Thread、Result（仅 Done）、Close reason（仅 Closed）、侧栏（Current Run and Focus、Recent Run outcomes、Session continuity）。
- Actions 只有两个操作：Done（直接完成，不需要理由）与 Closed（弹窗选择理由 + 可选备注）。不提供其它状态操作按钮。
- Task 创建后不可修改（见 `DESIGN.md`）；不显示 Related Threads 区块，不提供手动 Link/Unlink。
- Recent Run outcomes 在独立滚动容器内展示，每条 Run 聚合为一行（状态、Agent、Focus、错误码、起止时间）。
- Session continuity 取值 Warm / Cold / Reset required / Unavailable，只说明下次执行效率。
- Working、Waiting、Failed 是 Run 或 attention 状态，不进入 Task 状态选择器。

## Company 页面

- Company 是 Space 的公司形态入口，默认进入 Company Office；Office 入口在 Sidebar 上下文分组。
- Company Office 是像素化办公区：每个 Agent 一个工位，按 activity 状态显示坐姿、打字或空闲；不同 Agent 使用不同配色区分，名字固定在头顶。Agent 空闲一段时间后会随机游走、去茶水角喝水/喝咖啡或站到游戏桌，移动速度缓慢；一旦转为工作状态就走回工位（不瞬移）；Agent 参与 DM 活跃时，发起方走到对方工位讨论；群组频道在数分钟窗口内消息达到阈值时，频道内 Agent 聚集到会议区。动画遵循 `prefers-reduced-motion`；静止模式下停止随机游走与定时位置更换，工作、DM 和群组事件直接呈现目标位置。
- Office 场景与小人使用 Arlan_TR 的 [Free office pixel art](https://arlantr.itch.io/free-office-pixel-art)（itch.io 免费素材包）：场景由包内独立物件拼装（隔断、工位、电脑、椅子、绿植、水冷机等），地板为平铺像素砖；Agent 使用包内 Julia 的 idle / walk 动画，工作状态使用 Julia 的背面帧并对应电脑亮屏；页面底部展示作者署名。
- 办公室画布随 Agent 数量成长：0 人显示空办公室、1-3 人 480×270 单排、4-6 人 640×360 两排、7-9 人 800×450 三排、10-12 人 960×540 三排四列；超过 12 人后保持四列并通过增加行数向下扩展画布。只给实际 Agent 生成工位与电脑，每个 Agent 使用独立工位；人少时桌子挨在一起，画布也更小。

## Inbox、Members、Computers、Agent

- Channel header 的 Member strip 使用与 Agent 头像相同尺寸的加号按钮，并在 Agent 选择浮层中显示 Display Name 与 Role。
- Human Inbox 按 DM 和 Thread 聚合，每组使用 For you 暖底，同一组只显示最新来源 Message 的预览、时间和组内 Item 数；打开聚合行即标记已读。
- Inbox 页 header 提供 Mark all read，一次将当前 Human 全部 pending Item 标记为已读；没有待读 Item 时不显示按钮。
- Agent Inbox 不向普通 Member 公开；Owner/Admin 只能读取来源摘要和错误代码。
- Members 页面按 Agents、Humans 分组显示扁平列表行：身份（头像 + 名字 + 行内状态点）、Access 控制（自定义下拉）、30px 轻量消息图标。Permission 在 Agent 详情逐项管理，当前动作包括 channel.create、channel.invite、channel.remove 和 agent.create，不提供 Role 套餐。
- Computers 页面左侧为已配对列表和配对入口；未选择 Computer 时显示配对 onboarding。
- Computer 更新状态在 Server 持久化协议完成前不进入 UI；第一期只通过 Computer 本地结构化日志和 update journal 诊断，禁止从在线或离线状态推断更新结果。
- Agent 详情头部只显示安静信号（Activity、Computer 可达性、错误码），不提供 DM 按钮；facts 值使用 13px 并截断超长文本。
- Agent 详情 Activity feed 按时间倒序；参数压缩为单行 `name = value`（名称 muted 等宽、值为 subtle 底 code，最多 3 个），message preview 为 subtle 底 + 左侧细线的截断块，时间右对齐；字号统一。
- 详情分区使用扁平布局：不绘制卡片边框、底色或阴影，用 1px `--line` 分隔线组织。

## Agent insights

- `Agent insights` 是默认关闭的实验功能：只有 Settings 开启后，Sidebar 的 Space tools 才显示 Insights 入口；路由为 `/s/$spaceSlug/insights`，未开启时直达页面显示禁用说明与 Settings 链接。
- insights 使用列表/详情布局：Sidebar 上下文分组是 `Statistics` 与 `Graph` 两个条目（`/insights/stats`、`/insights/graph`），右侧展示对应内容。
- Statistics 页按 Agent 聚合 LLM 消耗：左列是可选中 Agent 列表（像素印章 + input/output 摘要），右侧显示该 Agent 的请求数、input/output/cached 统计卡、SVG 曲线和按 model 的分组表；支持 24h/7d/30d。部分 Computer 查询失败时显示部分数据警告，全部失败时显示错误与 Retry，不显示为无 usage。
- Graph 页为力导向关系图 + 右侧详情面板。节点是 Agent 像素印章头像（与 PixelIdentity 同源算法，SVG 内联渲染，不依赖 HTML foreignObject）+ display name；边是 Agent 之间的互动关系，使用 1px 细线，hover/focus 显示总数，选中态只通过头像描边与整体明暗区分。
- 支持拖拽节点、拖拽空白平移、滚轮缩放和屏幕上的 zoom in/out/reset 按钮；`prefers-reduced-motion` 下不做动画。
- 点击节点高亮其邻居并在面板列出相邻关系；点击边显示统计明细（DM、mention、reply 分向计数）和 Communication chain（最近 5 条可读消息的 author、kind、时间、正文预览）。
- 空态提示创建 Agent；加载失败显示 Retry。所有图形节点可键盘聚焦（Enter/Space 选中，Esc 清除），图形信号同时有文字与 `aria-label`。

## Settings

- Sidebar 底部身份行右侧提供 Settings 入口（齿轮图标），路由为 `/s/$spaceSlug/settings`。
- Settings 使用列表/详情布局：Sidebar 上下文分组是已注册 feature 的名称列表（含类型与 On/Off 状态），点击后在右侧展开该 feature 的详情；未选中时右侧显示占位说明。
- feature 通过注册表（`featureRegistry`）声明 id、名称、类型、描述、storageKey 和可选配置项；后续新增 feature 只改注册表，无需改页面结构。
- 实验类型（experimental）的详情默认包含 `Enabled` 开关；所有状态只持久化在浏览器 localStorage（`sumi.feature.<id>`），不上传 Server。开启后 Sidebar 才显示对应实验入口（当前为 Agent insights 与 Company office；Company office 未开启时直达 `/company/office` 显示禁用说明）。

## Computers 的 LLM usage 面板

- Computer 详情页为 Owner/Admin 显示 `LLM usage` 面板；数据只来自该 Computer 本地存储，Server 不保存。
- 面板包含请求数、input/output tokens、cache hit rate 四张统计卡；SVG 曲线展示 input/cached input/output 随时间的走势（24h 按小时、7d/30d 按天）；`By model` 与 `By agent` 两张分组表。
- 提供 24h / 7d / 30d 周期切换；Computer 离线显示离线说明，无数据显示空态，daemon 不可用显示重试提示。图表有 aria-label 与图例，控件可键盘操作。

## 响应式与无障碍

- 所有操作支持键盘，focus 可见；icon button 具有 accessible name。
- 自定义下拉、折叠分组等交互支持完整键盘操作（方向键、Enter、Esc）。
- 动画遵循 `prefers-reduced-motion`。
- 390px 和 1440px 视口都能完成核心流程。
