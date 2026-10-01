# T13 0af8e564 双轴复审与回填一致性反例

根核对干净冻结 `0af8e56463c56d7fb153429b555a302f9d6af581`，批准起点 `fa0dcad79646dcd08dd39dfe17cfc8eeaaf185b9`。两位独立 Sol/high 分别审 Standards 与 Spec；执行修复仍交原 Luna/xhigh 对话。相对 `8cfd15cc` 的三文件返修修复了完整 snake_case 词及 MyBatis result_map/sql_fragment 完整 ID。

## Standards

硬性违例 0；判断性 P3 两项：融合排序比较重复；Go 新投影与 SQL 回填复制派生规则、上限选择及 namespace 行为易漂移。后者已有实际行为缺口，由 Spec 轴单列，不混合严重性。

## Spec

一项 P2 必修：新建投影按遍历顺序取前 256 个正文词，migration 111 回填按字母排序取前 256 个词。同一 search_version、同一原始 chunk 可以产生不同完整词数组，升级旧数据后合法代码检索命中丢失。违反 T13 同加工版本的新建/回填一致性及代码规范化检索合同。

## 根实际复验

- 旧两项语义反例在冻结代码上通过；固定范围及真实查询计划通过 256.88s，旧派生语义反例通过 8.65s，包 269.689s。使用专用 57521/source_test、独立 schema、真实 HTTP parser；未重启共享服务。
- 新临时 Go overlay 使用合法多行 Java 方法，正文注释包含早出现的 `zzTarget` 和 260 个两字母词。断言投影实际达到 256 词上限，且该标识符不在 full_identifiers，排除精确通道遮蔽问题。新投影规范化谓词命中 1，删派生行后调用真实 migration 111 回填命中 0；实际 FAIL 10.46s，包 15.224s。
- 早先长标识符及单行方法夹具被 T12 拆成多块，未达到词数上限，不能证明该问题；根以达到上限的紧凑多行夹具重新确认。临时测试夹具错误不列产品 finding。
- 本机证据：Temp/weknora-root-t13-0af8-dense.go、dense-overlay.json、dense-compact.jsonl。worker 五项 PostgreSQL 通过报告保留，但不代替新的边界一致性反例。

## 修复合同与决定

未验收、未集成、#21 open。根已批准并派回同一执行对话：migration 111 建立一个有界源码专用 PostgreSQL 派生函数，由 StageFile 现有每块 INSERT 与旧数据回填共同调用。保留 256 词/512 字节约束、事实范围验证、staging 事务与发布完整检查、加工版本及清理寿命；不增加逐块数据库往返，不重写源码、不调用模型或重嵌入。查询侧 Go 的有界解析保持职责独立，删除生产路径已不使用的第二份派生算法。CheckReady 核对函数签名/版本，111 down 只移除该票函数与表；migration 112 留给 T18。

新验收覆盖完整数组一致性，含 255/256/257 上限、重复词、camel/acronym/snake/$、MyBatis 四类及 Java Mapper namespace、UTF-8 路径和越界事实。不能通过取消上限或特判反例词修复。等待新干净 SHA 后针对性复审；无需重跑已通过的无关 Windows 套件。

Standards 硬 0 / 判断 2（P3）；Spec 1（P2）。
