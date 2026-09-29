# 第二批独立对话协调（T05 / T08 / T10）

2026-09-29：用户选择“三个独立对话，各自调用子 Agent 审查”，授权当前总协调对话向它们发送任务、接口协调和修复反馈。创建请求已接受，各 checkout 已确认从 `3adaa587d16651a6d48f123d73c1e5cceaf75271` 建立对应分支；对话创建接口暂返回 clientThreadId，尚未通过 list_threads 取得正式 threadId，因此不将队列 ID 用于 read_thread、wait_threads 或 send_message_to_thread。

用户已确认统一审查起点 `3adaa587`。T08/T10 的启动说明已包含确认；T05 创建时确认尚在等待，应在正式 threadId 可用后补发确认，避免子任务重复询问。总协调对话 ID：`01a0e5ba-ea2d-7ea1-85e5-cb87f159495d`。每项固定范围及独立两轴审查、公开链路验证要求已写入创建 prompt。

| Ticket / Issue | 分支 | 已确认 checkout | 创建请求 |
| --- | --- | --- | --- |
| T05 / #13 故障对账 | `codex/source-failure-reconciliation` | `C:/Users/28211/.codex/worktrees/b0df/WeKnora` | `client-new-thread:13dc16bc-fad8-401f-bd81-6a110bd6d512` |
| T08 / #16 Python | `codex/source-python` | `C:/Users/28211/.codex/worktrees/a8ea/WeKnora` | `client-new-thread:5a022215-899f-4c45-b382-010e63f7967b` |
| T10 / #18 MyBatis XML/SQL | `codex/source-mybatis-sql` | `C:/Users/28211/.codex/worktrees/2119/WeKnora` | `client-new-thread:64ff8e53-ed4d-431d-aaca-e54feee94c36` |

各独立对话负责实现、相关验收及 Standards/Spec 两个并行审查子 Agent。当前对话负责接口决定、最终集成/交叉验证和 GitHub 状态；不得提前关闭本批 Issue、修改父 Spec 或创建 PR。T05 如需迁移使用 000106，T10 如需迁移使用 000107；T08 若需迁移先协调。Python 和 XML/SQL extractor 优先独立文件，公共语言路由与 processing fingerprint 由集成任务合并；不得放宽 T03 范围、T04 完整空发布、T14 已登记历史证据和明确清除优先。

专用 PostgreSQL 容器共享且不得重启/删除，各测试使用独立 schema。四语言已验收 grammar cache 只读，Python 建立独立 cache。各任务执行针对性测试、格式/类型与相关构建，完整 Go/前端套件由总协调任务集成后统一执行一次。首批失败边界继续如实记录；代表源码仅静态读取，不执行或把内部业务源码推送公开 GitHub。

旧首批实现 worktree 保留，其 grammar cache 仍供验证读取；新对话绑定各自托管 checkout。原目录 D:/Project-Weknora/WeKnora 保持 main。下一次协调先解析三个正式 threadId，然后使用 wait_threads 的紧凑快照获取进度、传达 T05 的审查起点确认并协调公共接口。
