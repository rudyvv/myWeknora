# T20 300ef851 双轴审查与独立复验

冻结提交：`300ef851ae21eb2634b2571d500d81ff8390f71b`，干净工作树 `C:/Users/28211/.codex/worktrees/b0df/WeKnora`。用户批准起点：`55847749344ae08bb9c2969dd32a5d397b18a0c6`。两个独立 GPT-6 Sol/high 子 Agent 分别审查 Standards 和 Spec，根对话复验冻结代码；执行者保持 GPT-6 Luna/xhigh。

## Standards

文档硬违例 0；判断性 P3 两项，不作为必修阻断：

- `internal/application/repository/source_sync.go:458` 的新投递/重复投递分支重复更新最后接收记录，属于可能的 Duplicated Code。
- `internal/handler/datasource_gitlab_webhook.go:16` 的四个适配方法重复检查接口并仅转发给独立 Hook handler，属于可能的 Middle Man。

管理路由权限、KB API-key 范围、加密存储 secret 和持久登记的项目/分支身份整体符合已有规范。

## Spec

必修两项：

1. **P1：错误状态阻断后续自动追赶。** `datasource_gitlab_webhook.go:175/189`、`source_sync.go:432` 和 `internal/datasource/scheduler.go:60/273` 只接受 active 数据源。实际失败会保留 error 状态。已有 T06 持久重试能恢复尚未耗尽的工作，故不能称首次瞬时失败立即永久停止；但耗尽预算并清理 active/pending 后，上游恢复时新合法 Hook 被拒绝，定时调度也不检查 HEAD。违反 T20 acceptance 第 4 项“定时……对账覆盖漏通知”。源码 error 且配置/凭据/绑定有效、未暂停或清除时需要继续接受新触发和周期检查；普通文档调度语义不扩大。
2. **P2：新 secret 的入站验证使用旧接收证据。** `source_sync.go:366` 轮换/清除 secret 时保留接收时间，`datasource_gitlab_webhook.go:123` 仅凭该时间就返回 verified。A 成功接收后在 WeKnora 轮换 B，但 GitLab 仍发 A 时，所有新入站都拒绝，连通测试仍显示已验证。违反当前配置的连通测试及根已批准“实际入站未验证应为 unverified”合同。需将验证证据绑定当前 secret 配置代数，或在变更时原子重置；历史记录可保留但不能验证新配置。

## 根独立验证

- 原新增真实 PostgreSQL HTTP Hook → 同源 worker → published HEAD 与 scheduler 路径通过，16.62s；测试包总 22.652s。
- 公共 Hook/管理 handler 定向测试通过；准确的 `TestTenantInfrastructureRoutesDeclareSpecificCapabilities` 通过 4.665s；身份/secret 两项通过 4.547s。首次错误的 router `-run` 匹配零测试，未计作验证，已用准确名称纠正。
- Callback URL 两项通过，前端 vue-tsc 通过。
- 根用 Go overlay 添加两个临时真实 PG 反例，不改冻结工作树。第一项运行零向量失败，确认 error 后直接置既有 run 的 retry_count=5 来模拟重试预算耗尽，再执行现有 recovery 清理；恢复有效模型后合法新 Hook 返回 Unauthorized，周期 2.2 秒无任务。第二项 A 接收→B 轮换→旧 A 拒绝，入站结果实际 verified、预期 unverified。两项均实际 FAIL，包总 19.872s。不是把编译当运行，也没有读取旧容器凭据。
- 使用独立 `127.0.0.1:57521/source_test`，已有锁定 parser/grammar 和进程环境脚本；共享应用不动。执行者原 PG 仅编译，根已补真实运行。未向真实 GitLab 创建 Hook、发送测试事件或声称现场连通。

## 处理决定

本 SHA 未验收、未集成、未关 #28。两项具体反例、修复合同及可执行环境直接发回原 T20 对话；停止重复 worker 双轴，正式审查由根负责。等待新完整干净提交及真实回归，不重审同 SHA。Standards 硬 0 / 判断 2（最严重 P3）；Spec 2（最严重 P1）。
