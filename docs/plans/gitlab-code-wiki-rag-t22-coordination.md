# T22 执行合同：代表仓库与整体验收

已批准 Issue #30 / Spec #8；正式双轴审查起点 1d32c08e36d255400c21e8863f5249d23a91afd3（已推送且 #29 已关闭的 T21 集成）。总控负责规划/合同、Sol high 两轴、独立复验、集成与关票；原三个执行对话继续 Luna xhigh。三份工作先并行实现可重复验收工具/数据，最终现场模型验证等待用户确定已运行的验收环境；不能把测试模型或静态扫描当作生产 RAG/Wiki 验收。

## 固定范围与分工

- 原 T09 / a8ea / codex/t22-representative-runner：只新增 scripts/source-acceptance-run.ps1、其独立测试及 docs/deployment/source-acceptance.md。实现有界可重复的现有 API 验收 runner：正常认证从进程环境安全注入、不输出凭据；明确显式知识库/源码来源/模型配置；完整预览与发布结果、30题 top10 证据核对、1/10/100 文件增量指标和同预算文本基线的报告输入/输出合同。真实 API 路由必须从现有代码核对。不修改已有 KB/模型配置、不借用现有业务容器凭据、不盲目触发大模型全量。目标提交先用隔离假 HTTP 服务检验分页、超时、scope、报告字段与错误路径；真实现场运行由总控在明确配置后执行。无法自动测量峰值内存/真实 token/模型限制时记录 unknown 和外部观测输入，不能编造零或性能结论。不要为控制增量修改原业务仓库；1/10/100 改动只能在独立验收副本中执行。
- 原 T06 / t16-impact-loader-errors / codex/t22-evidence-benchmark：只新增 docs/acceptance/source-representative-questions.json、说明文档和独立数据校验工具/测试。可只读 D:/Project4-evip/code 代表源码，禁止整仓复制/原文进入 GitHub。按现有检索/引用合同整理恰好30题：10符号/路径、10业务链、10前端/SQL；包含 getPushSchedule/FreeTutor、巨型 Java/Mapper、Vue2，及四语言补充语料需要的 TS/Python 题目。每题记录问题、正确证据的相对路径/行范围/符号/源文件 hash 和验证理由摘要；不存整段源码。每题必须有真实代码支撑，不从名字推测业务。静态文件扫描只是候选清单，实际入库规模以生产过滤预览为准。若本地项目缺 TS/Python，用已批准首期测试语料明确标为 supplement，不假装代表仓库内存在。数据校验覆盖分组数/路径范围/行范围/文件hash/引用定位与无效输入，不运行模型或共享 PG。题目状态 draft_for_human_confirmation，总控核对后最终用户人工确认，不能预填检索命中。
- 原 T10 / 2119 / codex/t22-agent-fault-matrix：只新增 scripts/source-acceptance-regression.ps1、测试与 docs/acceptance/source-fault-agent-matrix.md。按已批准矩阵映射和编排现有真实 gates：增删改/rename/force-push、异常鉴权/分支、双索引失败、重复/持续通知、崩溃恢复、旧Wiki job/CAS、历史/GC/清除、多源scope、普通文档以及 wiki/RAG/wiki+RAG 三种Agent。检查每格有实际已有测试还是需要现场/补充，明确未执行/失败/通过及证据路径。只实现工具和确有空缺的新独立测试，不改产品，不删除弱化已有断言，不用字符串搜索/编译冒充行为绿。需要 NEW integration test 时先向总控给具体缺口/文件名，避免共享 fixture/产品冲突。编排 script 显式限定 runner 与目标、输出安全聚合结果；不读取 runner 内容/DSN/旧凭据，不自启停共享依赖。PG唯一总控57822，执行者不运行 PG；native 单文件可正常测试，一次权限/缓存失败后报根，不新建绕过缓存。

## 接口与验收纪律

三个工具共用版本1报告数据：question_id、category、source/snapshot/commit、预期 path/start_line/end_line/hash、retrieved top10 evidence 与 matched/unknown；scope 必须是显式指定 KB/source/published snapshot。每题判定以正确证据及位置为准，不按回答措辞命中；27/30 阈值不能称为完整 Recall。baseline/增量报告记录实际 hardware/model/context/tokenizer/limits 与 selected file/byte counts，计时分阶段，估算和实际消耗分开；没有数据就 unknown。

所有执行对话问题直接根线程 01a0e5ba-ea2d-7ea1-85e5-cb87f159495d；工具实际拒绝时留下 NEEDS_ROOT 给 compact 轮询，不要求用户找内部脚本。禁止无进展 ACK 循环/逐过程 GitHub 评论。完整 clean SHA + owned 文件 + 实际 tests/未执行限制才 READY_FOR_REVIEW；根验收前不能派新票或自称整票完成。现场 Hook、真实模型/CA 与大仓性能必须单独记录；可重复工具完成不等于 T22 整票通过。
