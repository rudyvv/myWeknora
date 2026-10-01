# T20 f0e4aa87 验收审查

干净冻结 `f0e4aa87ef1bbcd832d1a78deab8aae4c7c1301a`；批准起点 `55847749344ae08bb9c2969dd32a5d397b18a0c6`。两个独立 Sol/high 审查者分别完成 Standards 与 Spec，root 实际复验；执行者 Luna/xhigh 不自行验收。

## Standards

硬性违例 0；判断性 P3 三项，不作为验收阻断：重复 receipt 更新、handler 委托中间层，以及 scheduler 启动/周期恢复重复枚举 error GitLab 源。管理权限、持久源身份、加密 secret、暂停/清除围栏符合原合同。

## Spec

剩余阻断 0。旧 P1 已在 Hook 服务入口、持久登记事务、启动 cron 和已注册 cron 的执行检查中修复：只有配置/凭据/绑定有效的源码 error 状态可重新触发，paused/unbound/cleared 不复活。旧 P2 已修：secret 轮换/清除与接收证据重置同事务且持有源/Hook 锁；并发旧请求重新检查当前 secret。

## 独立验证

- 冻结树真实专用 PostgreSQL 六项通过，包 61.380s：HTTP Hook→持久触发→同源 worker→published HEAD；预算耗尽后 error 接收新 Hook 与启动定时追赶；已有 cron 在 error 后继续；并发密钥轮换/清除/重启用；root 原两个失败反例重新运行亦通过。
- repository / datasource 定向通过 4.405s / 6.908s；准确管理 route policy 通过 4.751s。首次 handler/gitlabwebhook 的筛选匹配零测试未计验收，随后完整包实际通过 3.540s。
- 与已验收 T12 无冲突合并后，三项真实 PostgreSQL 组合回归通过，包 44.432s；service / handler / router 定向通过 4.327s / 2.291s / 1.200s。
- 集成前端 vue-tsc app 配置通过；callback URL 两项实际通过，验证当前 origin 与绝对 API base 均保留代理挂载前缀。
- 独立 `127.0.0.1:57521/source_test`、已有锁定 parser 和缓存；没有读取旧容器凭据、修改共享应用、对现场 GitLab 创建 Hook 或假称已完成真实内网连通。管理员仍需自行配置项目 Hook。
- 全服务包既有四项 Windows shell/工具运行限制仍是已知边界，不声称全仓全绿；本次未扩大范围反复跑它们。

## 决定

Standards 硬 0 / 判断 3（P3）；Spec 0。冻结与组合必要验证通过，允许集成发布并关闭 #28；最终提交记录在协调文件最新条目。
