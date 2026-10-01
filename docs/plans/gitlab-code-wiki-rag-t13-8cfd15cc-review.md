# T13 8cfd15cc 双轴审查与独立复验

冻结提交 `8cfd15cce7e7d4315c4daeee1086103c37f1bb51`，工作树干净；批准起点 `fa0dcad79646dcd08dd39dfe17cfc8eeaaf185b9`。两个独立 Sol/high 审查者分别处理 Standards 与 Spec；root 实际验证，Luna/xhigh 返修。

## Standards

硬性违例 0；判断性 P3 两项：

- `knowledgebase_search_fusion.go:75/98` 重复 tier→score→chunk ID 排序逻辑，可共享比较函数。
- `code_search_terms.go:90` 与 migration 111 复制派生规则且同版本结果不同，属于可能的 Duplicated Code / 版本漂移。行为缺口由 Spec 轴分别列出，不混合两轴严重性。

## Spec

两项 P2 必修：

1. migration 111 的 `[^A-Za-z0-9$]+` 先拆下划线，Go 生产与查询则先保留完整 snake_case 再拆分。规范化查询使用 `@>` 要求所有词，回填后丢完整词会退失规范化候选。违反 T13 AC1 的 snake_case 检索及新旧同加工版本一致性合同。
2. 新 StageFile 仅从 mybatis_mapper/statement/java_mapper_method 生成 `namespace#ID`，遗漏 parser 已提供的 result_map/sql_fragment；回填却包含这两类。新快照精确 Mapper ID 覆盖反而弱于旧升级数据，违反 Spec §22 的完整 Mapper ID 检索。

## Root 实际验证

- source、repository 两包通过 5.120s；postgres retriever 包无独立单元测试，不计其为实际查询验收。fusion / code query / rerank 失败回退定向通过 3.014s。
- exact path 抵抗重复 BM25、keyword-only 消费、Mapper statement、真实 GIN 查询计划和幂等回填计数通过 17.51s。
- 混合普通文档与源码范围测试独立第二轮通过 9.98s；同组后续 scope/query-plan 测试在 fixture 的 HTTP parser 同步等待时触发 90s 超时。该测试未通过，不把它直接定性为 T13 产品缺陷；首次未限定时长的运行由 root 中止，亦不算 PASS。
- root 在冻结代码上用临时 Go overlay 跑真实专用 PostgreSQL：`find_by_id` 的规范化谓词新投影命中 1、删投影后回填命中 0；新 XML 的 `demo.ParityMapper#basic_map` 与 `#base_columns` 均不存在、回填后存在。反例实际 FAIL 19.11s，包 24.640s。未修改冻结工作树；没有以编译或行数恢复冒充语义验证。
- 临时证据保存在本机 `weknora-root-t13-parity.go`、`weknora-root-t13-parity-overlay.json` 与 JSON 日志，不含代表仓库私有业务原文。

## 决定

未验收、未集成、#21 保持 open。两个具体反例已直接派回原 T13 对话，要求派生规则语义一致性及真实范围查询回归；不重复 worker 自派审查。Standards 硬 0 / 判断 2（P3）；Spec 2（P2）。
