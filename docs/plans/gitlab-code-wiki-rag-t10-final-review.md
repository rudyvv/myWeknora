# T10 最终提交首轮审查与修复派发

2026-09-29。用户要求审查已结束的 T10 执行对话。审查起点沿用已确认的 3adaa587d16651a6d48f123d73c1e5cceaf75271；冻结最终提交 4e12109c5900336ad9d2c5bc049a6e3fecf32086，工作树当时干净。完整审查命令 `git diff 3adaa587...4e12109c`，提交序列 fc826c19、fd61229c、132ae696、68305937、36d5ffff、984abc5d、4e12109c。

按 code-review 技能，两位独立 reviewer 保持 GPT-6 Sol / high，分别审查 Standards / Spec；T10 执行对话保持 GPT-6 Luna / xhigh。根对话通过 gh 读取了 Issue #18，其五项 AC 与本地已批准 ticket 一致；Spec reviewer 自己的 gh 身份读取失败，采用该冻结本地 ticket 和父 Spec，没有擅自恢复认证。两位 reviewer 只使用冻结 blob；worker 后续改动不属于本轮结论。

## Standards

**硬违反 1；判断性 finding 1。**

**P1：候选 Mapper 调用被发布为 certain。** `internal/source/source_relations.go:144–167` 按同一文件所有字段匹配 receiver 后缀，将字段类型缩成短名；192–193 只要 XML namespace / statement 唯一就生成 certain。parser facts 未携带声明类型、词法 receiver 绑定；无关 FQN Mapper、参数遮蔽同名字段、同文件另一类借用字段均可产生假确定关系。违反 ADR-0005:8“不能确定的关系必须标注为未确定，不能把候选关系写成已证实调用”。需核验完整类型与词法绑定，或保留有原因的 uncertain 候选；快照内唯一不证明 Java 调用目标。

**P3 possible Duplicated Code。** `internal/application/repository/source_snapshot.go:115–129` 两次复制同一 origin / target endpoint membership 查询。建议共享 endpoint validator，保持 snapshot / tenant / source / KB 约束一致。该项是判断性建议，不是文档硬规则。

原 CTE qualification / scope 与注解兄弟类型误遮蔽已修复；SQL table-fact helper 已提取，旧 Go Java/XML/SQL 解析模块已移除。同步、原子发布与关系阅读已接线，两端均受授权快照范围约束，未发现新的关系读取权限违反。Standards reviewer 仅静态审查，没有独立重跑 worker 的 parser / PG 套件。

## Spec

**剩余 4 项：2 P1、2 P2；无范围膨胀 finding。**

- **P1：没有可靠 Java binding 的 Mapper 调用变成 certain。** `internal/source/source_relations.go:149–167/192–193` 按短类型与 receiver 后缀匹配。冻结 parser facts 与 Go CorrelateSourceFacts 的独立探针实测参数遮蔽、other.mapper、无关全限定 Mapper 类型、重载方法四种情况均生成 certain XML edge。违反父 Spec:82“可确定框架关系需有证据”。未知绑定须保持 uncertain。
- **P1：关系上下文与质量未贯通公开消费者。** `knowledge_source.go:45` 普通 HTTP 读取建单文件 scope，隐藏跨文件关系；`knowledge_search.go:1069–1081`、`list_knowledge_chunks.go:214–227` 输出原文/source evidence，没有关系/诊断；`SourceCodeView.vue:60–69` 未显示诊断，还把有效 text_fallback XML 标为语法错误。违反 T10:12“质量提示”、:14“关联上下文与引用阅读贯通”。同一公开字段还在 source_file.go:84 静默 Limit(500)，没有分页或完整性标记；这是关系上下文输出部分，原始 facts/statement 没有据此丢失。500 上限为静态 SQL trace，未运行 1,200 项 PG 复现。需实际 Agent / HTTP / UI 消费测试，保留两端范围及有限输出的明确截断状态。
- **P2：派生表 UPDATE 来源回归。** `sourceparser/mybatis_parser.py:364` DML JOIN 仅接收直接 exp.Table。独立冻结 probe `UPDATE orders o JOIN (SELECT * FROM customers) c ON o.id=c.id SET o.x=1` 只返回 orders，先前检查点可返回 orders 和 customers。违反 T10:11“可确定表访问”。需恢复对子查询物理来源的作用域提取，排除派生别名。
- **P2：代表仓库验收证据缺失。** `datasource_source_integration_test.go:83` 为一个 synthetic PushSchedule Mapper。真实代表文件存在，但提交中没有两个 PushSchedule Mapper 与 FreeTutor 的实际验证记录，T10:15 仍未核验。不能将同名 synthetic fixture 当成代表语料。

Spec reviewer 独立运行冻结 parser 与隔离 Go 探针；没有运行 root 的整套 HTTP / PG 用例。既有 CTE scope 与 sibling annotation 反例已经通过。公共消费与代表语料 AC 未满足，因此整票验收仍阻断。

两轴计数：Standards 1 硬违反 + 1 判断性 finding，最严重 P1；Spec 4 finding，最严重 P1。两轴独立保留原评估，不合并或重排。

## 总协调独立验证

- 32 项 parser HTTP tests：PASS，26.132s。
- `TestSourceMyBatisMapperXMLFactsRelationsScopesAndIndexes`：PASS，21.992s。实际本地 Git、隔离 Python parser、独立 PostgreSQL 的关键词和向量索引、关系 staging、失败保留、空发布与旧 pin 阅读均执行；外部 GitLab / Embedding 为受控 fixture。
- `git diff --check 3adaa587...4e12109c`：PASS。

数据库继续使用用户选择的独立测试库；未读取旧容器凭据。根对话没有重跑已无必要的完整 Go / UI suite；worker 报告 broad service suite 有三项 Windows SQLite 临时文件清理失败，此处不称这些失败已由独立基线证实。修复前不集成，也不关闭 #18。

## 已派发修复

T10 已重新进入执行。没有 parser 提供的可核验 binding 时，Mapper 调用关系收口为 uncertain、无可读 target，或移除超出本票的调用 join，不自建 Java 类型解析器。关系上下文优先经既有 Agent SourceRead ctx 与 knowledge.GetSourceFile 固定版本 API 获取，再按独立目标 Range 形成可核验证据，不将拼接 SQL 冒充原 Mapper 区间；两端继续遵守组合 file/tag/source 范围、当前权限、pin 和 clear。

前端补 typed facts/diagnostics、真实质量及安全关联入口；默认单文件 HTTP 范围保持，跨文件入口只能消费同次问答已核验的 target evidence，不默认扩大 KB 范围。输出有界并显式标注/提供后续关系阅读能力。T09 已收到 SourceCodeView / SourceFileView 类型重叠协调，两个 worktree 优先独立 helper/component，根对话负责最终冲突处理。

代表仓库仅静态读取两个真实 PushSchedule Mapper 及 FreeTutor，记录统计、区间和不确定性，不提交内网业务原文或 SQL。陈旧 T10 coordination 文档需同步为真实实现合同。修复提交交回根对话后再次双轴复验；当前仍为 8/22 完成。