# 第二批独立对话协调（T05 / T08 / T10）

2026-09-29：用户授权三个独立对话并行实现，并授权当前总协调对话发送任务、接口协调及修复反馈。用户最新要求三个实现对话统一使用 **GPT-6 Luna + 极高思考**（`gpt-6-luna` / `xhigh`）；规划、接口决策及代码审查统一由当前 **GPT-6 Sol + 高思考**（`gpt-6-sol` / `high`）对话处理。此安排取代创建时“各自调用子 Agent 审查”的分工。

三个正式 threadId 已从本机 session index 取得，并通过 read_thread 核实各自标题和 checkout。已通过 send_message_to_thread 显式设置三个模型及思考参数、发送新分工，并在三项各自新轮的运行上下文核实 `gpt-6-luna` / `xhigh`，三个设置均已生效。旧轮在安全保存后结束，未丢弃改动。原有实现、提交和验证证据保留，既有 worker 审查材料仅供总协调参考。用户已确认统一审查起点 `3adaa587d16651a6d48f123d73c1e5cceaf75271`，T05 也已收到确认，不重复询问。

总协调对话 ID：`01a0e5ba-ea2d-7ea1-85e5-cb87f159495d`。

| Ticket / Issue | 分支 | checkout | 正式 threadId |
| --- | --- | --- | --- |
| T05 / #13 故障对账 | `codex/source-failure-reconciliation` | `C:/Users/28211/.codex/worktrees/b0df/WeKnora` | `01a0ebfe-56df-7223-8644-ca717bd658bd` |
| T08 / #16 Python | `codex/source-python` | `C:/Users/28211/.codex/worktrees/a8ea/WeKnora` | `01a0ebfe-8153-70e0-898e-2b7ffa91eaad` |
| T10 / #18 MyBatis XML/SQL | `codex/source-mybatis-sql` | `C:/Users/28211/.codex/worktrees/2119/WeKnora` | `01a0ebfe-da20-71d2-a963-fa965754b497` |

各独立对话负责既定 ticket 的实现、针对性验证及执行总协调下达的修复。公共契约或设计问题先在本 ticket 的 coordination 文档报告具体文件、最小接口、证据及待决事项，由总协调决定；可继续无依赖的工作。实现冻结后向总协调交付分支/提交/未提交文件、测试证据和验收差距，等待审查。总协调负责 Standards / Spec 两轴审查、接口决定、集成/交叉验证和 GitHub 状态；需要审查子 Agent 时由总协调创建，明确使用 `gpt-6-sol` / `high`。实现对话停止自行发起规划或审查 Agent，不以旧审查材料替代最终验收。

不得提前关闭本批 Issue、修改父 Spec 或创建 PR。T05 如需迁移使用 000106，T10 如需迁移使用 000107；T08 若需迁移先协调。Python 和 XML/SQL extractor 优先独立文件，公共语言路由与 processing fingerprint 由集成任务合并；不得放宽 T03 范围、T04 完整空发布、T14 已登记历史证据和明确清除优先。

专用 PostgreSQL 容器共享且不得重启/删除，各测试使用独立 schema。四语言已验收 grammar cache 只读，Python 建立独立 cache。各任务执行针对性测试、格式/类型与相关构建，完整 Go/前端套件由总协调任务集成后统一执行一次。首批失败边界继续如实记录；代表源码仅静态读取，不执行或把内部业务源码推送公开 GitHub。

旧首批实现 worktree 保留，其 grammar cache 仍供验证读取；新对话绑定各自托管 checkout。原目录 D:/Project-Weknora/WeKnora 保持 main。后续使用正式 threadId 和 wait_threads 紧凑快照协调。当前任务审查后给出问题清单，三个实现对话执行修复，再由当前任务复验和集成；不重新创建对话或丢弃现有实现。

模型核实（UTC）：T05 2026-09-29 07:40:37；T08 07:41:50；T10 07:44:57。T08 冻结提交 `65fe6d5c`，T10 冻结提交 `fc826c19`，两项已确认等待总协调审查反馈。此记录仅确认模型与职责切换，不宣称 ticket 已验收或可关闭。

首轮两轴审查已完成，所有具体修复已派发，三个实现对话恢复并行；固定提交、分轴报告、架构决定和验收差距见 [第二批审查与修复](gitlab-code-wiki-rag-batch-two-review.md)。尚未集成或关闭本批票。

## 独立验证数据库

用户在 2026-09-29 明确选择“不读取原凭据，使用独立测试数据库”。读取旧容器密码的自动审批拒绝记录保留；不再尝试元数据凭据提取。总协调创建了仅绑定 `127.0.0.1:57521` 的 `weknora-source-batch-two-test`（`paradedb/paradedb:v0.22.2-pg17`），确认数据库及 `vector` / `pg_search` 可用。明确 fixture 身份只用于该临时库，DSN 在测试进程环境注入，不是生产配置；所有用例各自建立独立 schema。旧 `weknora-code-source-t02-test` / 57520 保持原状。现有 fixture 的数据库名 `source_test` 与主机 `127.0.0.1` 限界不放宽。

T05 初次修复提交 `7ecdfaaf` 尚未获最终接受：总协调发现 BM25-only 对已删除 token 要求非空与先前删除断言矛盾，已下达第二次具体修正；继续真实 PG 验证。T08 正在修复迭代遍历、imports、多行字符串及降级公开链路；T10 按已记录的 parser/索引/关系权限边界实施。
