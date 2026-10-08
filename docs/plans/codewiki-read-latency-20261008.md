# CodeWiki 文档与 Wiki 加载延迟诊断（2026-10-08）

环境：`source-integration/WeKnora`，分支 `codex/gitlab-code-wiki-rag`，隔离开发服务 57825/57826，数据库 `source_t22_live_rehearsal_20261006`。

## 已复现的原因

知识库 `65658207-a2ec-47fb-bf0f-11e7b685369e` 的文档列表和 Wiki 均在等后端读取。原运行日志：根目录列表 5.60–7.30 秒，Wiki 单页 6.71–7.22 秒；KB 信息、目录、标签接口约 19–30 毫秒。浏览器首次打开 Wiki 时多个请求并发，索引/attempts 约 23 秒，页面列表约 16 秒，后续目录约 8 秒。

实际仓库读取探针在同一份演示数据、同一范围和权限 SQL 上对比 PostgreSQL JIT：

| 指标 | JIT on | JIT off |
| --- | ---: | ---: |
| AcquireSourceRead 总耗时 | 7570 ms | 108 ms |
| Wiki 投影捕获 SQL | 7409 ms | 61 ms |
| EXPLAIN ANALYZE 执行时间 | 6448.14 ms | 38.258 ms |
| 规划时间 | 13.228 ms | 12.738 ms |
| JIT 编译时间 | 6412.242 ms（599 functions） | 未启用 |

JIT 开启时 Optimization 3813.249 ms、Emission 2504.272 ms，占据了大部分等待时间。复杂的源码/Wiki 范围谓词估算成本很高，触发了 JIT；少量结果的实际执行不足以抵消每条语句的编译成本。Wiki 的多次读取把这个开销累加并造成并发 CPU 竞争。

昨晚修复了“每个文件各捕获一次投影”的重复工作，使整页只捕获一次，但未消除单次捕获中的 JIT 编译成本，因此 4–5 秒仍不是合格的交互性能。

## 修复和验证

提交 `7615e7e1` 在应用 PostgreSQL DSN 中设置 `jit=off`，由连接建立协议应用于每一个连接，包括连接池新建/替换连接。没有修改数据库服务器默认参数、表结构、权限谓词、源码固定版本或 Wiki 的完整来源验证。

真实 PG 连接池回归测试 `TestPostgresApplicationPoolAvoidsJITOnEveryConnection` 修复前因 `SHOW jit=on` 失败；修复后两轮各三个并发持有的新连接均为 off，独立控制连接的参数保持不变。`AUTO_MIGRATE=false`，不对业务表执行迁移。

真实 PG/parser 的 `TestSourceWikiWholePageAndHistoryRequireEveryActualSourceInScope` 与 `TestSourceSharedQuestionRevocationAndPurgeOverridePinnedReads` 通过。应用后端从当前源码重新编译，由现有本机快速开发脚本重启。

## 当前开发服务的实际结果

现有 `.codewiki-dev-start.ps1 -Restart` 从当前工作树编译并成功启动修复提交，后端 PID 32268；前后端健康检查均通过。重启没有扩大 TLS 例外，数据源仍暂停，数据库版本仍为 121 clean。

| 真实浏览器请求 | 修复后接口耗时 |
| --- | ---: |
| 根目录文档列表（多次，包括并发） | 251–892 ms |
| `src` 文档列表 | 146 ms |
| Wiki 页面列表（首次并发） | 424 ms |
| Wiki 目录（首次并发） | 309–339 ms |
| Wiki 索引分组 | 113–173 ms |
| 课后反馈 Wiki 正文 | 128 ms |
| 正文 e002 固定源码证据 | 155 ms |

从新进程日志抽取目标 KB 的实际文档/Wiki 请求，用 2000 ms 上限验证：33 条读取记录的最大值为 892.35 ms，通过。数据来自服务端 HTTP latency，不包含 Vite 冷启动模块编译或浏览器渲染时间。

真实 UI 已验证根目录 13 个文件、`src` 的直接文件和子目录、Wiki 五张内容页与索引、课后反馈正文以及固定 SHA `15d9575ebea6` 的 `detail/script.js` L1–42。截图：本聊天 artifact 目录 `codewiki-latency-fixed-documents.png` 和 `codewiki-latency-fixed-wiki.png`。

探针仅创建并释放自己的读取租约；临时 Go 探针文件已删除。没有修改业务内容、执行全量同步或推送分支。

本次没有重跑 nsb 全量或整个 T22；性能结论针对当前演示数据，后续大仓库仍需做执行计划与吞吐验证。
