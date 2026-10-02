# T17 1aa93fa8 复审

完整干净冻结 `1aa93fa85d9709fb55018a274f3ef317c1b3abcd`，原批准起点 `f040e5e838cfc1d755d332780882758f65610369`。两位独立 Sol/high 复核完整票及十二文件返修。已验收 T18 不重审；T15 独立切片不是整票 READY。

## Standards

文档化硬性违例 0。恢复在加载并核对 attempt/KB/source 租户与父子绑定后创建精确 KB task grant；没有新增全局 Admin 或请求入口授予 task 的路径。既有两项判断性 P3 Duplicated Code（QA 校验、模型/slug 派生）未变，非阻断。

## Spec

上一轮本地恢复扫描和未知 context 预检已修，但新增两项确定问题。

**P1：生产 bootstrap 立即解析未注册依赖，启动失败。** container.go:255 在恢复器 Provide 后立刻 Invoke；其知识库服务依赖 ResourceCatalog、TenantStoreOwnership、StorageBackendResolver、Scheduler，分别到 :269/:272/:288/:384 才注册。dig.Invoke 立即解析构造链，must 会 panic，不等待后续 Provide。应在完整依赖闭包注册后实际启动。违反 ticket17:7 的重启恢复目标。根及 Spec 审查者独立核对实际注册链；未声称已在缺原生依赖的本机完整运行 BuildContainer。

**P2：失效绑定的到期候选永久占据队列并保留 owner。** source_wiki_attempt_recovery.go:144–153 在 KB/source 缺失或绑定变化时返回，先于 :161 deadline 收尾。固定前八条 :113–125 会每轮选中这些 running 行，后续正常恢复得不到执行。须在禁止生成、禁止 task grant 的同时安全结束到期 attempt、释放 exact owner；未到期失效行也须有限终止或隔离，不永久占头部。违反 ticket17:9/16 的有限恢复、失败留因及 owner 生命周期要求，非 T19 清除功能扩展。

## 根独立验证

- 新冻结实际 PostgreSQL 六项 startup/周期恢复及预检：未返回 ID 前崩溃、活 lease 后 QA 同 ID 恢复、过期收尾与 owner 释放、清除 published、异租户绑定拒绝、未知 context 零 provider，全部 PASS，包 32.081s。
- 根独立 overlay 构造八条过期绑定不符且持有 exact pins 的候选，再加一条合法 due attempt；实际两次 bounded recoverBatch 后合法行仍 running，FAIL 7.82s、包 12.164s。证明失效头部阻塞，夹具仅本机 Temp/隔离测试库，不修改冻结源码。
- 本地恢复夹具通过不证明生产 bootstrap 构造链可启动。执行者 container 宽编译受原生 SQLite/DuckDB 依赖限制，已如实保留；缺依赖不是忽略注册顺序的理由。
- 所有根进程结果收取完，无遗留测试或修改共享应用。

## 决定

未通过，两项具体必修已交同一原 T10 Luna/xhigh 对话；不集成/关 #25/派下一票。T15 原 T18 对话完成 planner/coverage/114 独立切片并本地提交 `ef34c7a2`，整批启动/实际生成/原子父子预算尚未接入，保持未验收；允许继续独立接口和状态验证，待 T17 通过后根发确切 accepted SHA 续做。原 T11 已验收等待 T15 解锁 T16，累计仍 16/22。

Standards 硬 0 / 判断 2（P3）；Spec 2（P1、P2）。
