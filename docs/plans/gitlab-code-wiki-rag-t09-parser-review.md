# T09 解析器冻结检查点双轴审查

2026-09-29。用户暂停后明确恢复总协调及三个独立执行对话；T06、T09、T10 已核对 active。规划与审查由 GPT-6 Sol / high 完成，执行保持 GPT-6 Luna / xhigh。沿用用户确认的起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d，不重新确认。

本轮只审查 T09 解析器冻结提交 906801311680c6c6fb86db308d937e5aea892416。diff 为 `git diff 7f4fd1dc...90680131`，提交列表只有 90680131，14 个文件。Go、repository、公开阅读和前端的工作树改动不在冻结范围内，不能据此宣称整票通过。已通过 gh 读取 Issue #17，五项 AC 与本地 ticket 一致。

两个独立 Sol reviewer 分别执行 Standards 和 Spec，使用冻结 blob，不修改 worker 文件。暂停前 reviewer 不再存活，因此恢复时重新派发同一冻结范围；没有把执行对话后续修复混入本次结论。

## Standards

硬违反 3；判断性 smell 1。

- **P1 签名与原文范围不一致。** `sourceparser/runtime.py:250–253` 生成空 signature，却令 signature_range 覆盖非空 body。违反 implementation-plan:89,96 的原文切片核验，以及既有 `internal/source/parser_client.go:86` 契约。独立冻结探针范围 10..31，对应 `<div>预约😀</div>`，签名为空；接入现有 Go 校验会被拒绝。
- **P2 超长属性静默转换为空。** `sourceparser/sfc/parse_sfc.cjs:35–36` 将超过 64 字符的 lang 或 4096 字符的 src 清空。违反 parser README:19,39 的外置引用保留、未知方言显式降级合同。冻结探针分别得到 structural、语言及外置引用为空，未拒绝或说明降级。
- **P2 条件性进程清理缺口。** `sourceparser/server.py:107–125` 的六秒总期限仅 terminate Python；新增 `runtime.py:178–182` 的 Node 四秒期限在 grammar 加载后开始。违反 implementation-plan:89 的运行限额要求。Windows 受控故障探针注入三秒 grammar 加载和慢 Node，确认 Python 已退出后 Node 继续执行。该实证只覆盖 Windows 本地条件路径，尚未验证 Linux 容器故障路径；需要清理该次解析进程树后再释放槽位。
- **判断性 possible Duplicated Code。** `runtime.py:36–38,174–176` 重复环境字典与删除两项变量；:201,219 重复 region kind 归一化。可提取共享逻辑减少边界策略漂移；不是文档硬违反。

其余部署、固定依赖、禁止安装脚本和离线隔离静态一致。官方 checksum 在线读取未成功，不称已经完成外部复核。

## Spec

finding 3：1 P1、2 P2；范围膨胀 0。

- **P1 区域 marker 无法通过现有签名校验。** `runtime.py:251–253` 空签名与非空 signature_range 不一致，违反 T09 AC2“测试逐切片核验”。独立冻结探针四类非空区域均不相等；现有 Go 消费路径的拒绝后果为静态核验，不能冒充已执行整条 Go 链路。
- **P2 JSX 声明未走结构解析。** `runtime.py:220–221` 缺少 jsx 到 javascript 的映射。违反 AC1“script 按声明语言走结构解析”。独立 JSX SFC 探针得到 unknown_preprocess、无 Widget；同一代码改为 tsx 可得到结构和符号，直接 .jsx 原本支持。
- **P2 超长 lang / src 丢失原声明。** `parse_sfc.cjs:34–36` 把 65 字符 lang、4097 字符 src 清空，违反 AC3“不能读取的目标或未知预处理明确说明”。独立冻结探针会将它们错误呈现为普通内联 JavaScript / structural。应明确拒绝超限或保留有诊断的降级状态，不能默默解释为默认语言。

BOM、中文、emoji、CRLF、多 style/custom，以及内嵌脚本签名和 context 的原文切片探针通过。Go、scope、双索引、UI、代表仓库整票验证是冻结范围之外的已知 WIP，没有计为这个 parser 检查点的新缺陷。

两轴计数：Standards 3 硬违反 + 1 判断性 finding，最严重 P1；Spec 3 finding，最严重 P1。保持两轴独立，不合并或重新排序。

## 总协调独立验证

- 原冻结镜像 `sha256:5a994755b09a1365157ceb3c5805dd2a8ce245dca5011c9d754ad8a110402da7` 加冻结 tests 只读挂载：32 项 HTTP tests PASS，34.747s。
- 测试容器无网络、只读根、移除 capabilities、禁止提权，并限制内存、CPU、PID；缓存和临时目录通过限定 tmpfs 提供。镜像按 ID 固定，不将后来同名 tag 的结果混作冻结证据。
- `git diff --check 7f4fd1dc...90680131` PASS。
- Node 六项测试 PASS 是 worker 的既有记录，本次总协调没有重复执行，不称独立验证。

通过的现有用例没有覆盖上述所有反例，不能抵消已复现问题。没有读取旧测试容器凭据；后续 Go 集成继续使用用户选择的独立测试库。Issue #17 保持 open，未集成这批代码或标记验收完成。

## 修复派发与接口

签名采用与原文一致的实际切片，或合法的零宽签名区间；非空区域、emoji/CRLF 需在实际 Go ParseFile 路径回归。补 JSX alias 和原整文件坐标核验。超长属性明确拒绝并带诊断，或保留有界且可识别的降级信息；边界测试覆盖 64/65、4096/4097。

进程修复只能终止本次解析启动的子进程树，不能杀其他 Node 或任务；需验证总期限、槽位释放、正常结束和 Linux 生产路径。执行对话已收到条件性 Windows 实证范围，没有把它误报为 Linux 已验证问题。

继续沿用已批准 SourceRegion additive 合同，外置目标须同时受固定快照、manifest 准入及当前 file/tag/source 读取权限约束；未经授权不返回 resolved path 或目标存在性。T09 完成自己的 Go、repository、types、UI、测试提交，root 处理与 T10 共享文件的集成冲突。T09 不自行启动审查代理、push 或关票；新冻结提交交回 Sol 复验。

T06 已通过八项真实 PostgreSQL 聚焦用例，仍须补 Wiki outbox 的恢复投递/确认，不能只验证 pending 行存在。T10 已按五项 Spec finding 继续修复。三执行对话均在推进，父任务保持 8/22 完成。
