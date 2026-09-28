# T20：[CodeWiki] 可选 GitLab Push Webhook 触发与定时对账

已发布：[Issue #28](https://github.com/rudyvv/myWeknora/issues/28)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

管理员配置项目 Push Hook 后，合法推送快速触发既有持久同步流程；重复、漏通知和回调错误不改变同步真相。

## Acceptance criteria

- [ ] 提供受既定管理权限控制的回调配置与连通测试，管理员自行配置项目 Hook；不自动创建、不接入 MR/Issue/Pipeline。
- [ ] 按已登记 tenant/源/项目/分支验证 secret 或现场版本支持的签名、事件和重复标识，payload 不决定任意仓库或租户。
- [ ] 鉴权成功后可靠存触发再快速响应，实际 HEAD/固定清单由同源 worker 获取；鉴权失败不登记。
- [ ] 重复 Hook 合并，定时默认每小时可配置对账覆盖漏通知，暂停/解绑/清除源不能因回调重建。
- [ ] 受控 GitLab Hook 夹具验证签名/secret、重复和漏通知；现场可达时记录真实连通，否则保留手动/定时能力并明确限制。

## Blocked by

- [Issue #14 — 手动与定时源码更新的串行、追赶和重启恢复](https://github.com/rudyvv/myWeknora/issues/14)
