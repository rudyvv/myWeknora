# T16：[CodeWiki] 源码变更驱动受影响技术卡片更新

已发布：[Issue #24](https://github.com/rudyvv/myWeknora/issues/24)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

仓库发布新快照后更新受影响卡片，未受影响页保持正文并验证适用性；新增入口和公共接口变化不会被漏掉。

## Acceptance criteria

- [ ] 变更证据与可确定静态反向依赖定位页，影响理由可见；不确定扩展模块，公共接口/配置扩展关联模块及概览。
- [ ] 新增文件/入口重新检查主题骨架；不只反查已有证据，否则会漏掉新能力。
- [ ] 受影响代码贡献按版本替换，旧 UUID 不无限并集；多源页面只撤回/更新相应源的贡献。
- [ ] 未受影响页核验相关文件版本、依赖和模块清单，不只看一个引用片段；原正文/证据 SHA 保留，另记录新快照适用性。
- [ ] 发布时失效页立即退出当前问答；来源确实删除后撤回或清单来源页，生成暂时失败不当作来源删除。
- [ ] 通过两次发布后的 Wiki 查询/来源阅读验证局部更新、未变页、删除和旧任务完成后不能覆盖新页。

## Blocked by

- [Issue #12 — 源码增删改、重命名与配置变化的完整版本更新](https://github.com/rudyvv/myWeknora/issues/12)
- [Issue #23 — 系统、模块及业务流程骨架与首批卡片覆盖](https://github.com/rudyvv/myWeknora/issues/23)
