# 目录扫描生命周期：剩余四项验收

对应 [Spec](2026-09-26-wedrive-scan-lifecycle-spec.md) 和 [发布记录](2026-09-27-wedrive-0.4.0-release.md)。工具 0.4.2，服务端迁移 PostgreSQL 101。验收区分真实微盘扫描、真实签名 HTTP 协议和可控时间的服务规则；模拟时间通过不等于现场运行了 30 分钟。

## 操作与判定

| 项目 | 操作 | 必须满足的结果 |
| --- | --- | --- |
| 自动到期 | 用页面将已批准测试源设为 30 分钟，保持工具在线，不点击立即扫描。到期前观察一次，到期后等待工具最多一个轮询周期；完成后观察后续轮询。 | 到期前无领取，到期后只创建一次有效尝试；完整提交后下次时间为完成时间加 30 分钟，轮询/重连不重复提交。测试后恢复原频率。 |
| 超过 30 分钟 | 真实大目录持续扫描超过 30 分钟，记录初始身份/租约，每 5 分钟观察续租与进展，最后验证完整提交。另用隔离库的可控时钟验证 40 分钟、20 分钟无进展和 4 小时边界。 | 同一身份在原租约之后继续有效并完整提交；无进展与硬上限不能靠心跳绕过。现场无合适大目录时保留现场项，不通过伪造 EVIP 时间来替代。 |
| 扫描中重启 | 记录正在扫描的身份、开始时间、租约和后端进程；仅重启后端，保留同步工具。后端恢复后再次记录身份，并等待完整提交。 | 后端进程确实更换；有效尝试身份、开始时间保持不变，续租可以延长租约；最终清单绑定同一身份，没有 `scan_interrupted` 或新领取替代旧扫描。 |
| 旧结果迟到 | 隔离数据库通过实际 Python APIClient/签名 HTTP 领取 A、创建未完成清单、结束 A、领取 B；随后发送 A 的开始、上传、提交、续租、失败，再重试已完成的旧清单。 | 五类未完成旧写入均返回冲突；B 的身份、租约、状态和最近完整清单不变。已完成清单的等价提交只读返回，不改变 B，不重复成功审计。 |

现场重启由原启动终端执行，以保留原后端环境配置。查看日志和数据库时只记录身份、计数、状态及时间，不记录凭据、目录地址、分享链接或文件内容。

## 可重复协议验收

在仓库根目录设置 `WEKNORA_WEDRIVE_TEST_PYTHON` 为已安装依赖的 Python 路径，以及 `WEKNORA_WEDRIVE_TEST_POSTGRES_DSN` 为本机测试连接（不要把凭据写入文档或日志）。Windows SQLite CGO include 路径按项目开发环境配置，然后运行：

```powershell
go test ./internal/application/service ./internal/handler -run 'TestWeDrive(ProgressRenewsLongScanAcrossServerRestart|RepeatedHeartbeatsCannotKeepStalledScanAlive|ProgressCannotExtendScanBeyondFourHours|ScheduledClaimRetriesOnceThenReturnsToCadence|CadenceChangeSchedulesFromLastSuccess|PostgresMigrationsAndConcurrentClaims|StaleSnapshotCannotUploadOrCommitNewAttempt|OldFailureCannotEndNewScanAttempt|PythonClientEndToEnd)$' -count=1 -v
```

PostgreSQL 测试创建并清理独立 schema，不修改应用数据。40 分钟测试每次前进 5 分钟并续租，20 分钟时重建服务对象、执行重连恢复，40 分钟时完整提交；这是服务重建验证，不能表述为现场后端进程重启。Python→Go HTTP 测试使用临时设备密钥与 SQLite，不使用实际设备身份，也不向 EVIP 注入迟到写入。

## 2026-09-27 执行记录

- 14:43 协议复跑：上述 9 个 Go 测试全部通过，包含真实 PostgreSQL 临时 schema 和 Python→Go 签名 HTTP；工具断线/失去尝试等现有 Python 测试 34 项通过。日志：本机忽略目录 `.cache/wedrive-t5-followup-protocol.log`。
- 实际工具最初离线；启动已注册的 0.4.2 后恢复在线，原仅手动源未自行扫描。
- 页面临时保存 30 分钟频率，因距离 13:42:23 最近成功已经超过 30 分钟，下次到期设为 14:43:19；工具在 14:43:59 自动领取，未点击立即扫描。初始租约截止 15:13:59，身份已记录用于重启及完整提交对照。
- 14:48:59 实际工具完成第一次续租，服务端保存进展序号 269，租约延至 15:18:59；这是实际每 5 分钟续租证据，但尚不能证明现场超过 30 分钟。
- 14:52:17 实际自动扫描完整提交，清单 `complete`、246/246 条、身份与 14:43:59 的领取一致。本轮仅生成一份清单，最近成功更新；下次到期为 15:22:17，精确等于完成时间加 30 分钟，微盘内容 Cron 仍为 0。验收后恢复仅手动。
- 本轮后端进程始终为原进程，未发生现场重启，不能把自动扫描成功当作重启验收通过。待操作者准备好后再启动一轮扫描，在运行期间按原命令重启后端。
- 旧结果迟到：隔离环境的实际签名 HTTP 验收通过，五类旧写入均冲突、新尝试不变；没有向实际 EVIP 源注入旧写入。
- 真实超过 30 分钟的微盘扫描：等待合适目录；40 分钟可控时间协议验证通过。
