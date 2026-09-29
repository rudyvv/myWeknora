# T10 ffc98fde 双轴复审

冻结提交 ffc98fdeab698d34510f75f557580ddd459f724e，用户批准起点 3adaa587d16651a6d48f123d73c1e5cceaf75271。完整差异 39 文件、十提交。两位 Sol/high 审查员独立检查 Standards 与 Spec；未通过，Issue #18 保持 open，不集成。

## Standards

硬违反 0 项；判断性建议 3 项，最严重 P3。

- possible Speculative Generality：source_relations.go:27 的 mapperDocument 已不再读取 member/resultMaps/sql，却仍填充后两列表；可移除无用存储。
- possible Duplicated Code：agent/tools/source_analysis.go:120 与 modelcontext/source_analysis_output.go:101 的事实字段白名单重复且已分歧，后者的 owner_name/reference_kind 被前者先丢弃。
- possible Duplicated Code：repository/source_snapshot.go:116、:126 重复端点关联和作用域验证。

## Spec

错误/部分实现 1 项，P2；缺失 0、越界 0。

- 父 Spec 第 84 行要求可读但无法可靠解析的文本显式降级。可读的损坏 Mapper XML，例如未闭合的 select，在 mybatis_parser.py:447 抛 ValueError，经 runtime.py:72 和 server.py:107 返回 422，datasource_source_sync.go:180–195 中止整个快照；它既没有质量降级，也未保留该文件为文本索引。应区别语法损坏与 DOCTYPE/外部实体等不安全输入；前者保留准确范围、诊断和可检索文本，后者维持安全拒绝。

上轮三项必修已静态核对：Docker build 阶段现在复制 mybatis_parser.py；直接 resultMap 引用走统一 owner/namespace/target 歧义解析；模型关系页在可达的 20 行 Agent page 内保留关系与 opaque cursor。Root 在干净冻结 ffc98fde 上独立运行 internal/source PASS1.382s、internal/modelcontext PASS4.377s；Worker 报告实际 Docker build target 完成 prefetch 并验证四种语法，无容器启动。本轮未重复 PG 集成链，未把静态 XML 问题冒充实际 HTTP 测试。新缺口已发回原 Luna/xhigh T10 对话，等待新干净完整 SHA。两轴计数独立保留：Standards 硬0/判断3、最严重P3；Spec1、最严重P2。父任务仍 8/22。
