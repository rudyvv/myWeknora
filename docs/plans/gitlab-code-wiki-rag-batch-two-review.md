# 第二批首轮审查与修复（T05 / T08 / T10）

2026-09-29。用户确认按“总协调审查并指出问题 → Luna 并行修复 → 总协调复验和集成”执行。审查固定起点 `3adaa587d16651a6d48f123d73c1e5cceaf75271` 已获确认；本轮按各冻结提交完整三点 diff（含新增文件）审查。Standards / Spec 两个独立 reviewer 由当前对话创建，均明确使用 `gpt-6-sol` / `high`。三个独立实现对话仍为 `gpt-6-luna` / `xhigh`，已收到各自修复清单并开始执行，没有关闭本批 Issue。

| Ticket | 本轮冻结提交 | Standards | Spec |
| --- | --- | --- | --- |
| T05 / #13 | `12fa0bc71be769f22eb05c51bac2f49503284c0d` | 0 文档规则违反，0 可执行 smell；另发现验证断言错误 | 3 项，最严重 P1 |
| T08 / #16 | `65fe6d5cb390928e6cf92d7811c6da1634b6f61c` | 0 文档规则违反，1 判断性 smell（P2） | 3 项，最严重 P1 |
| T10 / #18 | `fc826c19a9b12a6cac49f0b2c4165497314a99c4` | 3 文档冲突/违反（最严重 P1），1 判断性 smell | 5 项，最严重 P1 |

GitHub CLI 读取三个 Issue 仍返回 401；本轮采用已发布且用户确认的对应 `docs/plans/gitlab-code-wiki-rag-tickets/` 文件和父 Spec/实施方案，并核对各提交 `#13/#16/#18` 引用。无身份恢复操作或凭据输出。

## Standards

### T05

0 条标准违反和 0 条可执行 smell。变化保留 ADR-0004 发布语义，实际 SyncLogs 调用读取状态。独立验证发现 `SourceSnapshotRunView.test.ts:52` 仍断言旧“已发布 SHA：”，而 Vue:26 已改“当前发布 SHA：”；这属于具体测试回归，不虚构为仓库标准违反。

### T08

判断性 finding：possible Duplicated Code，且有可靠性影响。`sourceparser/runtime.py:120–134` 新增 Python 递归 AST 遍历；相邻 Java/JS/TS 已采用显式 stack。审查用锁定 grammar 复现约 2.4 KB 合法函数的 1,200 项加法表达式抛 `RecursionError`，HTTP child 返回 422，同步终止。改迭代遍历并保留父结构与装饰器。其余静态分析及隔离边界保留。

### T10

1. P1 ADR-0008 冲突：`mybatis/java.go:16–63` 用 Go regex 重解析 Java，违反独立 Python 服务复用 Tree-sitter、Go 管同步权限快照与发布的分工。
2. P1 ADR-0005 违反：`java.go:46` 在 raw source 匹配方法，注释中的 `String find();` 被 `correlate.go:52` 写成 certain Mapper edge；不是可核验语法关系。
3. P1 ADR-0005 违反：`sql.go:58` 将所有非 dynamic 候选设为 certain，`correlate.go:70–74` 保留；CTE `recent` 被当成确定实体表。
4. 判断性 possible Data Clumps：`storage.go:14` 六个位置 string 标识，建议 scope 和 from/to endpoint 类型，避免相同 primitive 参数互换。

这三个文档问题存在于未接入的模块契约，必须在生产接入前修复。worker coordination 的承认不等于修改已接受 ADR。

## Spec

### T05

1. P1 公开验收未完成。Ticket:15 要求真实本地 Git 提交图、受控 GitLab 错误与公开同步/查询入口；validation:18–22 的 PG 用例未执行。force-push test:183 用所有文件相同 embedding 查询后断言旧词零召回，会把合法新文件向量命中误当删除失败；应分别验关键词旧词和两路 source/snapshot/version/SHA 范围。界面测试还保留旧标签。补真实执行，并证明远端/本地 Git 缓存不再有旧提交时旧固定原文仍按已保存版本读取。
2. P2 首轮失败误称“已保留当前发布”。`SourceSnapshotRunView.vue:12` failed 文案无条件包含保留，首次同步失败无 previous SHA；应依真实当前发布条件区分，并验证两种失败。
3. P2 queued ProcessSync 在 unavailable repository 下越过 nil readiness guard，`datasource_source_sync.go:72` 先 GetPublished 导致 panic；Normal DI 有 repo，风险仅指缺能力队列路径，应记录可诊断失败。

### T08

1. P1 合法深表达式因递归报错，既不返回完整结构也不明确降级，阻止发布；父 Spec:84 与 Ticket:12 要求可读原文的可见降级。改有界迭代遍历并增加 parser HTTP /公开同步回归。
2. P2 导入结构遗漏。父 Spec:38 要求 Python 函数、类及导入；runtime:121 仅 class/function，`import os`、`from pkg import helper` 无 import symbols。补语法节点、精确坐标与别名/相对/多行等语料，不执行导入或推断框架行为。
3. P2 独立语料缺多行字符串，公开集成仅 structural success，缺 syntax-error/degraded 文件的发布、两路检索、引用阅读和质量 UI 验收。直接多行字符串 probe 已保持覆盖，因此此项是证据缺口，不能说已证明该输入解析失败。

### T10

1. P1 用户能力缺失。Ticket:14 要求索引字段、关联上下文与引用阅读贯通；没有生产摄取、DI 或检索调用，XML 准入拒绝。必须在本票接通隔离 parser、双索引、授权 relation/context read 与质量/引用，不能推给下一票。
2. P1 注释方法被 raw regex 误认成 certain Mapper edge，违反 Ticket:12 可核验关联。
3. P1 CTE 名成为实体表，违反 Ticket:11 可确定表访问。
4. P2 缺目标不确定边无法保存：Correlate 返回空 target，但 storage:21 拒绝全部此类非 table 边；应持久化 unresolved diagnostic，不能发明可读 target。
5. P2 XML-only statement 无关系：correlate:29 只在匹配 Java 方法内提取 include/resultMap/table；Ticket:7 允许从 SQL statement 开始。先独立提取 XML 事实，再 join Java。

## 总协调下达的 T10 实施决定

保留 ADR-0008 分工。Java 使用现有锁定 Tree-sitter AST；XML 使用成熟 Python Expat 事件解析与真实 byte 坐标，拒绝实体/网络；SQL 使用 SQLGlot 30.20.0 / MySQL dialect。官方发布 pure wheel SHA256 `886fd92eba9bf944dee97164b16760f21576a7f55ef4a9211337903442e8aded`，纳入 requirements.lock 和加工 fingerprint；依赖/venv/grammar 独立，不覆盖已验收缓存。SQLGlot 可提取 AST 及表节点，但语法解析成功不能证明目标库的真实执行，动态/未知行为仍标记不确定。参考 [SQLGlot 官方说明](https://sqlglot.com/) 与 [Python Expat 官方接口](https://docs.python.org/3/library/pyexpat.html)。

Parser 返回局部静态事实、精确区间与诊断，Go 做 hash/range 核验及固定 snapshot facts join、版本绑定/权限/发布。关系随完整 snapshot staging；失败不可见，空发布不留当前关系，旧问答 pin、撤销及 clear 规则不变。每条关联两端均需满足原始组合 targets，不能因 Mapper 可读就绕过 XML 文件/tag 范围。优先复用现有源码读取/命中上下文而不增加 Agent 模式。000107 可保留并强化合同，未知 target 仅是诊断。

本票补真实 Sync→Git→parser HTTP→PG 双索引→scoped read/citation，覆盖动态/重复/缺失、跨仓同名、权限、pin、增量/空发布/clear/失败保持；代表 PushSchedule/FreeTutor 只静态读，不提交内网业务源码。源码大仓库与真实模型性能仍留最终整体验收，不能用受控 fixture 宣称完成。

## 修复与后续验收

三个实现对话已并行恢复。T05 补环境验证可只读复用 integration frontend/node_modules 与既有 PG 容器，测试 DSN 在进程内读取该专用容器配置，禁止打印或写入文档。各测试独立 schema，容器不重启/删除；共享 grammar 只读。

总协调收到新冻结 commit 后以本轮原提交为增量复核范围，分别更新两轴 finding 状态，并核对原 base 到最终提交的完整要求。通过后集成，执行跨票公开验证及必要完整检查，再更新 GitHub。当前记录是首轮审查/修复派发，三票仍待验收。
