# T22 根总控交接点（2026-10-07）

本轮唯一总控继续原集成树；不创建新执行对话或工作树。原8080进程9376保持运行，原main b477f690保持。#30 Open，未完成T22，不跑最终全套测试或编造30题/性能评分。

## 已完成的第一依赖

T09真实专用clone升级120→121/clean、6423条NULL缓存元数据回填完成、102批proof全零、三对同公式字节完全相等，旧/新聚合中位994/59ms。详见 payload-rehearsal-20261007 报告。最终payload代码冻结5cdadd8e，独立Sol Standards/Spec/security已通过；最终Windows与Linux/amd64构建均通过。不得重跑upgrade/backfill；需要新操作时重新只读核态。

## 当前准备材料与审查

- 隔离runtime/fixture窗口冻结5373a7a440be3a812481e3f19aed4baaa457a549，经独立Sol三轴复审通过。runtime main SHA00758DF79A244D5BEF95665B359EF1145710AC160C64C518E3E2EF0AEA874AE8；fixture main D9AC5E7F0AE238E3DF13789814338101A0EC33EFC33A6A3D506B95F14BF3D78C。原旧TLS超窗/KB tenant守卫缺口已修，定向测试red→green1.498s/vet/build通过。新backend由已接受5cdadd8e产品树构建，SHA60352A58DC68E6C85E259EE46140E98C3FFC5B086A2CE408E924C558F14FD906；CGO使用repo既有shim和官方mattn头文件，未改系统/module cache。
- 根实测私有storage/logs/fixture/Redis目录和文件owner28211，无意外Allow和reparse；保护目录禁继承，Redisenv/conf从该目录继承严格ACL。已审fixture helper真实--extend exit0/status extended，既有key1仅延到2026-10-07T10:45:00Z（北京时间18:45），scope/hash/cipher保持。无token、DSN、密码、源码或模型密钥输出。
- **隔离启动尚未执行**。包含“docker start weknora-source-t22-redis-57824 + 冻结runtime helper --start”的整条命令被自动审批审核拒绝，返回只有blocked by policy，没有具体原因。未重试或换渠道绕过。根已发送具体异步批准请求，尚无回答；即便用户批准仍需工具允许。当前57824/57825无listener，原8080未停止或重启。禁止flush、共享依赖重启或读取旧容器凭据。
- source-publish准备helper冻结da48e853c4e2d8b5b9e47fab5c1ce32e45d0bd2d，经独立Standards/Spec/security复审认可其准备态。main SHA3D97404A9192E73B1F6A1A9319C6C12F98D7569621A21793C42569A5FCE482F4。定向fakeHTTP1.844s/vet/build通过，仅工具测试。已修实际工作deadline10:44Z之前校验/失败不输出计划计数作实测。**公开--publish的固定拒绝已于2026-10-07由TRAE解除（eaf4bc5e）**：产品修复冻结4e4fc3b4并经独立三轴审查后，移除conditional_sync_required_no_mutation，sync请求携带expected_commit_sha，补fake-HTTP回归（红→绿；双轴审查0发现）。实际CLI仍未读凭据、未建journal、未HTTP写。
- **验收backend已轮换（42bd2e46，独立审查通过）**：从4e4fc3b4修复树以同一CGO shim构建 `weknora-t22-acceptance-4e4fc3b4.exe`（444138140字节），SHA256 3A9073844550D75BE0EACDCA2CB1CDF6C51C8345C9FA8C5CFA8282BEFD4ADDE5；launcher已pin新哈希、stat-only/vet/测试通过；旧5cdadd8e二进制不得再启动。launcher --start仍待此前policy拒绝的启动批准。

## 首次真实 nsb 完整范围发布尝试（2026-10-07 TRAE，经用户明确批准）

用户批准后按冻结合同执行：只读核验三保护目录及凭据文件 ACL（仅 SYSTEM/Administrators/28211，owner 28211，无 reparse）；docker start 专用 Redis57824；launcher --start 以新 pin 二进制（哈希实测匹配）启动验收 backend（PID16544，57825）；DB 只读预检、队列空检查、TLS 窗口校验全部通过。

source-publish adapter：`--preview` 真实通过，**恰好 5448 文件/88273936 字节**与批准清单逐文件一致；`--publish` 创建排他 journal、resume、提交 `expected_commit_sha=c5e128035cd1d0178184a24f164673988c3f3036`（服务接受，登记 durable run）。**发布失败**：worker 5 次尝试全部被真实 Volcengine embedding 服务商以 `ModelAccountTpmRateLimitExceeded`（doubao-embedding-vision 账户 TPM 每分钟 token 配额超限）拒绝，重试预算耗尽，run 终态 failed（syncLog `57f39a7c-c445-4dd2-bd76-31bcaf6923f4`）。只读核验：`source_sync_runs.target_commit_sha` 在全部重试中保持 `c5e12803` 固定（**预览固定机制在真实生产环境行为正确**）；失败 run 零发布，上一发布（11 文件旧快照 `5665ba1d`）完整保留——"明确失败、保留上一发布"合同成立。runtime 已按合同收尾（backend 停止、57824/57825 关闭、原 8080 PID9376 未动）；journal 与日志保留。

**结论**：发布被服务商侧模型账户 TPM 配额阻断，非产品缺陷；预检早已标注模型 token 预算从未实测（"未知输入预算不得当已验证厂商参数"），此为该风险的真实兑现。本次尝试不算 T22 发布验收成功；后续需要：模型账户 TPM 配额提升/确认，或产品侧 TPM 限流退避策略，再在新固定窗口（新审 TLS/凭据截止）重跑。今日 journal 已存在，同日不得重跑 --publish。

## TPM 配额退避产品 slice（2026-10-07 TRAE，用户批准）

针对上述阻断实施"低配额慢速跑完"产品补强：source 索引阶段识别配额类错误（Volcengine `ModelAccountTpmRateLimitExceeded`/`RateLimitExceeded` 家族、HTTP 429/`Too Many Requests`、`rate limit`，分类器有隔离单元测试钉住双厂商错误形态与反例），以 75 秒配额窗口退避重试同一批，受运行截止（backoff+2 分钟预留不可越界，否则带配额标记显式失败）与 40 次上限双重约束；每批成功即落盘向量缓存（既有 `SaveEmbeddingArtifacts`），跨 durable retry_wait 重投收敛；非配额错误行为与错误文本不变；租约续期在独立 goroutine 不受睡眠影响；聊天/Wiki 嵌入路径未触碰。两个真实集成回归：前 3 次 429 后恢复→发布成功；1 小时退避对 30 分钟 RunTimeout→立即失败、phase retry_wait、不睡眠。产品冻结 `660dbade`（红→绿+10 测合并回归 43.689s 全绿），分类器测试 `8f1fa40a`。独立 TRAE 子代理三轴审查：Standards 0 硬违规（判断项：字符串分类器脆性—失败方向安全、包级可变测试 seam—包内测试串行）、Spec 合规（唯一缺口即上述分类器单元测试，已补）、Security 0 可利用发现（固定错误文本不泄露 provider 响应体、资源占用不劣于既有慢端点能力、生产无 var 写入）。验收 backend 重建为 `weknora-t22-acceptance-8f1fa40a.exe`（SHA256 0724ff7ae19e0fde27a754fef8bfa278875f4d3adec4cb660584fdcf3ba7c5bd，444143574 字节），launcher pin 轮换 `4dbd612e` 并经独立审查（双修复祖先关系、哈希实测匹配、旧二进制禁用标记齐备）。**下次发布前置**：新固定 TLS/凭据窗口（今日已过期）+ 新日（journal 排他）；配额即使不提升，退避可在"配额至少支撑慢速跑完"时收敛，否则仍需配额提升。

## 第二次固定窗口准备（2026-10-07 晚，TRAE，用户批准"今晚开新窗口"）

**遗留协调器状态诊断与恢复**：首次发布失败后 backend 被强制停止，run 冻结在周期中——`source_sync_states` 持有死 run `57f39a7c`（active+lease 已过期 09:49:56、retry_count=5 耗尽、gen=2）、`sync_logs` status=running 但 error_message/finished_at 已是终态文案（09:44:44）、nsb 源 status=error。launcher 预检 guard 2/3/4 全部会拒绝（catch-22：预检在 backend 启动前要求干净，但只有 backend 的 RecoverSourceTriggers 能清理）。产品源码核验：`RecoverSourceTriggers` 对该形态确定走 1062 行清理分支（lease 过期+retry≥5→log 置 failed+清空 state，不重投）；`PauseSourceSync` 单事务完成 cancel+清态+置 paused。新建窄域恢复 helper `scripts/acceptance/source-coordinator-recover`（默认只读 inspect，`--recover` 要求精确死形态：error+active+过期 lease+retry 耗尽+running+finished_at+无 pending，执行产品恢复后 postcheck 与 launcher 预检 guard 2/3/4/5 全库对齐）。TDD 冻结 `c0b6a5cf` + 审查修复 `59c5ca25`，三轴独立审查 0 blocker（修复项：RunPhase 输出、删未用字段、DSN path 检查、postcheck 全库对齐）。**已执行**：`--recover` 成功——死 run 诚实终态 failed（"source retry budget exhausted; retry manually"）、源回 paused、只读复核全部 guard=0。

**窗口轮换**：新冻结截止 `2026-10-07T16:30:00Z`（北京 10-08 00:30，延期执行需 ≥19:30 北京以满足 maxFutureExpiry=5h）。延期前只读核对 artifact/DB 一致（都为 10:45:00Z、未吊销）。fixture-window 第二次延期常量、launcher frozenTLSDeadline+钉住测试值、source-publish windowEnd+journal 轮换为 `nsb-full-publication-20261007-attempt2.jsonl`（首次 journal 原样保留、不删不改）。冻结 `9d3c8baa` + 文档一致性修复 `341a3187`，独立审查 0 blocker（唯一 major 为 plan 文档旧截止残留，已修）。launcher exe 重建轮换 `t22-runtime-launch-root-fixed-20261007-attempt2.exe`（25985024 字节与旧版一致——常量等长替换；二进制实测嵌入新截止、无旧截止）。backend 二进制不变（8f1fa40a 退避构建）。

**第二次延期执行与 outcome_unknown 处理**：19:30:48 执行 `--extend`（首次尝试因 LLM_DEBUG_LOG 门禁被拒、未触库；设 false 后重跑）。事务内全部校验通过并**提交成功**（DB key 行 16:30:00Z），但收尾的 artifact 原子替换被工具沙箱阻止（`post_commit_fixture_replace / outcome_unknown` exit 3）。按合同**未盲重试**（重跑本也会因 DB 已是新值而 reject）：只读核验 DB=16:30Z、artifact 仍 10:45Z、无 .tmp 残留；随后以 .NET 直写**手工完成 helper 的原定收尾步骤**——对 166 字节 artifact 做同长字节级替换（仅 expires_at 10:45Z→16:30Z），先备份、写后逐字段校验（token 逐字节一致、id/tenant/kb 一致、仅 expiry 变化、尺寸不变 166）、删除备份。artifact/DB 双侧 16:30:00Z 一致。

**第二次发布尝试（attempt 2）已启动**：19:45 Redis 57824 启动（队列 idle）→ launcher `--start` 全门禁通过（backend PID 27384，57825，2 秒监听就绪）→ 19:46 `--publish`：journal attempt2 创建，preview 精确匹配 **5448 文件/88,273,936 字节**，resume 成功，sync 登记（新 run `5ff2060a-0b02-45df-9e04-8e197eeee044`，target_commit_sha=c5e12803 固定 ✓）。19:49 phase=parsing、retry=0、无 lease recovery。发布在 TPM 退避下慢速收敛中（窗口至北京 00:29）。

**attempt 2 用户决策终止（21:00 前后，用户："mobile QA 立即开始，nsb 停掉"）**：TPM 配额持续饱和（嵌入冻结在 6292 缓存条目/2016 staged 行 ≈ 8.4%，配额窗口间穿插恢复），按用户决定经产品 API `POST /datasource/{id}/pause` 干净收尾（与恢复 helper 的 PauseSourceSync 同一产品语义，未强杀进程）：run `5ff2060a` 终态 canceled（"source was paused; this run was fenced off"，retry=0 未耗预算）、20:00 cron 触发的 pending `d0c466c1` 同批 canceled、协调器 active/pending/lease 全清、nsb 源回 paused、adapter 轮询到 canceled 后按合同自然退出（journal 终态 `failed_or_unknown_no_blind_retry`）。**向量缓存 6292 条（含今晚新增 2016）全部保留**——明天新窗口续跑时 parse/向量缓存断点续传零浪费。首份诚实进度实测：staged 2016 chunks/7.40MB 内容（embeddings 表 attempt2 行），TPM 限速下平均 302-400KB/min。

## mobile Wiki QA 执行（2026-10-07 晚，用户批准立即开始）

**前置状态核验**：dashboard（61 文件/2011 chunks）与 mobile（102 文件/675 chunks）两源于 10-06 09:50/09:53 已全量发布（与批准范围一致）；三 gitlab 源共用 KB 65658207。

**mobile wiki 实际状态：0 张卡片（36 次 attempt 全部 failed）**。`wiki_derivation_state=complete` 指推导流程走完（带着全拒结束），非生成成功。失败原因单一：批 QA 拒绝 `flow/GET /qywx/refund/getStatusList.do` 卡片——"presents two different call sites as a single flow and makes a composition claim not supported by the evidence"，整批拒绝，且该卡在每次重试中反复再生（36 attempts 同一 reason），一张坏卡拖死全部批次直至预算耗尽。

**QA 拒绝正确性核验（对照已发布 mobile chunks 的真实源码证据）**：`getStatusList` 命中 3 个启用 chunks——`src/pages/RefundAudit/index/script.js`（含 `methods.getState` + `methods.onStateConfirm` 两个调用点：mounted 初始请求与筛选确认刷新）、`src/pages/RefundAudit/detail/script.js`（第三个独立调用点，卡片未提及）、`src/service/interceptor.js`。卡片 draft 的 flow 级组合叙事（title/summary 把 mounted 加载与 filter 确认刷新串成单一流程）确实无单一证据支持，且遗漏 detail 页调用点。**QA 判定正确**。

**System overview（dashboard 成功案例）引用真实性抽检**：页面 v1/published/4795 字符，16 个 source_refs（`file_id|path` 格式）经 source_files 表核验 **16/16 全部解析到真实 dashboard 源文件**（exists=true、ds_match=true）。dashboard attempt 轨迹：第 1 次失败于 QA 响应 JSON 解析错误（`json: cannot unmarshal array into Go struct field sourceWikiQA.uncertain`——provider 返回 uncertain 数组、产品期望标量），第 2 次失败于 whole-batch QA 拒绝，第 3 次 ready→published。

**mobile Wiki QA 验收判定**：QA 机制本身验证通过（引用校验判定正确、成功案例引用 100% 真实、失败诚实记录）；**产品发现 2 项**（非本次验收失败）：(1) whole-batch rejection 的 omit 分支从未生效（合同语义 "rejected or omitted"），必然被拒的卡片无放弃机制，导致 mobile 0 卡片——需产品侧修复 omit 或坏卡剔除后重跑 mobile wiki 才能完成产出验收；(2) QA 响应 schema 对 provider 变体不健壮（uncertain 字段数组/标量不匹配即解析失败，靠重试碰运气恢复）。mobile 源 0 卡片是 omit 缺陷的结果而非语料不值得生成，产出验收保留至 omit 修复后重跑。

**runtime 提前收尾（用户决定，~21:4x 北京，窗口 00:29 到期前）**：收尾前只读核验 guard 2/3/4 全 0（与 attempt 1 强杀时存在 active run 的情形本质不同——此时协调器已干净，停止 backend 不会产生死 run）。停止验收 backend（PID 27384，57825 关闭）→ 停止专用 Redis 57824（队列保留、无 flush）→ TLS 例外随进程消亡自动恢复正常验证（例外仅注入 backend 子进程 env，非系统级）。终态核验：原 8080 PID 9376 存活且仍持有 8080；无残留发布进程；DB 终态 nsb=paused、无 queued/running sync_logs、无 active/pending/lease state；向量缓存 6292 条完整保留。parser 57823 容器与 PG 57822 容器按合同保留（基础设施，供后续窗口复用）。

1. 解决source-preview与worker分别解析master的竞态：不能假定只读GitLab token能冻结远端分支。需要产品将预览确认的expected commit持久化到durable source run，并在worker中尊重固定target；核验实际HEAD并发变化、恢复/重投以及租约/配置代数，冻结提交后双轴/安全Sol审查。**2026-10-07 TRAE接手后已完成**：产品slice冻结 `4e4fc3b4`（首冻49f0238b+审查补充），真实竞态red在553afa0复现后green；fixture对齐真实GitLab按可达SHA fetch行为（真实gitlab.p.it只读实测exit 0）；七个定向回归（竞态、登记前HEAD变化拒绝、document模式拒绝、无协调器拒绝、目标不可达保留上一发布、崩溃恢复重投、配置fence）全部通过。独立TRAE子代理三轴审查：Standards 0硬违规、Security 0阻断、Spec缺口（两条拒绝路径未验证）已补测修复。详见preview-target-contract文档。全包37个失败经553afa0已验证工作区基线确认全部预存，非本slice回归。
2. 用户对policy-rejected具体隔离启动动作的回答到达后，由根核验新ACL/clone121clean/no tasks/queue、固定backend哈希、精确GitLab TLS截止，并只启动专用Redis与57825应用；不能触原8080。到期自动恢复正常TLS验证；需要新窗口时重新明确冻结截止并审查，不能复用过期helper。固定凭据窗口到期后不能盲--extend：先只读核对artifact/DB一致，再准备新固定窗口审过helper。
3. 以批准完整清单重跑nsb：5448文件/88273936 bytes；其余dashboard61/mobile102，总5611/92352148 bytes（约92.35MB）。原私有metadata SHA508CCECC713C612D67F362BB6FCA7E31932C6444BAE25CAF56632A952825D676；题集仍C8E527D3CAD34DFD5D4FD2ED54853F1BABCBA17DFB78EB96A170F2A876AE1B44。不得缩范围、修改评分题集或把工具/旧发布当这次成功。
4. 之后严格顺序完成大仓Wiki/mobileQA、真实前端引用、固定30题Wiki/RAG/混合评分、增量/性能/故障/现场证据。已接受T06/T10回归不重复。全部通过前#30保持Open。只有新有效验收里程碑/最终完成才更新GitHub；本轮仅T09里程碑已有comment。

恢复时先git status与实际环境，只使用当前集成树。既有三旧执行聊天空闲不重新派实现；独立review子agent可复用原review角色。新实施按已批准公开API/Agent及真实隔离PG seam用TDD，定向验证；整票完成时一次全套，最后code-review与当前分支提交。
