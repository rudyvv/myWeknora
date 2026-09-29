# T10 b3a5744d 修复提交双轴复审

2026-09-29。用户明确额度恢复并要求继续总控及三个执行对话。执行者已主动 READY_FOR_REVIEW 并冻结干净 b3a5744df7ed942c15fbb83c4a908c781b2fef2c，基线为已批准 3adaa587d16651a6d48f123d73c1e5cceaf75271；命令 `git diff 3adaa587...b3a5744d`。完整提交 fc826c19/fd61229c/132ae696/68305937/36d5ffff/984abc5d/4e12109c/b3a5744d。Root gh 核对 #18 五项AC与本地ticket一致。

两个独立 GPT-6 Sol/high reviewer 因额度中断，恢复后从原停点继续；未将正在执行的新修复混入冻结结论。功能修复继续由原 GPT-6 Luna/xhigh 执行对话完成。

## Standards

硬违反0，判断1；最严重P3。

旧P1已修：基于Java receiver/短类型名称的Mapper调用绑定已移除，符合ADR-0005“不能把候选关系写成已证实调用”。

- **P3 possible Duplicated Code。** source_snapshot.go:116–118/126–128 两次复制同构 source_snapshot_members → source_file_versions → source_files JOIN 与快照/tenant/source/KB校验，仅替换端点字段。建议抽取私有endpoint校验函数，保留各自错误语义。判断性建议，仓库无必须抽取的硬规则。

其余完整smell baseline与ADR检查未发现充分定位的新违反。Reviewer仅冻结blob/diff阅读和环境探测，未重复Root PG/HTTP/frontend套件。

## Spec

partial2、wrong1、scope creep0；最严重P1。

1. **P1 partial：Agent实际模型路径丢失结构分析。** T10:14“关联上下文与引用阅读贯通”。source_analysis进入Data/Output，但model_output.go:252–274仅chunks重建，未读analysis。Root真实合成函数反例先确认原chunk正文出现，再断言statement/diagnostic/target/cursor四marker，全部缺失。应接入实际 Registry.ModelToolResultForTool sharedformatter，保留有界输出与转义、合法证据handles，不仅测试Data或tool.Output。
2. **P2 wrong：关系续页遇certain跨文件target被原游标拒绝。** 同一AC；source_analysis.go:32将原文件cursor写ctx，:66复用此ctx读取target；source_file.go:106–108校验cursor绑定不同file/version必拒。现有续页probe无target未覆盖。应主分页ctx与target读ctx分别处理，target清游标但保留同次SourceRead、版本pin和权限，不放宽边界。Reviewer为静态确定性追踪；Root随后临时PG反例实际FAIL，17.109s：首个20行页面成功，第二页含验证过的跨文件endpoint即返回 invalid source relation cursor。
3. **P2 partial：嵌套include/resultMap引用链遗漏。** T10:11“提取…include/resultMap”。mybatis_parser.py:505–523仅statement后代include，:535–549 fragment不处理include；:487–489 resultMap仅id。Reviewer冻结blob内存探针确认fragment→fragment include、resultMap extends及association引用均无facts/diagnostic。补现有Expat事实和可确定关联，提供原文range/owner/target；缺失/重复/循环/动态保持不确定或明确诊断，不解释执行动态SQL。

旧五项已有实质修复：不再发/接收java_field/java_mapper_call；100行keyset与明确截断，Agent20行；derived UPDATE探针返回orders+customers；5个代表文件聚合验证记录已补；Agent/UI已增加有界分析，但实际模型消费和跨file续页仍被上述问题阻断。AC1部分、AC4部分；AC2/3静态符合保守合同，AC5记录及超大覆盖已核验。本reviewer未重复读取业务原文或PG/HTTP/UI套件。

## Root冻结复验

- 32项HTTP tests PASS，31.218s。
- TestSourceMyBatisMapperXMLFactsRelationsScopesAndIndexes PASS，17.339s。独立PG schema、固定解析器、双索引、关系scope/paging/pin；合成无目标probe不能代表跨文件续页通过。
- 临时跨文件续页反例：在冻结测试fixture中仅增加45个已有Mapper边的分页探针，全部沿用真实已验证双端版本/range，first page成功、next page失败，Error invalid source relation cursor，17.109s。临时测试改变，不是产品改动。
- SourceCodeView两项组件tests PASS；新增KnowledgeChunksList.test FAIL，_ctx.$t is not a function，测试挂载缺全局翻译注册。未推断运行产品缺陷。修复测试环境后须重跑实际UI断言。
- app/node vue-tsc检查PASS。额度中断时第二项检查未执行；恢复后真实接续完成，没有降低审批约束。
- TestRootT10ModelReceivesSourceAnalysis临时反例FAIL，1.117s；真实调用NewRegistry(true).ModelToolResultForTool("list_knowledge_chunks", result)，四marker缺失，原正文出现。仅合成数据、临时导出，不改产品执行分支。

使用独立测试数据库与锁定venv/grammar，不读取旧容器凭据，不安装共享依赖或改缓存。frontend依赖只读共享，产物写临时导出。三项Spec和测试setup失败已全部交T10修复；P3可选。#18保持OPEN，不集成、不增加完成计数。

两轴计数：Standards硬0/判断1，最严重P3；Spec partial2/wrong1，最严重P1。各轴独立，不合并或重排。
