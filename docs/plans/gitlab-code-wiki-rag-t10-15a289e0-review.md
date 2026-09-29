# T10 15a289e0 双轴复审与独立验证

2026-09-30。干净完整冻结15a289e064fca5cdc65299f0f707181d305bc660，沿用用户批准起点3adaa587d16651a6d48f123d73c1e5cceaf75271。修复轮次b3a5744d不替换全票基线；diff `git diff 3adaa587...15a289e0`，九提交fc826c19/fd61229c/132ae696/68305937/36d5ffff/984abc5d/4e12109c/b3a5744d/15a289e0，39文件。两位Sol/high独立审查完整范围，root独立必要验证。未通过；Issue #18保持open，不集成。

## Standards

硬违反0，判断性建议2；最严重P3。完整39文件对照AGENTS/CONTEXT/ADR0003–0009/domain/dev-guide/implementation-plan及完整Fowler baseline；跳过工具强制规则。

- P3 possible Duplicated Code：Agent事实过滤source_analysis.go:120与modelcontext/source_analysis_output.go:103重复schema和字段边界；后者新允许owner_name/reference_kind，前者却先丢弃，实际producer不会把这些字段交给formatter。可共享typed fact-summary schema，保留不同消费者的输出预算。
- P3 possible Duplicated Code：repository/source_snapshot.go:116及:126重复成员/版本/文件关联和snapshot/tenant/source/KB核验，只origin/target不同。可抽取私有endpoint验证，保留各自错误提示。

先前ADR0005的词法mapper-call确定性问题仍已修复。新模型入口、cursor清理及嵌套引用无新增明确标准硬冲突。本轴仅静态审查。

## Spec

wrong/partial3，missing0，scope creep0；最严重P1。

- P1：标准Docker build在prefetch前失败。父Spec:108要求“标准 Docker 交付独立限资源解析组件”。runtime.py:9新增import mybatis_parser，prefetch.py:9导入runtime。Dockerfile:7只复制runtime/prefetch，:8立即执行prefetch；mybatis_parser在final :15才复制。完整source目录HTTP测试不覆盖build stage布局。
- P2：直接statement/resultMap关联绕过歧义检查。T10 ticket:12要求“重复 ID/namespace 或缺失目标保持不确定和质量提示”。source_relations.go:119–127对statement.ResultMapRefs只核target唯一；重复statement ID可得到certain resultMap，重复source namespace也可得到certain外部target。:202–218的owner/source namespace规则仅用于新增reference-fact路径。
- P2：模型摘要裁剪使关系续页跳过证据。T10 ticket:14要求“关联上下文与引用阅读贯通”。source_analysis_output.go:67–68先保留repository cursor，:81–84因12KiB预算删除relation尾行；cursor仍跨过原整页。terminal页还可能截掉关系且无继续入口。须在确定cursor的边界进行预算分页，或提供涵盖被省略行的可靠补读，不能在renderer伪造opaque token。

上轮三项缺陷：实际模型sidecar遗漏、跨文件游标错误及嵌套include/resultMap提取已修复，并经过root实际路径验证。保持静态可证实关系范围，不解释为完整运行时调用图。

## Root独立验证

验证运行于固定SHA临时导出及用户批准独立loopback数据库57521/source_test。只使用已有Python/grammar/frontend依赖；没有读取旧容器凭据、安装依赖或改共享容器。前端build-info单独写临时目录。

通过：
- internal/source PASS0.904s；internal/modelcontext PASS3.339s（包括真正Registry.ModelToolResultForTool sidecar回归）。
- 真实PG TestSourceMyBatisMapperXMLFactsRelationsScopesAndIndexes PASS14.850s，覆盖parser/client/correlator、双索引/授权、嵌套引用及下一20关系页的跨文件target固定位置。
- 新JavaHTTPContract.test_mybatis_fragment_and_result_map_references_keep_owner_and_exact_range PASS0.724s；测试仅启停自有临时parser，不影响共享58082。
- SourceCodeView两个实际组件测试通过，KnowledgeChunksList正确路径实际组件测试一个通过（173.7511ms）；app/node两个项目独立typecheck均通过。

三个缺陷独立反例：
- 与Docker build stage一致的临时目录仅含runtime.py/prefetch.py，已有lockedPython `-E -B prefetch.py --help`实际失败ModuleNotFoundError: mybatis_parser，无下载/prefetch行为。此为build导入布局取证，不声称已执行完整Docker构建。最初-I命令会连runtime路径一起排除，未用作结论。
- 临时Go correlator两反例实际FAIL1.119s：重复statement产生2条certain target=xf；重复source namespace引用唯一外部resultMap产生certain target=xc。
- 临时实际Registry模型输出反例FAIL3.394s：20条正常UUID/240字中文target snippet关系仅保留8条，却保留指向第20条之后的cursor，跳过12条。初始ToolResult缺display_type未进入预期formatter，修正真实production形状后取得此证据。

默认沙箱曾拒绝Go缓存和Node用户信息读取，正常审批后验证通过，未把环境错误作为代码finding。Worker HTTP33、其它Go局部套件结果与Windows环境限制见其coordination报告；本轮只独立重复新增引用HTTP和相关链，不声称全仓suite绿色。

## 修复与集成合同

三项必修及全部取证已发回同一个Luna/xhigh对话，要求真实stage部署回归、两种歧义路径及实际模型预算/continuation回归。保持有界输出和opaque cursor，由返回实际关系页的边界决定其continuation；不靠无界加预算。P3本身不独自阻挡验收。

T10基线没有T08 Python/T09 Vue，后续root集成时保留五种grammar、Node SFC合同、T09 Region与完整quality helper，同时保留T10事实/诊断/关系/cursor UI/API。不要求回退合法功能或把未完成消费接口移到后票。

两轴计数独立：Standards硬0/判断2，最严重P3；Spec3，最严重P1。等待新clean SHA主动READY，未push或关票。
