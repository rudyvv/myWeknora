# T05：[CodeWiki] Force-push、分支与鉴权异常的源码对账

已发布：[Issue #13](https://github.com/rudyvv/myWeknora/issues/13)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

仓库发生非线性提交或暂时无法访问时，管理员能看到故障与最后成功版本；恢复后对账出包括删除在内的正确完整版本。

## Acceptance criteria

- [ ] 非祖先目标、远端旧提交缺失、compare 不可用时用旧 manifest 与新固定 SHA 完整清单对账，包括消失文件。
- [ ] 分支消失、只读凭据失效或拉取失败不推断仓库删除，不自动切默认分支，不清空已发布索引。
- [ ] UI 区分错误、当前发布、检测/处理目标与最后成功时间；更换凭据或分支恢复后可手动重新同步。
- [ ] 不依赖 Webhook diff 作为删除真相，部分扫描失败不能产生完整发布或删除判断。
- [ ] 通过本地 Git 提交图、受控 GitLab 错误和公开同步/查询入口验收；旧固定引用的原文不依赖远端提交仍存在。

## Blocked by

- [Issue #12 — 源码增删改、重命名与配置变化的完整版本更新](https://github.com/rudyvv/myWeknora/issues/12)
