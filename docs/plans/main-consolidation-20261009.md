# main 整合与环境清理（2026-10-09）

## 最终代码入口

日常开发统一使用 `main`。本机工作区为 `D:/Project-Weknora/WeKnora`。

main 从 `b477f690` 快进到源码集成提交 `98f441ab`，保留完整提交历史，同时包含：

1. 业务资料接入、业务 WikiPage 卡片生成与阅读。
2. GitLab 源码接入、解析与索引、技术 WikiPage、源码 RAG 和固定版本源码引用。

两套本地环境使用同一份 main 源码，数据库仍独立。没有合并业务数据与演示数据。

| 环境 | 前端 | 后端 | 数据库 |
| --- | --- | --- | --- |
| 业务环境 | `http://127.0.0.1:5173/` | `127.0.0.1:8080` | `5432 / WeKnora` |
| GitLab 演示环境 | `http://127.0.0.1:57826/` | `127.0.0.1:57825` | `57822 / source_t22_live_rehearsal_20261006` |

依赖服务由 Docker 运行，后端从本机源码编译，前端使用 Vite 热更新。GitLab 演示环境的配置已迁移到主工作区 `.codewiki-dev/runtime.env`，不会覆盖业务环境根目录 `.env`。

## 分支和工作树处理

- 清理前 53 个本地分支、11 个工作树；清理后只保留 main 和主工作区。
- 已整合分支通过提交祖先关系、patch 等价关系及最终文件内容确认；没有把同一套实现再次重复合并。
- 用户决定归档 `codex/t22-wiki-qa-diagnostic` 的旧诊断实验，以当前集成实现为准。完整历史仍可从备份恢复。
- 自有 GitHub 仓库 `rudyvv/myWeknora` 同步 main，并清理 5 个旧开发分支。
- 腾讯官方 `origin` 不做远程修改；本地仅跟踪其 main，清除 59 个多余的本地远程跟踪引用。

后续功能使用短期 `codex/<feature>` 分支；完成后合入 main 并删除分支和工作树，避免长期维护每个 ticket 的副本。

## 数据保护与 Docker 清理

主库迁移前使用 `pg_dump -Fc` 备份，结构从 103 升级到 122，原有 3 个知识库、3005 张 Wiki 页面数量不变。GitLab 演示库也有完整备份。

删除 8 个旧测试容器、3 个已备份的测试 PostgreSQL 卷、9 个旧解析器镜像标签；经用户确认，继续删除 6 个旧 Docker 部署容器及闲置镜像。业务数据卷保留。Docker 构建缓存清理报告回收 3.516 GB，后续闲置镜像清理报告回收 4.458 GB；共享层使这些数字不能直接当作磁盘净增量。

现有 Docker 依赖按用途保留：业务环境依赖、GitLab 演示 PostgreSQL / Redis、共用源码解析器。不同数据库与 Redis 隔离有实际用途，不进行容器或数据合并。

## 验证记录

- 前端类型检查、生产构建通过；构建有既有大 chunk 提示。
- Source/Wiki 前端专项测试：30/30 通过。
- Go source、datasource 主包、GitLab connector、repository、types 测试通过；Wiki/Source service 和引用工具定向测试通过。
- 独立 `source_test` 数据库与真实 parser 集成测试：4/4 通过，覆盖首次源码发布与搜索、暂停阻止新同步、技术 Wiki 生成、版本切换后的引用范围。没有在演示库运行测试夹具。
- 全量前端测试首次运行 965/971 通过。其中 SourceSnapshotRunView 的测试加载器漏掉 `@/` 模块别名，本次补齐并通过专项复测；其他失败涉及沙箱配置断言与 Windows 上的 POSIX shell 用例。
- 后端全量测试仍有飞书日志断言、沙箱环境、Python / shell / Windows 符号链接等失败。不将专项通过表述为全量测试通过。
- 完整 T22 性能及故障矩阵没有重新验收，相关 GitHub issue 未关闭。

### 最终运行与页面复核

- 两套后端 `/health`、两个前端入口与 `/@vite/client`、parser `/health` 均返回 200。两套后端实际运行主工作区编译的 `63cb55a0`，后续本记录补充提交只修改文档。
- GitLab 知识库 `65658207-a2ec-47fb-bf0f-11e7b685369e` 文档列表正常加载，显示目录和已完成文件；技术 Wiki 目录、正文、固定提交源码证据均可打开。
- 同一 main 代码下，演示库保留的 EVIP 业务 Wiki 目录和“一对一”卡片正文可读；页面刷新后可重新打开。业务环境 5173 当前浏览器未登录，仅验证了登录页与服务健康，没有绕过账号认证。
- 新建真实源码 RAG 会话 `d695eda1-f747-4655-b7b4-d46e57460b6b`，问题涉及 SwitchMode.vue 的中心过滤、请求参数和调用接口。回答正确定位 `isSimplify !== mode`、`schoolId/status`、学校列表/签名/模式切换接口；真实源码引用可打开，刷新后回答与引用仍可用。
- 用户批准完整备份后，暂停 4 个 active 源，取消 3 个待执行 Wiki 更新计划、2 个待执行计划项、4 条持久化排队记录，以及 Redis 中对应的 1 条 Wiki 重试投递。计划保留为显式失败并记录 operator_cancelled 原因。已有源码、Wiki 和会话保留。
- 重启前演示库为 5 个 paused 源、1 个 deleted 源、0 条 pending ops；3010 张 Wiki 页面、269 条未删除知识记录、17 个既有会话保留。此次验证另增加 1 个会话。未触发 nsb 全量同步。
- Docker 最终保留 15 个容器（12 个运行中）、12 个唯一镜像、25 个卷。未删除不明归属或历史业务卷；3 个停止的开发配套容器保留，以便现有 Compose 配置继续使用。

### 仍需后续处理

- 上午源码更新使部分技术 Wiki 卡片过期，当前回答会排除这些过期卡片；历史正文和固定版本证据可读。重新生成属于后续人工触发工作，本次按用户要求取消待执行任务并保持数据源暂停。
- 自动会话标题仍可能把提问当成要求回答的内容，生成误导性长标题；本次验证会话已手动改名为“main 整合验证：模式切换参数与源码引用”。问答正文成功，不代表标题生成问题已修复。
- 聊天仍有既有“图片无法显示”占位；部分业务 Wiki 索引交叉链接未能定位到页面，左侧目录中的实际卡片可打开。
- 演示环境页面提示若干普通文档类型未配置解析引擎；本次验证已有业务 Wiki 阅读，没有在演示库新增普通文档上传。
- 全量测试未全部通过，具体范围见上文。完整 T22 容量、性能和故障矩阵仍待独立验收。

本机截图保存在 `.consolidation-20261009/business-wiki-proof.png` 与 `rag-source-proof.png`。

## 本机重启

这些脚本及凭据仅保存在本机、被 Git 忽略；其他电脑应按项目开发文档自行配置。

```powershell
Set-Location D:/Project-Weknora/WeKnora
powershell -NoProfile -ExecutionPolicy Bypass -File ./.main-dev-start.ps1 -Restart
powershell -NoProfile -ExecutionPolicy Bypass -File ./.codewiki-dev-start.ps1 -Restart -EnableGitLabTLSException
```

第二条命令的临时 GitLab TLS 例外仅适用于本机已批准的固定 origin，截止北京时间 2026-10-10 19:00，到期后去掉 `-EnableGitLabTLSException` 并使用有效服务器证书。重启命令不自动延期。TLS 配置与构建覆盖均未提交到 Git。

两个脚本都支持 `-CheckOnly`。重启会核对端口进程身份，遇到不属于脚本记录的进程会拒绝替换。GitLab 演示启动前要求数据源暂停、后台任务为空闲，避免自动触发大仓库同步。

## 恢复资料

本机私有目录 `.consolidation-20261009/` 保存：

- `all-refs-before.bundle`：清理前所有 Git 引用及完整历史，已通过 bundle verify。
- `branch-audit.json`：逐分支提交与整合审计。
- `worktree-local-files/` 与 manifest：旧工作树配置、日志等不可重建文件；依赖目录、缓存和可重编译二进制未重复保存。
- `main-db-before.dump`、`source-db-before.dump` 和 3 个测试卷压缩备份。
- Docker 清理前清单、构建与测试日志。

恢复旧实验时，应从 bundle 指定分支恢复到新的工作区；数据库恢复应导入独立数据库，避免覆盖正在使用的数据。
