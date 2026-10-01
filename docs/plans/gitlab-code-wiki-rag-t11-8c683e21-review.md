# T11 8c683e21 最终复审与独立两链审计

完整干净冻结 `8c683e21c3f643378e4271ae29c8faf078aa240c`，用户批准起点 `04d99577814d3786b7e004f81124885638dffced`。复用两位独立 Sol/high 审查者，分别复核完整票及最后两文件返修；不重复未变宽套件。

## Standards

文档化硬性违例 0。候选合并保留明确与未解析实现，按文件版本及原文范围去重，所有歧义候选保持 uncertain 且不设置导航目标。

既有判断性 P3 两项：Spring mapping 提取的 possible Duplicated Code；Java/HTTP 关联器职责增长的 possible Divergent Change。未发现新增问题，非阻断。

## Spec

Actionable finding 0。上一轮 Known 方法候选被 unresolved 分支吞掉的 P2 已修；不能将明确父类型声明当作存在潜在第二实现时的唯一可导航实现。真实业务链的动态代理、接口选择和动态 SQL 不确定性保留，不伪造完整确定图。

## 根独立验证

- 原真实 HTTP 混合 Known/Unknown overlay 反例在新冻结上 PASS，包 11.044s。
- 新冻结真实 PostgreSQL 问答快照、范围与 Agent 固定位置读取案例 PASS 15.33s，包 19.762s。
- 根使用冻结的实际 HTTP parser 重新解析两个真实业务闭包，共 30 个文件；第一链移除一个重复输入，包含两个仓库相对路径 Vue 文件。原始业务代码及 SQL 不提交或对外发布；临时审计脚本、输入清单、匿名统计保留本机，可复跑。审计 PASS，包 62.023s。
- 第一链 10 文件：8 structural、2 degraded；独立提取 25 个 MyBatis statement、45 个 SQL table 事实。目标后端方法保留 3 条 uncertain implements 候选、2 条 certain / 1 条 uncertain Mapper statement、4 条 certain 调用、12 条 certain table access。前跳歧义不再遮蔽独立后端证据。
- 第二链 20 文件均 structural；4 个 Mapper/XML、46 个 statement、32 个 table 事实，关系保留 46 certain / 2 uncertain Mapper statement、16 certain / 16 uncertain table access。选定入口实际可直接调用 Mapper，不要求或虚构 Service 中间层。
- 两链所有 uncertain 关系均核对无目标文件/版本导航 ID。统计是局部静态闭包验证，不代表全仓动态调用图或性能验收；代表仓库全量规模验收仍属 T22。
- 根临时审计首次误用不存在的 sql_table_access fact 名导致断言失败；按实际 sql_table 合同修正审计后重新运行通过。原 Vue 失败是旧审计误传主机绝对路径，非 T09 产品问题。

## 合并意图与验收门禁

四文件冲突分别保留 T06/T11 独立 PG 测试、T12 文本回退和 embedding profile / T13 检索版本、T11 Vue facts，以及 T11 的 Java/JS/TS 规则版本。ProcessingVersion 综合升为 v5，避免沿用任一旧产物；不恢复硬编码 cl100k 作为通用 embedding 上限。

合并后真实 HTTP→Go 混合/未解析父类型验证 PASS，包 17.176s；真实 PG 业务范围案例 15.20s、历史 pin/裁剪/旧索引 GC 9.97s，包 29.584s。合并边界另验证模板文本回退、Vue 原文位置事实和巨大 MyBatis statement 三个真实 HTTP 案例，3/3 PASS 4.082s；Go 业务事实接收、完整索引 token、profile 拆分及路由四项 PASS，包 3.570s。Python 语法编译、Go 格式及 diff 检查通过。

正式双轴、冻结独立复验和必要合并交叉验证全部通过，允许发布/关闭 #19 并派 T15，累计 16/22。其余已通过 source/modelcontext/Agent/UI 定向结果见上一轮 0704 审查，不重复运行。

Standards 硬 0 / 判断 2（P3）；Spec 0。
