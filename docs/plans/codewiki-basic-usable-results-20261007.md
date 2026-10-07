# CodeWiki MVP 实测与演示交付（2026-10-07）

本记录对应今晚基本可用计划。工作树为 `C:/Users/28211/.codex/worktrees/source-integration/WeKnora`，分支 `codex/gitlab-code-wiki-rag`，本轮起点 `36ea9c1c`。这是独立环境的基本功能验证，不是完整 T22 验收或正式主库升级；GitHub #30 保持 Open。

## 入口与启动

- 演示入口：http://127.0.0.1:57826/ 。沿用原账号登录。原 5173/8080 与此入口是不同运行环境。
- 知识库：`T22 GitLab 源码验收 2026-10-05`，ID `65658207-a2ec-47fb-bf0f-11e7b685369e`。
- 数据库是原数据的隔离副本 `source_t22_live_rehearsal_20261006`；PG、parser、Redis、后端分别使用 loopback 57822/57823/57824/57825。
- 本机启动脚本：`C:/Users/28211/.codex/visualizations/2026/10/07/01a1166c-d4c1-7c11-a64a-09bb137b5c13/start-codewiki-mvp.ps1`。Docker Desktop 需已运行。PowerShell 复制下列整行；若启动了前端，保持窗口开启：

```powershell
powershell -NoProfile -File C:/Users/28211/.codex/visualizations/2026/10/07/01a1166c-d4c1-7c11-a64a-09bb137b5c13/start-codewiki-mvp.ps1 -UseApprovedDemoTLS
```

已用该脚本实际重启后端，健康检查 200，保留 Wiki 与历史问答；再次执行不会重复启动现有前端。前端原启动脚本也已经由用户实际启动成功。启动器核对固定二进制 SHA、私有目录 ACL、数据库版本、来源暂停及任务队列空闲，拒绝不满足条件的启动，不自动迁移或清理数据。演示后暂停恢复过的来源，便于再次启动。

同一启动脚本也保存于仓库 `scripts/acceptance/start-codewiki-mvp.ps1`，两份文件 SHA256 一致，便于复核。本脚本依赖这台机器上已准备的 Docker 容器和受保护配置，不声称是全新机器的部署安装器。

用户于本轮明确批准：仅 `https://gitlab.p.it` 的 TLS 例外延至北京时间 **2026-10-08 18:00**（UTC `2026-10-08T10:00:00Z`）。重启后真实 GitLab 预览再次取得固定 SHA。其他地址正常校验，窗口到期自动恢复校验，脚本拒绝过期例外。去掉 `-UseApprovedDemoTLS` 可正常启动和使用已有 Wiki/RAG；公司 CA 信任仍未配置，到期后新同步需正确 CA 或明确批准的新窗口。私有验收 API key 的旧到期时间没有延长，正常账号登录不依赖该 key。

## 本轮修复

1. `SourceWikiModules.vue` 错把 datasource API 的数组当 `{data}`，导致有仓库却无法选择生成。修复真实数组处理，并保留旧 envelope 兼容。
2. Volcengine 按文本调用的一个批次中，部分文本成功、另一项遇配额错误时，重试丢失已成功向量，重复消耗配额。加入单次有界批次内的成功结果缓存；不改变模型、维度、重试次数或运行期限，也不跨模型/来源复用。
3. Agent 聚合文档引用丢失工具的源码证据；工具保存与 SSE 又剥离了整个 chunks。保留最多 200 条版本定位元数据，继续剥离源码正文、片段与原始 context；前端只为同 KB、同文件的引用补全证据，不替换已有固定证据。
4. 聊天 Wiki 预览缺少源码证据查看器接线。为当前页面所属证据打开 `SourceCodeView`，绑定 KB、slug、页面版本、证据 ID、文件版本和 SHA，拒绝跨来源链接。
5. 模型可能返回正确内容却漏掉行内引用标签。为已完成且有真实检索来源的回答提供“参考来源”按钮，复用现有来源面板；不合成引用或把检索到的所有文件冒充逐条断言的证据。Wiki 源码抽屉按需挂载，避免隐藏抽屉抢输入焦点。

证据 QA 没有关闭，整批失败没有改成成功。独立模块生成允许先交付真实合格页面，旧 mobile 失败批次仍如实展示。

## GitLab 接入与持续使用

新来源 `64f3069e-3da3-49a3-8f5b-9da7b55e6340`，名称 `CodeWiki MVP mobile system module`，project 1051、master、范围 `src/pages/System`。来源经正常 API 创建，随后在真实 UI 完成配置、分支/范围预览及保存；**不冒充通过 UI 首次创建**。范围包含三个完整文件（Login.vue、NotFound.vue、SwitchMode.vue），不是截断正文的样本。

| 检查 | 现场证据与结果 |
| --- | --- |
| 初次同步 | 最初因模型配额停滞，取消日志 `0bf61ca3-a881-473f-b971-e999759906ac`，未冒充完成 |
| 修复后重试 | 日志 `b3b0f98a-29fa-4b8a-9dc1-5764ec286480`，success/published，3 文件、33 chunks/33 embedded，462.23 秒 |
| 固定发布 | SHA `15d9575ebea6d82d9bb1b69dfe2b9950c6eb4cd5`；snapshot `0c88466a-4667-4389-a497-2c45862e2d4b` |
| 再次同步 | 日志 `a0514fb5-0758-4ee6-be43-5038e683a43e` 成功，沿用同一 snapshot，不重复发布；累计 embedded 数不代表第二次新调用数 |
| 新来源 Wiki | `concept/source-64f3069e-3da3-49a3-8f5b-9da7b55e6340/module-18af0dd76f2aef8a`，v1，正文与三个固定源码证据；重开页面仍可读 |
| 受控变化与失败 | 隔离 Git 夹具 + 真实 PG/parser 测试验证修改/重命名、keyword/vector 失败保留旧发布；没有向业务 GitLab 分支写提交 |

冻结时所有验收来源暂停，保留发布和向量缓存。要演示重新同步，在知识库设置 → 数据源找到新来源，恢复后“立即同步”，查看日志；结束后暂停。不要恢复 nsb 全量。

## Wiki 产出

保留 dashboard 的已有 System overview（61 文件/2011 chunks）。本轮真实模型新增四张 ready 模块页：

| 来源 / 模块 | attempt | 产出 |
| --- | --- | --- |
| mobile / SensitiveInfoLog | `ef40d404-fa58-463c-a450-bb5f12dd0ae8` | 敏感数据导出记录查询模块 |
| mobile / ClassFeedback | `10f4d9c9-ba30-421b-a20b-1e16ceaea7df` | 课后反馈模块：详情展示与列表筛选 |
| mobile / System | `10bc8bbd-e1eb-4b4a-90e3-646e7b21450e` | 系统入口模块：登录、异常页与模式切换（真实 UI 触发） |
| 新来源 / System | 本轮来源页 v1 | 系统入口模块：登录验证与模式切换 |

每张通过现有正文和证据 QA，以上三个 mobile attempt 各两次模型调用、零修复；调用账本的 token 字段是预算统计，不能冒充实际账单。独立模块重试修复失败覆盖、且不推进失败批次游标的真实 PG 集成测试通过。没有实现整批 QA 失败后自动剔除部分发布。

## 自由问答和引用

| 检查 | 会话 / 结果 |
| --- | --- |
| Wiki 问答 | API `ee5609de-416d-4892-a889-65ff63a0a85a`，敏感数据查询；真实 UI `a2dc5dad-03e3-42a6-bc6e-9c6739e3726c`，课后反馈问卷组合，完成 45s/8 tools，读取 Wiki 与源码 |
| RAG 问答 | API `bb528ada-82fb-4d67-b68e-4105ad110ffd`；真实 UI `155b63d0-4444-4087-b216-bad7abd4b5dd`，SwitchMode 两种模式、学校来源和切换 API，正确回答并可打开源码 |
| Hybrid 问答 | 初次 UI `ba2dc15c-cbf6-47df-bf44-40744b72cf38` 的引用问题促成修复；最终 UI `035bf919-c722-4538-af68-3abf6d21dfa3`，反馈传参/问卷组合，33s/3 tools，答案核对正确；未输出行内标签，刷新后“参考来源”可打开固定源码 |
| 生成后自由问题 | API `ba03fbf1-38ff-4173-8432-9efd09570fc9`，新来源问题，14 引用均为新来源，并纠正“用户/员工模式”的错误前提 |
| 精确路径定位 | 新来源 SwitchMode 路径关键词搜索 5 hits 均为该路径与该 source；此项关闭向量匹配，不冒充混合检索 |
| 范围外问题 | API `e3975eda-a41a-4da3-80b5-0a52b26b45d5`，只选 System 来源询问 FreeTutor 后端 SQL，回答无法确认、零引用；未引入范围外 SQL |
| 刷新后引用 | RAG 会话刷新后仍可打开 SHA `15d9575...`、SwitchMode.vue L132–190，高亮 59 行（132 到 190），GitLab blob URL 固定同 SHA |
| Wiki 预览到源码 | Wiki 会话刷新后打开课后反馈页面，再激活 detail/script.js 的 e002 证据，源码面板显示同 SHA、L1–42/42 行；实际使用键盘激活验证 |

问题不是白名单，无预置答案。真实 UI 的 RAG、Wiki 和 Hybrid 均调用现有 Agent。API key 与账号会话所有权不同，账号会话经 UI 验证，未绕过所有权 404。

## 验证与边界

- 前端相关测试：SourceWikiModules、ChatReferencesDrawer、agentDrawerReferences 共 15 通过；referenceSources 13 通过；Wiki 预览证据路由 2 通过。
- Go 工具持久化/源码测试、embedding quota/dispatch 定向测试、启动器 TLS 测试通过；完整后端构建和前端生产构建通过。构建有既有大 chunk 提示，组件测试有未注册 TDesign 组件提示。
- 真实 PG/parser 定向用例：来源/租户/tag 范围；配额恢复与 deadline；完整 manifest/重命名；keyword 失败保留；vector 失败保留；独立 Wiki 发布与手动失败重试。均通过。
- 两个原测试夹具重复执行 migration 114，已去除重复迁移。vector 失败用例原 `indexing` 断言与流式批次的真实 `parsing` 阶段不符，已用原 HEAD overlay 复现同样失败后调整断言；旧发布保护断言保留。
- 没有重跑完整 T22，不宣称历史全包失败已消失；nsb 5448 文件全量、整仓批次 20–40 页、性能/完整故障矩阵、主库升级后移。
- 模型账户配额仍可能让新同步等待数分钟或让向量查询失败。关键词/grep 与已有发布可用，此轮成功不等于满负荷吞吐承诺。
- 范围外问题的一次模型回答错把实际 3 文件说成 2 文件，但拒答和来源边界正确；不是所有语义都完美。
- 修复前已经保存且丢失版本元数据的旧会话没有自动回填；新回答与其刷新记录验证通过。
- 会话自动标题模型曾把问题当成回答，生成很长的标题；已为 Wiki 演示会话改名，未将标题生成作为本轮功能修复。预览图片占位也有既有显示问题，不影响正文和固定源码查看。

截图位于本聊天 artifact 目录：`codewiki-module-ready.jpg`、`codewiki-rag-fixed-reference.jpg`、`codewiki-wiki-answer.jpg`、`codewiki-wiki-source-reference.jpg`、`codewiki-hybrid-fixed-reference.jpg`。不含凭据。

## 演示顺序与冻结状态

1. 打开上述入口和知识库 Wiki：先展示 System overview，再看课后反馈与系统入口模块；打开正文内的固定源码证据。
2. 新对话选 `T22 Acceptance RAG` / `T22 Acceptance Wiki` / `T22 Acceptance Hybrid`，添加本验收 KB，自由提问。回答的行内引用或“参考来源”可打开源码；“参考来源”展示检索返回内容，相关性仍需阅读判断。
3. 已验证会话直达：RAG `/platform/chat/155b63d0-4444-4087-b216-bad7abd4b5dd`，Wiki `/platform/chat/a2dc5dad-03e3-42a6-bc6e-9c6739e3726c`，Hybrid `/platform/chat/035bf919-c722-4538-af68-3abf6d21dfa3`。
4. 数据源中展示新来源的范围、固定提交、同步日志。需要现场同步时只恢复这个 3 文件来源并立即同步，结束后暂停。

最终健康检查前后端均 200，四个来源均 paused，原 8080/5173 仍运行。演示前端保持用户打开的终端，后端已通过新启动脚本恢复；程序未写业务远端 GitLab，也未推送 Git 分支或关闭 issue。
