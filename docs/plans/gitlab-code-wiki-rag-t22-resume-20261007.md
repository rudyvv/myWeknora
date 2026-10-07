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

1. 解决source-preview与worker分别解析master的竞态：不能假定只读GitLab token能冻结远端分支。需要产品将预览确认的expected commit持久化到durable source run，并在worker中尊重固定target；核验实际HEAD并发变化、恢复/重投以及租约/配置代数，冻结提交后双轴/安全Sol审查。**2026-10-07 TRAE接手后已完成**：产品slice冻结 `4e4fc3b4`（首冻49f0238b+审查补充），真实竞态red在553afa0复现后green；fixture对齐真实GitLab按可达SHA fetch行为（真实gitlab.p.it只读实测exit 0）；七个定向回归（竞态、登记前HEAD变化拒绝、document模式拒绝、无协调器拒绝、目标不可达保留上一发布、崩溃恢复重投、配置fence）全部通过。独立TRAE子代理三轴审查：Standards 0硬违规、Security 0阻断、Spec缺口（两条拒绝路径未验证）已补测修复。详见preview-target-contract文档。全包37个失败经553afa0已验证工作区基线确认全部预存，非本slice回归。
2. 用户对policy-rejected具体隔离启动动作的回答到达后，由根核验新ACL/clone121clean/no tasks/queue、固定backend哈希、精确GitLab TLS截止，并只启动专用Redis与57825应用；不能触原8080。到期自动恢复正常TLS验证；需要新窗口时重新明确冻结截止并审查，不能复用过期helper。固定凭据窗口到期后不能盲--extend：先只读核对artifact/DB一致，再准备新固定窗口审过helper。
3. 以批准完整清单重跑nsb：5448文件/88273936 bytes；其余dashboard61/mobile102，总5611/92352148 bytes（约92.35MB）。原私有metadata SHA508CCECC713C612D67F362BB6FCA7E31932C6444BAE25CAF56632A952825D676；题集仍C8E527D3CAD34DFD5D4FD2ED54853F1BABCBA17DFB78EB96A170F2A876AE1B44。不得缩范围、修改评分题集或把工具/旧发布当这次成功。
4. 之后严格顺序完成大仓Wiki/mobileQA、真实前端引用、固定30题Wiki/RAG/混合评分、增量/性能/故障/现场证据。已接受T06/T10回归不重复。全部通过前#30保持Open。只有新有效验收里程碑/最终完成才更新GitHub；本轮仅T09里程碑已有comment。

恢复时先git status与实际环境，只使用当前集成树。既有三旧执行聊天空闲不重新派实现；独立review子agent可复用原review角色。新实施按已批准公开API/Agent及真实隔离PG seam用TDD，定向验证；整票完成时一次全套，最后code-review与当前分支提交。
