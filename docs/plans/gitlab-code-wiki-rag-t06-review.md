# T06 检查点首轮双轴审查与修复合同

2026-09-29。用户要求审查已结束的 T06。执行对话正常完成，无额度或运行错误，提交检查点 53344c50d9d633b901ca1fa02b29ba3ea3fac495；冻结时工作树干净。沿用用户明确批准的起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。diff 为 `git diff 7f4fd1dc...53344c50`，提交 239ba2dd、53344c50，26 个文件。没有把审查派发后 worker 的修改混入本轮结论。

Root 已通过 gh 读取 Issue #14，六项 AC 与已批准本地 ticket 一致。按 code-review 技能派发两位独立 GPT-6 Sol / high reviewer，分别检查 Standards / Spec。两轴报告保留各自计数与严重等级；执行对话继续 GPT-6 Luna / xhigh。

Worker 准确说明 outbox 仅保存 pending，没有 relay / 确认；通用 wiki:ingest 不能直接满足源码证据校验合同。该检查点不代表整票已完成，Issue #14 保持 open，不合并实现。

## Standards

硬违反 1；判断性建议 1；最严重 P2。

- **P2：旧 worker 可覆盖已取消运行。** `internal/application/repository/datasource_repo.go:274–280` 的 UpdateResult 仅比较 sync_logs 自身旧 generation/token；`source_sync.go:83–90` 取消运行未清除这些字段。配置变更使租约失效后，旧 worker 回写仍满足条件，可把 canceled 改成 running / failed / success。违反 implementation-plan:106“数据库租约和单调 fencing token 保护写入”、:110“过期租约任务不能提交”。应在同一事务核验当前数据库租约后回写日志。影响已核验为运行状态复活，不称能够绕过 Publish 发布事务。
- **P3 possible Duplicated Code。** `source_sync.go:93–115` 与 :180–202 重复取消 active/pending、递增配置代数和 fencing、清租约/运行指针、更新 state 的失效处理。可以抽取共同事务步骤，并保留取消原因和 disabled 指纹差异。属于判断性建议。

其余调度、发布、迁移和 UI hunks 未发现新增 ADR 硬冲突。Pause 的完整管理语义属于 T19，未将其未修改方法计成本轮新缺陷；outbox 未完成交由 Spec 轴。此 reviewer 仅静态审查，未独立重复运行 PostgreSQL 套件。

## Spec

7 项：missing/partial 3、wrong 4、scope creep 0；4 P1、3 P2，最严重 P1。

- **P1，missing：Wiki 信号尚不能恢复投递。** T06:13“事务成功后信号仍能恢复投递”；`source_snapshot.go:219–222` 只插入 pending，全树没有领取、消费或确认路径。测试只计数 pending，不能证明 T06:16 的有效更新信号。
- **P2，missing：同 SHA / 有效配置仍重复发布。** implementation-plan:106“重复事件或同 SHA 同配置返回已有运行状态”；`source_sync.go:134–153` 每次触发新运行，`datasource_source_sync.go:55,80` 仅按新 log 查重，没有已发布 SHA / 加工配置相等时的跳过路径。默认每小时检查可能反复创建快照和 Wiki 信号。
- **P2，partial：预算持久化合同与恢复断言不足。** T06:14“既有预算计数不重置”、implementation-plan:53 运行记录“已消费预算”；新增 `source_sync.go:46–48` 和 migration 000108:39–41 只声明默认零预算，没有消费更新或恢复断言。此项是合同与验收证据不足，不称既有 SourceWikiAttempt 预算已被重置，也不推断所有模型消耗无限。
- **P1，wrong：配置失效后旧 worker 仍能改运行状态。** T06:11“配置代数拒绝旧 worker 写入”；`datasource_repo.go:273–289` 仅比较日志旧 generation/token，取消时未替换，旧 worker 可覆盖 canceled。该轴独立评级 P1；Standards 轴的 P2 原评估保留。
- **P1，wrong：普通失败丢失队列重试后不能对账恢复。** T06:14“丢信号后对账恢复”；`datasource_service.go:1074` 始终 ReleaseSourceRun(...false)，`source_sync.go:428–433` 清 active，:498–552 只找 active/pending。直接重放原 task 可再 claim，但数据库恢复器已发现不了该运行，不能代替丢信号验证。未发现此问题可以绕过 PreviousSnapshotID 保护导致旧快照覆盖新快照。
- **P1，wrong：解析器升级后的阶段复用混合加工版本。** 父 Spec:88“产物复用包含……有效解析/切块/上下文版本”；`datasource_source_sync.go:199–207` 恢复不核 ProcessingVersion，:231–270 用当前解析产物配旧 chunk IDs，只比较块数。块数相同但切块或上下文改变时，新索引/向量可能对应旧正文、坐标及旧版本记录。此项是冻结静态链路核验，未实际执行 parser 升级故障测试。
- **P2，wrong：数据源摘要保存旧错误和旧结果。** T06:15 失败/重试和最后成功可见；`datasource_service.go:1513–1533` 提前返回，跳过 :1545–1546 的 ErrorMessage / LastSyncResult 赋值，`source_sync.go:600` 写回旧值。日志有失败原因，数据源卡片仍可能没有新原因和结果。

此 reviewer 只读冻结源码与测试，反例为静态推导；根对话随后实际核验的范围列在下面。两轴计数：Standards 1 硬违反 + 1 判断性建议，最严重 P2；Spec 7 finding，最严重 P1，不合并或重新排序。

## 总协调独立验证

八项真实 PostgreSQL 聚焦 tests PASS，35.801s：租约并发/失效/配置 fencing、阶段复用、错误向量/关键词失败禁止发布、B/C/D 串行追赶、首个 Java 快照双索引、enqueue 失败持久登记、scheduler 重启 redelivery。使用用户选择的独立测试数据库及既有 Python / grammar cache，未读取旧容器凭据。

另以临时 Go overlay 加入三个期望行为断言，均真实执行，未改冻结工作树：

1. `TestRootT06StaleRunCannotOverwriteCanceledStatus`：FAIL，package 3.127s。旧 lease 的 RecordSourceRunPhase 正确返回 ErrSourceSyncLeaseLost，但随后 UpdateResult 无错误；状态 canceled 变成 success。该证据只证明日志状态复活。
2. `TestRootT06FailedRunRecoveryAndVisibleState`：FAIL。模拟模型返回零向量造成普通失败，随后恢复模型并令队列丢失重试信号；实际 run_failed_reason_present=true、datasource_error_present=false、datasource_result_bytes=0、durable_retry_dispatches=0。同时证实恢复及公开摘要两个缺口。
3. `TestRootT06UnchangedTargetDoesNotRepublish`：FAIL。两次公开 ManualSync / ProcessSync、原 HEAD 和有效配置不变，实际 same_sha=true、snapshot_pointer_changed=true、published_snapshot_count=2、outbox_signal_count=2。

后两个用例同一 package 9.721s，失败来自预期合同断言，非测试环境异常。现有八项通过不能抵消新增反例。`git diff --check 7f4fd1dc...53344c50` PASS。worker 报告前端分项目类型检查通过及四项 POSIX 专用服务测试在 Windows 失败；本轮未重复完整 Go / frontend suite，不称已独立确认这些环境失败的基线。

## 已决定的 Wiki 接收合同

T06 负责把 publication 事件可靠交给源码专用的持久待更新入口；T15 / T16 负责选题、影响分析和模型生成。复用现有 task_pending_ops 基础设施，采用独立 typed lane，例如 task_type=source:wiki:update、scope=knowledge_base、op=published_snapshot。禁止直接接通普通 document wiki:ingest，或将一条发布通知直接当作 GenerateModule 请求。

通知携带 schema_version、稳定 event_id、tenant / KB / source / snapshot ID、commit SHA 和发布配置代数；身份字段从数据库核对，不信任任意队列 payload。接收入口核验归属、有效 publication 与生命周期，幂等持久保存待 Wiki 更新；泛用 TaskPendingOps 的 DedupKey 没有唯一约束，不能独自保证幂等。只有持久接受确认之后才可标 outbox delivered；同 PostgreSQL 可将接收和确认置于一个事务。投递失败有计次、有界重试和重启 reconcile，丢唤醒仍可重发。

“已接收待 Wiki 更新”不等于“Wiki 已生成”。旧事件不得覆盖较新的待更新目标；source / KB 删除或明确 clear 不能复活待处理工作。真实 PG tests 必须观察真实接收入口和持久目标，覆盖 rollback、接受/确认失败、重启、重复 relay、丢唤醒、目标追赶及生命周期。仅 fake receiver 或 outbox 行计数不算生产接线验证；本票不另建生成器。

## 修复派发

七项 Spec finding、Standards 硬违反及建议、实际反例和上述合同均已发回 T06 GPT-6 Luna / xhigh。执行对话已恢复 active，沿用原基线，提交新冻结结果后由 Sol 双轴复验和集成；不 push 或关票。临时 overlay 仅作为本轮取证，正式回归由 worker 添加。当前父任务仍为 8/22 完成。
