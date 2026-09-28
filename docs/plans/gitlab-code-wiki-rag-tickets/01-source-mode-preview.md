# T01：[CodeWiki] GitLab 源码模式配置与固定提交过滤预览

已发布：[Issue #9](https://github.com/rudyvv/myWeknora/issues/9)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

管理员在现有数据源入口选择源码模式，验证项目、分支、只读凭据和处理能力，查看固定提交下实际纳入的文件清单；旧文档模式继续工作。

## Acceptance criteria

- [ ] 旧配置缺少模式字段时按文档处理；文档同步及普通 Wiki 后处理回归通过，源码有独立路由而不是放宽原文档后缀。
- [ ] 源码源绑定一个项目、指定分支、目标 KB；管理员权限、加密凭据与轮换沿用既有行为，日志不含凭据。
- [ ] 预检 GitLab 分支、双索引与解析能力；缺少 Wiki 能力明确提示且不改变已有开关或 Agent AllowedTools。
- [ ] 固定 SHA 清单预览显示路径、纳入/排除原因、生成/超大/异常分类；包括规则可保存并版本化，不凭 dist/plugins 名字全部排除。
- [ ] LFS、子模块、不可读文件及未知编码真实可获取性可见，不扩大新仓库授权；预览不创建已发布源码或每文件摘要卡片。
- [ ] 通过公开配置/预览 API、受控 GitLab/Git 夹具与管理 UI 验证；必要路由扩展保持旧接口兼容。

## Blocked by

None (can start immediately)
