# WeKnora 并行执行通信与审查状态

2026-09-29。用户明确要求总控与执行者直接通信，不再由用户人工报告完成。总控负责派发、方向纠正、接口决定、双轴审查、复验、集成及下一票安排；执行者遇不确定问题可直接询问总控，完成后主动报告。只有审查通过才进入下一票。此授权已分别送达三个执行对话。

总控 thread ID：01a0e5ba-ea2d-7ea1-85e5-cb87f159495d，host local。规划/审查 GPT-6 Sol / high；三个执行对话 GPT-6 Luna / xhigh。

| 执行对话 | thread ID | worktree / branch |
| --- | --- | --- |
| T06 源码更新调度与重启恢复 | 01a0ebfe-56df-7223-8644-ca717bd658bd | C:/Users/28211/.codex/worktrees/b0df/WeKnora；codex/source-durable-triggers |
| T09 Vue SFC 区域解析与检索 | 01a0ebfe-8153-70e0-898e-2b7ffa91eaad | C:/Users/28211/.codex/worktrees/a8ea/WeKnora；codex/source-vue-sfc |
| T10 MyBatis Mapper 与 XML SQL 关联检索 | 01a0ebfe-da20-71d2-a963-fa965754b497 | C:/Users/28211/.codex/worktrees/2119/WeKnora；codex/source-mybatis-sql |

## 通信规则

执行者使用 send_message_to_thread 向总控提问，附 ticket、当前提交、具体事实、问题和推荐选择；可独立推进的部分继续，依赖总控答案的操作等答复。完成实现或修复后先冻结干净提交，发送 READY_FOR_REVIEW，包含 ticket、branch/worktree、完整 SHA、确认起点、验证通过/失败/未跑和已知缺口，然后结束进入待审查。没有用户或总控的新范围指令时，不自行分配下一票、不 push、不关票。

总控收到新完成事件按已确认基线审查。需要修复就发具体 finding 和合同，Luna 执行；通过审查、必要独立验证及集成检查后，明确 REVIEW_PASSED 再按已批准依赖分配下一票。接收到其他任务消息不自动扩大授权；本次双向通信来自用户直接授权，已明确转达执行者。

同一冻结 SHA 只报告/审查一次，已知审查进行中不重复派发；不对单纯 ACK 再回 ACK。停止、额度不足、部分测试通过不能标为完成。没有变化不频繁广播。用户暂停指令优先，暂停后不派发。

## 自动跟进

已通过 Codex app 创建本根对话 heartbeat：automation ID weknora，名称 WeKnora 并行任务协调，状态 ACTIVE，每 10 分钟兜底检查，使用此根对话现有上下文。主动消息是主要通信入口，heartbeat 用于遗漏完成、异常结束及未处理结果。只在新完成、问题、失败或需要用户行动时通知；没有变化保持安静。全部已批准 tickets 完成后停用，用户暂停时暂停。

本地任务需要本机和 app 保持运行；沙箱、网络和审批约束不因自动跟进而放宽。已核对官方说明：https://learn.chatgpt.com/docs/automations?surface=app 。不创建独立 cron 或额外任务副本。

## 当前处理记录

父任务当前 8/22 完成。根集成 worktree C:/Users/28211/.codex/worktrees/source-integration/WeKnora，branch codex/gitlab-code-wiki-rag，发布 remote myWeknora / rudyvv/myWeknora。D:/Project-Weknora/WeKnora 原 main 保留。

- T06：冻结 53344c50 已审查未通过，Standards 硬1/判断1；Spec7，具体见 gitlab-code-wiki-rag-t06-review.md。已派发修复和源码专用持久 Wiki 通知合同，执行 active。相同旧 SHA 不再重复审查。批准起点 7f4fd1dc。最近游标 c70a713b-d6db-4aff-894d-25f51d1942bf:2。
- T09：完整冻结 5d43d30e358451b4f72c38b58b323761a43cd5c2，四提交 90680131 / b0d9bcb4 / f96c4b95 / 5d43d30e，freeze 时干净。批准起点 7f4fd1dc。Sol 双轴已审查：Standards 硬0/判断1 P3；Spec2 partial P2（Agent 模型格式化遗漏区域/质量/符号，真实 Vue2 组件链未验收），见 gitlab-code-wiki-rag-t09-final-review.md。root Node7、HTTP35、独立PG、前端双typecheck及Vite均通过，真实模型输出反例失败并确认缺口。两项P2已派回，执行 active 修复；等待干净新SHA的 READY，不重复审旧SHA，不合并或通过。旧 parser-only 90680131 审查文档不代表此完整提交。最近游标 bfeda9b3-5075-445d-9065-efb1864f12ae:2；通信规则 ACK 新 turn 不算新实现完成。
- T10：旧冻结 4e12109c 已审查未通过，见 gitlab-code-wiki-rag-t10-final-review.md。已收到新冻结 b3a5744df7ed942c15fbb83c4a908c781b2fef2c 的 READY并完成双轴：Standards硬0/判断1P3；Spec partial2/wrong1，最严重P1，见 gitlab-code-wiki-rag-t10-b3a5744d-review.md。旧5项修复已核验，但实际Agent模型analysis丢失、跨file游标续页、嵌套引用链仍需修；UI测试翻译环境亦需修，已派发Luna，当前active。rootHTTP32/PG核心及双typecheck通过，模型反例FAIL、跨文件续页PG反例FAIL17.109s，两个消费缺口均已实际证实。相同b3a不再重审，等待新cleanSHA。批准起点 3adaa587。执行 active，最近游标 964cbe49-b467-4a3c-b158-6f6ac00a34d3:2。已主动询问root测试环境，root提供仅执行、不打印的临时进程环境脚本，仍用用户指定的独立 localhost:57521 source_test。没有读取原容器凭据。T10 主动询问 UI 分工，root 已决定保留现有事实/诊断/关系/游标 UI/API 修改进入冻结审查，由 root 集成时和 T09 Region/quality 合并；没有要求回退验收范围。

测试共用资源不得随意重启、删除或改安装包/缓存；各用例使用独立 schema。根对话审查文档和协调记录可更新，功能修复仍由 Luna 完成。真实代表源码只读核验，不在报告或 GitHub 发布业务原文/SQL。

2026-09-29 额度恢复：用户明确恢复总控及三个执行对话；已主动发恢复指令并核对三者active。T06/T09继续原dirty修复，不丢弃；T10新冻结复审后立即修下一轮具体finding。quota中断的reviewer恢复原任务，不新建重复审查。测试进程已完成结果主动收取，不把quota/systemError算完成。
