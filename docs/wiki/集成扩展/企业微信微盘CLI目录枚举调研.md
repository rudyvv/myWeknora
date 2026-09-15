# 企业微信微盘 CLI 目录枚举调研

> 调研日期：2026-09-15；仅依据企业微信官方开源仓库 `WecomTeam/wecom-cli` 的当前 `main` 分支资料。

## 结论

`wecom-cli` **不能递归枚举指定微盘文件夹的全部文件**，也不能以 `folder_id`、路径或路径前缀列出某个文件夹的直接子项。因此，不能把它单独作为「指定文件夹完整同步」的数据发现层。

CLI 可以在已经拿到离线文件 `file_id` 后下载文件；在线文档需按类型交给相应的 CLI 文档能力读取，且有部分在线类型尚无正文读取能力。若要同步一个人工创建的微盘目录，需由另一个已获授权的目录发现层（例如 RPA）负责在企业微信客户端中递归收集目录树与文件标识，CLI 只承担后续拉取。

## 官方证据

### 1. `list` 不是目录列表

官方微盘技能将 `disk files list` 定义为「获取用户微盘最近查看的文件列表」，而不是按文件夹列举子项；其命令为：

```bash
wecom-cli disk files list --json '{"limit": 10}'
```

该命令的入参只有 `cursor` 和 `limit`，没有 `folder_id`、`space_id`、`path` 或递归参数。虽然支持分页，但分页对象是“最近查看的文件”集合，不能据此构建指定目录树。[官方技能：列出文件](https://github.com/WecomTeam/wecom-cli/blob/main/skills/wecomcli-disk/SKILL.md#%E5%88%97%E5%87%BA%E6%96%87%E4%BB%B6)

### 2. `search` 是全局检索，不能限定某个目录

`disk files search` 支持关键词、文件类型、创建者、空间名称关键词、排序和游标分页。官方说明明确 `space_keywords` 是按**空间名称**的附加过滤，且“不接受 `space_id`”；参数中同样没有 `folder_id`、父节点、路径前缀或递归开关。故它可以协助发现可访问文件，却无法保证返回某一文件夹及其全部后代，也不能作为 parent-child 遍历接口。[官方技能：搜索文件](https://github.com/WecomTeam/wecom-cli/blob/main/skills/wecomcli-disk/SKILL.md#%E6%90%9C%E7%B4%A2%E6%96%87%E4%BB%B6)

### 3. 按 ID 下载可行，但仅覆盖离线二进制文件

已知文件 ID 后可调用：

```bash
wecom-cli disk files download --json '{"file_id": "FILE_ID"}'
```

官方约束：只有 `type=file` 的离线二进制文件可以用此接口下载。`word`、`sheet`、`smartsheet`、`smartpage` 等在线文档应使用其 `docid` 路由到相应 CLI 文档能力；`ppt`、`journal`、`collect`、`mind`、`flow` 目前没有对应正文读取能力。因而“目录发现”和“内容抽取”均应按文件类型分流。[官方技能：下载文件](https://github.com/WecomTeam/wecom-cli/blob/main/skills/wecomcli-disk/SKILL.md#%E4%B8%8B%E8%BD%BD%E6%96%87%E4%BB%B6)

### 4. 身份边界

CLI 的官方安装说明要求企业微信账号，首次通过 `wecom-cli auth init` 完成交互式授权，默认是扫码；这与企业微盘 API 的应用空间授权模型不同，但仍只会看到该登录身份有权访问的内容。[官方 README：安装与授权](https://github.com/WecomTeam/wecom-cli#%E5%AE%89%E8%A3%85--%E4%BD%BF%E7%94%A8)

## 对同步方案的影响

若此处的 “PRA” 指 RPA，推荐按以下职责拆分：

```text
RPA（企业微信客户端）
  └─ 打开配置的目标目录，递归展开并采集：路径、文件/文件夹 ID、类型、更新时间
       ↓
本地同步代理
  ├─ 离线文件：wecom-cli disk files download(file_id)
  ├─ 支持读取的在线文档：按 docid 调对应 CLI 文档能力
  └─ 不支持正文读取的在线类型：记录跳过原因或提示人工导出
       ↓
WeKnora 上传 / 解析 / 索引
```

建议以 `space_id + file_id` 作来源主键，以 RPA 采集的路径仅作展示和范围校验；用 `update_time`（必要时加内容哈希）判断增量。RPA 自动化应额外处理登录失效、客户端版本/界面变化、权限弹窗、下载频率限制和失败重试。不要从 CLI 的全局搜索结果推断目录完整性。

## 参考

- [WecomTeam/wecom-cli README：微盘功能范围](https://github.com/WecomTeam/wecom-cli#%E5%8A%9F%E8%83%BD%E8%8C%83%E5%9B%B4)
- [WecomTeam/wecom-cli：微盘技能完整说明](https://github.com/WecomTeam/wecom-cli/blob/main/skills/wecomcli-disk/SKILL.md)
- [WecomTeam/wecom-cli CHANGELOG：命令模型与运行时行为变更](https://github.com/WecomTeam/wecom-cli/blob/main/CHANGELOG.md)
