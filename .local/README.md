# Sumi 实现讲解资料

这组文档用于讲解 Sumi 的实现。结论以当前代码、schema、测试和设计文档为依据；历史问题单独标为“历史行为”，不把旧缺陷当成当前保证。

## 建议阅读顺序

1. [讲解总览](./01-讲解总览.md)：先建立组件边界和一条完整请求链。
2. [上下文与会话](./02-上下文与会话.md)：回答“上下文如何管理、锚点是什么”。
3. [消息顺序与并发](./03-消息顺序与并发.md)：回答“为什么有时能得到 1/2/3”，并区分保证与巧合。
4. [提示词优化与安全](./04-提示词优化与安全.md)：说明 Prompt 分层、评估方法和 Provider 干涉边界。
5. [领域模型与协作流程](./05-领域模型与协作流程.md)：说明 Space、Thread、Task、Inbox、Run 如何协作。
6. [可靠性恢复与可观测性](./06-可靠性恢复与可观测性.md)：说明重连、重试、幂等、更新和诊断。
7. [设计亮点、边界与演示提纲](./07-设计亮点、边界与演示提纲.md)：整理可作为结论的优点、已知边界和演示话术。
8. [实现与设计核查](./08-实现与设计核查.md)：列出讲解时必须区分的当前实现、产品要求与已知风险。

## 一句话总括

Sumi 不把 Agent 当成一次性聊天请求，而是把 Human 和 Agent 放进同一套持久协作模型；Server 保存协作事实，Computer 保存本地执行连续性，Driver 负责调用模型，Agent 侧的领域写入经受权限校验的 CLI 完成。

## 资料口径

- 产品不变量：[`docs/DESIGN.md`](../docs/DESIGN.md)
- 系统协议、安全与运行要求：[`docs/SYSTEM_DESIGN.md`](../docs/SYSTEM_DESIGN.md)
- 界面对 Thread、Task、seq 的展示语义：[`docs/UI_DESIGN.md`](../docs/UI_DESIGN.md)
- 主要实现：`src/server/`、`src/computer/`、`crates/sumi-agent-core/`、`crates/sumi-builtin-agent/`
- 主要验收：[`tests/builtin_harness_benchmark.rs`](../tests/builtin_harness_benchmark.rs)、[`tests/brain_in_vat.rs`](../tests/brain_in_vat.rs)、[`tests/werewolf_communication.rs`](../tests/werewolf_communication.rs)

这些资料把“当前代码已实现”“产品设计要求”“建议的讲解口径”分开写。发生差异时，以 [实现与设计核查](./08-实现与设计核查.md) 为准，不把设计要求说成已由代码强制保证。

## 先记住的四个区分

| 容易混淆的词 | 实际含义 |
| --- | --- |
| `channel_seq` | 一个 Channel 内所有 Message 共用的持久坐标，不是主时间线行号。 |
| `delivery_seq` | 一个 Run 内 Inbox Item 的投递坐标。 |
| Provider Session | Computer 本地的模型 transcript 缓存，不是 Server 的协作事实。 |
| Memory | Agent 跨 Run、跨 Channel 的本地知识，不等于 Provider Session。 |
| Agent 消息中的 `@display_name` | 一种受定义的写入语法；入口将它归一化为 mention relation，后续路由不再猜正文。 |
