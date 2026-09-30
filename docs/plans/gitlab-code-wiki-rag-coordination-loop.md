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

- T06：冻结 53344c50 已审查未通过，Standards 硬1/判断1；Spec7，具体见 gitlab-code-wiki-rag-t06-review.md。已派发修复和源码专用持久 Wiki 通知合同，执行 active。相同旧 SHA 不再重复审查。批准起点 7f4fd1dc。最近游标 c70a713b-d6db-4aff-894d-25f51d1942bf:17。
- T09：完整冻结 5d43d30e358451b4f72c38b58b323761a43cd5c2，四提交 90680131 / b0d9bcb4 / f96c4b95 / 5d43d30e，freeze 时干净。批准起点 7f4fd1dc。Sol 双轴已审查：Standards 硬0/判断1 P3；Spec2 partial P2（Agent 模型格式化遗漏区域/质量/符号，真实 Vue2 组件链未验收），见 gitlab-code-wiki-rag-t09-final-review.md。root Node7、HTTP35、独立PG、前端双typecheck及Vite均通过，真实模型输出反例失败并确认缺口。两项P2已派回，执行 active 修复；等待干净新SHA的 READY，不重复审旧SHA，不合并或通过。旧 parser-only 90680131 审查文档不代表此完整提交。最近游标 bfeda9b3-5075-445d-9065-efb1864f12ae:17；通信规则 ACK 新 turn 不算新实现完成。
- T10：旧冻结 4e12109c 已审查未通过，见 gitlab-code-wiki-rag-t10-final-review.md。已收到新冻结 b3a5744df7ed942c15fbb83c4a908c781b2fef2c 的 READY并完成双轴：Standards硬0/判断1P3；Spec partial2/wrong1，最严重P1，见 gitlab-code-wiki-rag-t10-b3a5744d-review.md。旧5项修复已核验，但实际Agent模型analysis丢失、跨file游标续页、嵌套引用链仍需修；UI测试翻译环境亦需修，已派发Luna，当前active。rootHTTP32/PG核心及双typecheck通过，模型反例FAIL、跨文件续页PG反例FAIL17.109s，两个消费缺口均已实际证实。相同b3a不再重审，等待新cleanSHA。批准起点 3adaa587。执行 active，最近游标 964cbe49-b467-4a3c-b158-6f6ac00a34d3:13。已主动询问root测试环境，root提供仅执行、不打印的临时进程环境脚本，仍用用户指定的独立 localhost:57521 source_test。没有读取原容器凭据。T10 主动询问 UI 分工，root 已决定保留现有事实/诊断/关系/游标 UI/API 修改进入冻结审查，由 root 集成时和 T09 Region/quality 合并；没有要求回退验收范围。

测试共用资源不得随意重启、删除或改安装包/缓存；各用例使用独立 schema。根对话审查文档和协调记录可更新，功能修复仍由 Luna 完成。真实代表源码只读核验，不在报告或 GitHub 发布业务原文/SQL。

2026-09-29 额度恢复：用户明确恢复总控及三个执行对话；已主动发恢复指令并核对三者active。T06/T09继续原dirty修复，不丢弃；T10新冻结复审后立即修下一轮具体finding。quota中断的reviewer恢复原任务，不新建重复审查。测试进程已完成结果主动收取，不把quota/systemError算完成。
后续执行进展（非验收）：T09 报告真实三Vue组件关键词3/3、向量3/3、6次授权读取、6次模型证据切片已通过，正在固化安全统计与提交，尚无新cleanSHA，不能按执行中的产物审查/合并。T06 数据源与repository检查通过，但 consolidated PG新失败仍在定位，不能标绿；root已要求保存临时完整测试结果，避免为输出截断反复跑整组。T10 新回归与修复继续active。三者均无新READY，不重复审旧冻结。

下一批候选只在对应票审查、独立验证、集成及关票完成后派发：T09/T10均验收后优先T11业务链；T10验收后T12有界切块；T06验收后T17预算/并发/恢复或T20 Hook；T13排序和T18历史已具原依赖条件，可用于空闲线。以当时具体依赖与公共文件冲突确定安排，不提前分配未解锁票，不因空闲跳过当前失败。已向用户询问后续review起点是否统一为每票启动时的已验收集成提交，尚待答复；当前三票起点已明确，不受该问题影响。
2026-09-30 T09 新READY：干净冻结 c56b1c22e026ab5571ab97779ba5e3f65eb0e88d，已批准起点7f4fd1dc。t09_c56_standards/t09_c56_spec两个Sol/high复审已完成：Standards硬0/判断1P3，Spec旧2P2已修但新1P2为空外置script的未读/拒绝信息丢失。见gitlab-code-wiki-rag-t09-c56b1c22-review.md，不能重复审此SHA。root modelcontext PASS1.869s，真实三Vue PG PASS11.252s；新P2已派回。SourceCodeView现有component test实际FAIL（手动SFC加载器找不到新增SourceRegionBadge.vue），仅证明test setup未适配，不声称产品运行坏。已交T09补实际子组件加载与Region DOM断言，worker可单独修复交下个cleanSHA；parser/PG无变化部分不必重复。c56不集成。T06仍有两项状态断言正在定位；T10继续active修复，无新READY。

最新游标：T09 bfeda9b3-5075-445d-9065-efb1864f12ae:33，继续parser状态和badge test修复；T06 c70a713b-d6db-4aff-894d-25f51d1942bf:23，20项PG已报PASS146.368s，正在记录并冻结，尚无新READY；T10 964cbe49-b467-4a3c-b158-6f6ac00a34d3:19，仍active无新READY。只在新SHA/问题/失败事件动作，marker变化不等于完成。

2026-09-30 T06 新 READY：已核对干净冻结 d56ab04ca3e99fa4df22c0860b18d94cf72de12d，批准起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。t06_d56_standards / t06_d56_spec 两位 Sol/high 正在独立双轴复审，root 正在冻结导出复验关键真实 PG 路径。通知 worker 保持冻结，待明确 finding 或 REVIEW_PASSED；同 SHA 不重复派发审查。T06 游标 c70a713b-d6db-4aff-894d-25f51d1942bf:24；T09 bfeda9b3-5075-445d-9065-efb1864f12ae:34 和 T10 964cbe49-b467-4a3c-b158-6f6ac00a34d3:20 均 active 修复，无新 READY。

2026-09-30 T06 d56ab04c 已完成双轴复审，Standards硬2/判断2(P2/P3)，Spec3(worstP1)。root8项真实PG PASS52.451s，但真实失败后的Scheduler启动漏掉error/paused重投递、并发取消被旧结果改回success、旧配置claim取消新配置B均实际反例FAIL，见gitlab-code-wiki-rag-t06-d56ab04c-review.md。必修已发回原Luna/xhigh修复，不集成/关票。T06回复ACK不算新READY。
T10新干净冻结15a289e064fca5cdc65299f0f707181d305bc660已接收，全票批准base3adaa587保留。t10_15a_standards和复用t06_d56_spec两位Sol/high独立复审完成（复用agent切换T10 Spec范围，不是混合两轴）。Standards硬0/判断2(P3)，Spec3(worstP1)，root独立必要验证和最终修复反馈记录中，勿重复派发此SHA。T09游标bfeda9b3-5075-445d-9065-efb1864f12ae:35，仍active HTTP/模型/真实badge修复，无新READY。

T10 15a289e0 root验证完成：source/modelcontext、真实PG14.850s、新引用HTTP0.724s、三个frontend组件及双typecheck通过，但Docker build-stage导入布局、重复statement/source namespace的direct resultMap certain边、实际模型20→8条却cursor跨20三反例FAIL，见gitlab-code-wiki-rag-t10-15a289e0-review.md。三必修已直接派回原Luna/xhigh，不集成/关票；worker待审查ACK无需root再ACK。T06游标c70a713b-d6db-4aff-894d-25f51d1942bf:25，active新修复；T10游标964cbe49-b467-4a3c-b158-6f6ac00a34d3:21，此游标为保持冻结的旧ACK，返修消息已发后新turn不得当新READY；T09最新:35仍active。三票待新冻结结果，不新建重复对话。

本轮发布记录：审查文档已随8d769d5e推送现有功能分支；#14评论 https://github.com/rudyvv/myWeknora/issues/14#issuecomment-5894544242 ，#18评论 https://github.com/rudyvv/myWeknora/issues/18#issuecomment-5894544904 ，两票保持open，父8/22不变。最后紧凑核验三对话均active：T06 c70a713b-d6db-4aff-894d-25f51d1942bf:26；T09 bfeda9b3-5075-445d-9065-efb1864f12ae:36；T10 964cbe49-b467-4a3c-b158-6f6ac00a34d3:22。root本轮所有测试会话已收取结果；没有遗留root自建parser/container。待新完整干净SHA的主动READY，由heartbeat兜底遗漏；没有新结果不重播以上review或发送ACK。

2026-09-30 T09 新主动READY：根核对干净44423910800c625b22e7952eb134ff964b0e7652，批准base7f4fd1dc保留。复用Sol/high的t06_standards_review、t06_d56_spec，明确切换T09完整32文件两独立轴。Worker仅3个shim HTTP契约通过，不能代替标准部署；root冻结导出将用已有锁定Docker依赖运行真实HTTP/Tree-sitter/Node及独立PG。UI实际badge loader与空/whitespace外置wrapper证据的新修复均在此轮。正在审此SHA勿重派，worker收到保持冻结，无需ACK。

2026-09-30 T09 44423910复审完：Standards硬0/判断1P3，Spec2 partialP2（按block kind未知模板/样式预处理误报structural；descriptor警告只在file不在被索引模型chunk）。root真实锁定Node7、VueHTTP8/9且1项仅测试指纹明文误断言、Go source/modelcontext、真实合成Vue PG与代表三文件PG、实际Badge及双typecheck通过；两项质量反例已由真实官方Node/HTTP证实。见gitlab-code-wiki-rag-t09-44423910-review.md，已派回同一Luna/xhigh修复含错断言；未集成/关票或计完成。root专用58083已停止，共享58082继续。此SHA不重复审，等新完整clean SHA READY。

2026-09-30 额度恢复后审计：此前T06/T09/T10均因使用上限变notLoaded/failed，无一例被当作完成，当前账号usage已恢复ordinaryUsageAllowed；已向三个原对话按Luna/xhigh发送保留dirty状态和原票续做的消息，无新重复对话。新的紧凑快照均active：T06 c70a713b-d6db-4aff-894d-25f51d1942bf:31，报告原24个真实PG已PASS、正在补最后失败保护；T09 bfeda9b3-5075-445d-9065-efb1864f12ae:39，已提交仅指纹测试修正cf000a94，继续两质量红测和修复、尚无新READY；T10 964cbe49-b467-4a3c-b158-6f6ac00a34d3:27，active续修，无新READY。已恢复先前因自动审批额度不足而未执行的#17评论，成功链接https://github.com/rudyvv/myWeknora/issues/17#issuecomment-5898436078 。先前审批失败仅额度限制，未绕过拒绝；父仍8/22，三票未集成/关票。下一动作仅在具体问题或新完整clean SHA READY出现时读取详情。

2026-09-30 T06 新干净冻结96a4c418与T10新干净冻结ffc98fde经wait_threads最终消息直接识别，两条worker→root报告虽被桌面审批拒绝但不需用户人工转达。T06全票用户批准base7f4fd1dc双轴复审Standards硬1/判断1(P2/P3)、Spec1(P1)：配置预围栏与datasource落库之间崩溃留下state B与持久A，恢复跳过空指针并拒绝后续触发；已交原T06 Luna/xhigh修复。T10全票base3adaa587双轴复审Standards硬0/判断3(P3)、Spec1(P2)：可读损坏Mapper XML返回422令快照失败，违反显式降级；已交原T10 Luna/xhigh修复。T06 worker25真实PG PASS但root五项独立复跑因worker开始修改冻结树而中止，不记PASS；T10 root两Go包独立PASS。见各自96a4c418/ffc98fde-review.md，均未push集成或关票，父8/22不变。T09继续原票修复，无新READY。下一动作等新干净完整SHA。

2026-09-30 T09 新干净c9a20a62从wait_threads最终消息取得，base7f4fd1dc双轴复审Standards硬0/判断2(P3)、Spec2(P2)：边界descriptor warning未降级正文、共享region污染无关块；外置script目标缺soft-delete可读性。root锁定Node/SFC/Tree-sitter真实HTTP11项9过2败（其中1项为错误测试断言），正常Vue两项真实PG PASS19.077s；专用58083已停。T06 新干净072086a0双轴复审Standards硬0/判断1(P3)、Spec2(P2)：凭据轮换窗口会丢已发布Wiki outbox信号且same-target不补；UI queued混淆初次/追赶/重试。先前legacy queued-without-state硬finding已依据批准base无生产者撤回。见t09-c9a20a62/t06-072086a0-review.md，必修均已交各原Luna/xhigh对话，未push集成/关票，父8/22。T10仍在损坏XML修复，无新READY。
2026-09-30 额度再次中断后恢复：三执行对话中 T06/T09 上轮因 quota failed/notLoaded，未算完成；root 已按 Luna/xhigh 在原对话续派原票，保留 dirty 状态。T10 e8ac6518 完整干净冻结已由两位 Sol/high 审查：Standards 硬0/判断2(P2/P3)，Spec1 partial P2（跨文件固定位置阅读 UI 缺口）；root 两Go包独立 PASS，parser HTTP/PG 因本地 venv 缺 sqlglot 与 Docker daemon 不可用未独立复验，worker 原 PG 证据保留。具体见 gitlab-code-wiki-rag-t10-e8ac6518-review.md；两项必修已派回同一 T10 Luna/xhigh，未集成/关票/发下一票，父8/22。
2026-09-30 T09 新干净 b8a2b071 已按用户批准 base7f4fd1dc 完整双轴复审：Standards 两P2（UTF16逐字符映射8MiB易超过768m、空块告警region:null）及一P3，Spec一P2（重复顶层script前块被当structural无区域）。root Node SFC7/7、Go source/repository两包PASS，真实Node双script只保留后块的反例确认；锁定Vue HTTP/独立PG因Docker daemon/57521不可用未跑，新SHA不能沿用旧通过。详见 gitlab-code-wiki-rag-t09-b8a2b071-review.md；必修已派回原Luna/xhigh，未集成/关票/发下一票，父8/22。
2026-09-30 用户确认 Docker 已启动。root 只读核对 daemon 29.7.2，原本独立 weknora-source-batch-two-test 容器因 Docker 中断停于 Exited(255)，root 仅启动该专用容器；127.0.0.1:57521 连通且容器内 pg_isready 接受连接。共享应用和58082 parser 未重启，未读取旧容器凭据。已向 T06/T09/T10 原 Luna/xhigh 执行对话分别通知独立 source_test 可用，要求新增 PG 回归在新冻结前实际运行，T09 parser 用独立端口。三票仍未验收/集成/关票；父8/22。

2026-09-30 T06 新完整干净冻结 84b0b8ee，用户批准 base7f4fd1dc，双轴 Sol/high 审查：Standards 硬0/判断1 P3；Spec 新 P2（Wiki event 已 relay 后凭据轮换、同目标 no-op 仍只持旧代数 pending-op）及 P3（success 日志的 phase 写为 failed）。旧两 P2 原反例已修。root 独立 PostgreSQL 关键3项及前端渲染1项 PASS；见 gitlab-code-wiki-rag-t06-84b0b8ee-review.md。两新项已派回同一 T06 Luna/xhigh；不集成/关 #14/发下一票，父8/22。T09 新干净冻结 0a595184 已收到，保持冻结待双轴复审；worker 缺锁定Node环境，root 将用本机捆绑Node24核验。T10 继续原票修复。

2026-09-30 T09 0a595184 双轴 Sol/high 审查完：Standards ADR-0008 硬2（NODE_ENV=production 丢 SFC 告警、重复块 lang 被清空）/判断1 P3；Spec2 P2（自闭合顶层块被 invalid compiler range 拒绝、合法仓库内 ../ 相对外置 script 被预拒绝）。root 锁定 Node24 官方 SFC8/8、Go source/repository 两包 PASS，且自闭合反例实测失败；见 gitlab-code-wiki-rag-t09-0a595184-review.md。四项必修已派回原 T09 Luna/xhigh，不集成/关 #17。

2026-09-30 T10 1d8c9978 双轴 Sol/high 审查完：Standards 硬0/判断1 P3，旧逐边查询已批量修复；Spec1 P2：从 XML statement 反向看 Java Mapper 时 UI 与 Agent 总选 to_* 自指 XML，不可读 from_* 固定位置。详见 gitlab-code-wiki-rag-t10-1d8c9978-review.md；已派回原 T10 Luna/xhigh，要求 UI/Agent/真实PG 双向回归，不集成/关 #18。root Go 独立运行仍在进行，结果稍后记录。额中断后用户确认恢复，原三个执行对话已按 Luna/xhigh 唤醒且紧凑快照均 active：T06 982c32f0-0d27-4566-aeb6-7b22f1a98238:2、T09 efd2c4b2-fa2e-4eea-97f7-aff026c3423f:2、T10 3eea0c77-0b5f-4fd7-b4e0-4cae44105dca:2。三票均待新完整干净SHA，父8/22。

2026-09-30 root 独立 T10 Go source/modelcontext/repository 三包 PASS，具体时长已写 t10-1d8c9978-review.md。三个审查文档及协调记录已推送功能分支 d190d54c，T06/T09/T10 各自新审查摘要已通过登录的 GitHub 浏览器分别评论到 #14/#17/#18，票均保持 open。T06 活跃回报两项新增 PG 回归先红后绿、继续广泛套件；T09/T10 active。继续待各自新完整干净 SHA，不重复审或派票。

2026-09-30 T06 新完整干净冻结704c1ed7（base7f4fd1dc）双轴复审：Standards README Conventional Commits 硬1、判断1P3（最终集成可 squash 合规）；Spec1 P2：delivered pending-op 若已被旧消费者 claim，同代数更替时原地改 payload/清 claimed_at，第二消费者可 claim，同 ID 的旧 DeleteByIDs 可删除新通知。当前消费者属于 T15/T16，但 T06 耐久 handoff 需防此竞态。见 gitlab-code-wiki-rag-t06-704c1ed7-review.md；已交原 T06 Luna/xhigh 做真实 PG claimed-op 夹具和修复，不集成/关票/派新票。root独立 PG 三项仍在运行；worker 31项PG PASS 不替代该并发反例。T09/T10继续active修复，各自待新完整干净 SHA，父8/22。

Root 的 T06 独立 PG 复验因 worker 已开始修改 704c 冻结工作树而不能作为冻结结果：新 claimed-op 红测在 `datasource_source_integration_test.go:836` 按预期失败（只剩1行，预期旧/新代数独立2行），是对 finding 的进一步证据。停止将该 moving-tree 测试计 PASS；待 worker 新 clean SHA 时在稳定导出/冻结树复验。T09 已获本机捆绑 Node24.19.0 只读路径以完成其锁定运行测试，独立58083/parser与用户指定57521数据库不动共享服务。

T06 执行者尝试向 root 跨对话询问代数投递身份时被自动审批拒绝；root 从线程记录主动收取提案并通过既有授权的 root→worker 消息给出合同，不需要用户人工传话。决定：EventID 稳定标识一次源码发布/outbox 事件；DeliveryID 确定性标识 (EventID, ConfigGeneration)，作为 pending-op 的 dedup 与未来 T15/T16 消费幂等键。新代数必须获得独立 row ID，旧 claimed op 原样留下供旧消费者按旧 ID 确认；消费前仍需重验源代数、当前发布快照和权限，旧 DeliveryID 不写 Wiki。此合同限 T06 耐久通知及后续 T15/T16 消费接口，不提前实现卡片生成。worker 已获具体修复方向、继续 active。
2026-09-30 14:55 CST 最新权威状态：T10 冻结 33d186daa2dc3c209f7234cced0510d766dbfcec 经 Sol/high 双轴审查、root 独立定向 Go/PG/parser/frontend 复验通过，已合入并推送集成提交 9e2ef8261d29f0d3fb74916de1d8e34dff58b594；GitHub #18 已关。旧 T10 分支保留，原 T10 Luna/xhigh 对话已在该集成基线接续已批准 T12/#20，活动中。T06 新冻结 6b0f807e 双轴审查未过：旧 source delivery 可在 source→document 切换后绕过代数围栏、已 ACK Wiki pending 信号在同代数 no-op 中重建、启动恢复可能漏无 state 行 legacy queued 日志；三项已交回原 T06 Luna/xhigh，worker 正验证第三项可达性并修复，#14 保持 open。T09 新冻结 09d99445 双轴审查 Standards 硬0/判断2、Spec 1 P2：已查询但不可读的外置 script 仍展示 raw `unchecked`，不能明确说明不可读；已交原 T09 Luna/xhigh 修复，#17 保持 open。root T09 Node SFC 10/10 PASS；Go 独立试验因默认 cache 禁止访问，改独立 temp cache 后冷编译过久中止，不记 PASS。三执行对话均已通过既有授权直接双向通信，不需用户人工报告完成；下一步只收新 clean SHA 并审查，未通过不得集成或派下一票。GitHub Issue 仅发布验收里程碑，不写过程流水。
2026-09-30 用户批准后续每张新 ticket 统一以启动时已验收的集成提交作为 code-review 双轴固定起点；T12 为 9e2ef826，T06/T09 继续用先前分别已确认的 7f4fd1dc。T12 已在原 T10 执行工作树从本地 9e2ef826 建立独立 `codex/source-bounded-chunks` 分支并进入实现；无需重复索取起点确认。用户明确授权继续，工具层自动审批仍须遵守。
T12 Embedding tokenizer 合同（root 规划决定，待执行者收取）：源码索引必须在切块/入库前解析有效模型 profile：明确 tokenizer 标识和最大输入 token，可由已确认模型内建映射或现有模型配置显式给出；`TruncatePromptTokens` 与硬编码 `cl100k/2000` 不可作为所有模型真实上限。未知 profile 对源码发布 fail closed，返回可操作配置错误并保留旧快照，不改变普通文档处理。逐块计完整 `SourceIndexText`（正文、路径、签名、上下文）的实际 token，并严格低于模型上限与首期 2000 的较小值且预留接口余量；不得依赖服务商静默截断。配置可用 additive JSON 字段，模型 profile 变化必须改变产物版本或触发重建。先用已知模型与未知 profile 的边界测试验证。执行者已主动提问，但 root→T12 `send_message_to_thread` 两次收到自动审批“requires approval, but approval policy is never”，未假称已送达；T12 可先做无关工作，必要时由用户在 T12 对话直接转达此合同。
T09 新冻结 f18b1261：双轴复审 Standards 硬0/判断1 P3，Spec1 P2：外置脚本未知 lang 被 degraded 遮蔽，见 gitlab-code-wiki-rag-t09-f18b1261-review.md。root Node10/10、Go 三包、实际 UI 组件1/1 与 vue-tsc 通过。跨独立对话反馈工具再次遭自动审批拒绝，因此同一 T09 工作树交 GPT-6 Luna/xhigh 临时子 Agent 修单点，原 T09 对话 idle/frozen，不产生重复聊天。新修复 clean SHA 未交前不集成/关 #17；本 f18 SHA 不再重复复审。
T12 编码合同（回应执行者问题，待其读取）：首期不要为 T12 临时重定义整个 SourceRange/阅读 API 去支持 UTF-16LE/BE。现有 `ReadGit` 对 UTF-16 明确标 `unsupported_encoding` 并阻止未排除文件的完整发布，这符合 Spec §76 与实施计划 §81 对不支持编码的显式失败要求；不能悄悄把解码后的字节当作原始区间。T12 AC4 在已支持 UTF-8 路径上验证 BOM/Unicode/CRLF/缩进、任何实际转换的原字节偏移，以及分离不连续上下文；若支持 UTF-8 BOM 去 BOM，则必须保留单调原始字节映射和 raw download。UTF-16 真正支持需设计解码内容与原始字节双表示、偏移映射、哈希/索引/阅读合同，再作为独立后续变更；本票至少用 UTF-16LE/BE 夹具证实清晰预览失败、旧快照保留。不得宣称已支持未实现的非 UTF-8 转换。
2026-09-30 额度恢复后：T12 原独立 Luna/xhigh 会话已重启并读取上述合同，普通 Go 服务测试及集成标签编译通过，巨型 Java/MyBatis/FreeMarker/YAML 在专用 PostgreSQL 的发布和关键词/向量检索通过；仍需清理其余断言、完整冻结及 root 审查。T06 09ea4d55 双轴复审指出 P1 source pause 未 fence 活动运行、P2 无 state 的多个 legacy queued 旧日志可能遗留；root 已独立跑三个针对性 PG 案例通过，但新两反例尚未过。额度中断的 Luna/xhigh 子 Agent 已在原 T06 工作树恢复，获专用测试环境脚本路径，正做红绿回归，原独立 T06 会话保持冻结，避免两者同时编辑。T09 41373dcb 双轴复审：Standards 硬0/判断1 P3，Spec0；root 新增 HTTP 回归在连续三个请求的第三个碰到 429 容量窗口，已发回原 T09 Luna/xhigh 会话做最小稳定化，未将 413 SHA 认作最终验收。三个票均未因执行对话停止而冒充完成；只有 clean SHA、独立复验与双轴审查通过才集成，过程不逐条写 GitHub。

2026-09-30 本轮验收：T09 冻结 `a81bc0db` 已双轴审查通过；合并 T10 时修复真实 parser 清理槽位与 HTTP 响应次序竞争，旧实现确定性复现第二个顺序请求 429，合并后 Script/Vue/MyBatis HTTP 15/22/19 全通过。两项 Vue 发布/代表组件 PostgreSQL 通过；集成 `04d99577` 已推送并关闭 #17。T11/#19 以此提交为批准审查起点，在原 T09 工作树启动；独立对话发送工具暂报 thread not found，根对话使用同一工作树的 Luna/xhigh 子 Agent 执行，避免停工或新建重复侧边栏对话。

T06 冻结 `309f1958` 双轴审查 Spec0、Standards硬0/判断2P3；根独立 PostgreSQL 的 legacy coalescing/pause 两项通过。与 T09/T10 合并时发现已暂存文件重试后丢静态关系，真实回归先红（2→0）后绿（2→2）。合并后另五项真实 PostgreSQL、Go 五包、前端 typecheck 与同步日志组件测试通过；集成提交 `ce6a6cf2`，详见 `gitlab-code-wiki-rag-t06-309f1958-review.md`。T12 新冻结 `c87d56d5`，批准起点 `9e2ef826`；根已派两位 Sol/high 分别做 Standards/Spec 审查，在审查与独立复验通过前不集成或关 #20。
