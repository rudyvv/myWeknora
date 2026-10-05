# T22 现场部署预检

记录时间：2026-10-05。正式 T22 审查起点 `1d32c08e`；已接受功能树 `55322bfc`，Wiki 合入 `3a17ede5`。此文档记录部署准备，不表示 T22 验收通过。

## 已完成的实际检查

用户确认在表单中填写 GitLab 只读令牌后，API 测试实际显示「已连接」。仅此连接已通过；没有保存数据源、同步代码、调用真实生成/Embedding 模型或给 30 题评分。临时证书例外仍沿用首次批准的截止 `2026-10-05T15:23:33Z`，没有续延。该时间已到，逐请求策略恢复正常验证；正常 CA 信任尚未解决。到期后的 localhost:8080 健康检查实际为 200。

当前应用为原版 `b477` 加已审查的 TLS 兼容切片 `88fa74c5`。新源码功能仍在根集成树，原 D 盘 main 和当前数据库没有由本轮执行升级。先前完整功能二进制使用旧工作目录时，健康接口成功但知识库设置因缺表/列失败，已回退；不能以健康接口替代业务页面检查。

只读预检 helper 默认模式只检查文件存在性；显式模式才在进程内读取原应用受保护配置，按原 native runtime 的数据库规则连接。所有元数据查询都位于已经确认 `transaction_read_only=on` 的事务中，结束回滚，不迁移、写入或输出凭据/用户内容。

| 检查 | 实际结果 |
| --- | --- |
| 数据库连接 | native loopback 5432；成功 |
| PostgreSQL | 17.9，`server_version_num=170009` |
| 迁移状态 | 恰好一行，版本 **103**，`dirty=false` |
| 数据库大小 | 290,928,307 bytes，约 277.5 MiB |
| 102–119 所需 source 表 | 34 张中 9 张存在，25 张缺失 |
| 相关扩展 | `pg_search=0.22.2`、`vector=0.8.1`；完整列表在本地脱敏报告 |
| 备份客户端能力 | 根专用容器内 `pg_dump`、`pg_restore`、`psql` 均为 17.9，仅执行版本命令 |
| 备份目录 | 外部 test-runners，已禁继承；仅当前操作用户、SYSTEM、Administrators，无其它 Allow 规则。首次预检时未建归档，后续实际备份结果见下节。 |

预检源码冻结 SHA256 `794ED5C734E3E85A52A5E068C08F31ED626C3835CBB781A2F1E5AF5B7929159A`；根独立构建 exe SHA256 `9222DF7FD82404E317E9E450680A13E3C0CBACA3F4EEF20A1F40D7391F0BC7E5`。原冻结的 Spec 两项问题（early-return 状态为空、NULL ledger 误报 ok）已经最小修复；窄 Standards 0、Spec 0，均为独立 Sol/high。根独立 build、默认 dry-run 和实际只读查询均已完成，不能把 worker 未连接模式当实际查询证据。

本地脱敏报告 `C:/Users/28211/.codex/test-runners/t22-live-schema-inventory-20261005.json`，SHA256 `63D9F6DBF6070A5DF94F2B81EE67193BDCE8132A780BE97603FB829E0FEE7765`。不包含 DSN、数据库用户名/密码、模型密钥或用户行数据。

## 备份与隔离预演进度

备份门禁已进一步完成：外部 helper 冻结源码 `EF81A28DB8B9051BDE51615E8C0716A636E99BED4AA300323D31C11B5DE1C5AE`，根独立构建 exe `72E27170DA63FD515C6ACB6C05E60E6BB596B267347A811E0F3C0564899405C2`。独立 Standards 无硬违规（1 项非阻断 stderr buffer 维护判断）、Spec 0；根独立默认 dry-run 通过。根在同一执行前再次核验固定目录严格 ACL/owner/非 reparse，再执行仅源端只读 pg_dump。实际正常退出，session70702已全部收取；开始 `2026-10-05T16:00:28Z`，归档于16:01:02Z前完成。

新归档 95,670,858 bytes（约91.2 MiB），SHA256 `DE017BE949435AAE871A1ABFBD7C6D382504523430987CE61900CAFE0964F2AC`。保留在严格 ACL 的外部本地备份目录，不复制到仓库/聊天/GitHub。根额外通过二进制 stdin 调用根自有 PG17.9 `pg_restore --list`，实际 exit0/757 条 TOC 项；没有输出或保存原 TOC/stderr。归档可识别，不等于完整恢复已验证；尚未执行 restore 或迁移，也不将在线备份当未来正式维护窗口的最新回退点。

上述是备份完成时的状态。随后根已实际完成隔离恢复和迁移，见下节；不得再次以相同目标运行新建数据库预演，也不得删除目标绕过已存在保护。

1. 完成、冻结并审查外部备份 helper；默认模式不连接、不读取配置、不创建 archive。显式备份由根在同一调用前再次核验固定目录的 ACL 和路径，源端仅 `pg_dump` 一致性只读备份。密码仅以子进程环境传入 Docker exec，参数不含密码，stdout 直接以二进制写独占新文件，stderr 不输出原文。保留 owner/ACL metadata，不使用 `pg_dumpall`。
2. 仅在备份正常退出、文件 sync/close、散列和归档检查完成后才可作为恢复候选。部分文件保留并标记不可用，不能以存在性或非零字节数当备份成功；不自动删除历史文件。
3. 恢复和迁移只能先在新隔离预演数据库进行，不以原应用库或既有 `source_test` 为目标。只恢复到空目标，恢复阶段抑制原 owner/ACL，保留原备份用于另行评估回退。不能启动连接原 Redis 队列/原文件存储的克隆应用。
4. 对预演库执行确切版本 103→119 升级，使用冻结根目录的迁移文件和明确的绝对路径；不调用会打印凭据的 `scripts/migrate.sh`，不 `force`、不自动恢复 dirty。核验扩展/对象、旧文档与配置计数、单行版本119/dirty=false。实际执行结果见下节。
5. 预演通过后，准备完整应用和前端、解析服务、配置/存储/队列切换以及可恢复操作步骤，再形成具体现场部署请求。当前用户批准的仅是已经执行的临时 TLS 重启，不能当作已经批准任意共享数据库升级。

## 已查明的迁移注意事项

104 起是本次实际待应用的迁移。源序列缺 106 文件，但编号空洞本身不是缺少必需迁移的证据：锁定的 golang-migrate v4.19.1 在 `migrate.go` 使用 `sourceDrv.Next`，`source/migration.go` 返回排序索引中的下一条实际版本。禁止为连续编号补一个虚构 106。

源端多数 down 会丢弃新增表、字段及证据数据，不是保护性回退；116 的 down 会对旧唯一键冲突明确拒绝。111 的历史搜索投影循环、112/113/115/116 回填和普通索引/DDL锁都要在隔离库测量。驱动默认把整份迁移交给一次 ExecContext，但这不证明整个 103→119 升级是单一事务；dirty ledger 与实际残留必须独立核验。

现有应用 startup 使用相对 `file://migrations/versioned`，并允许迁移失败后继续启动；`initDatabase` 的自动 dirty 恢复默认与直接 `RunMigrations` 不同。完整部署应先完成明确的手工升级和核验，再强制进程 `AUTO_MIGRATE=false`、`AUTO_RECOVER_DIRTY=false`，避免启动时隐式改库或把失败隐藏在 health200 后。

## 未完成的 T22 现场门禁

正常 GitLab TLS/仓库权限、完整代表仓库过滤与首轮/增量同步、真实模型及索引交付、30 题人工评分、大仓耗时与资源预算仍待实际验证。工具、Wiki 切片和只读部署预检通过不等于这些门禁通过。当前仍为 21/22；#30 保持 OPEN，不写 GitHub 过程流水。

## 隔离恢复和运行准备的实际结果（2026-10-06 北京时间）

预演 helper 最终源码 SHA256 `E4E5C2435145CA75FAF04B7C26B97A7A94601841A815403388BC7D1A1976661E`，根独立 exe `DA153AF36F6F8C833CFACAC84BA9590D507D886B6C12F3EBA016088A7E4EC9B0`。独立 Sol/high Standards 无新增硬违规，Spec 两项原发现修复后为 0；根核验备份 ACL/散列和迁移清单，实际执行 session93989，exit0 已收。新隔离库 `source_t22_live_rehearsal_20261006` 已创建并恢复成功：103/clean→119/clean，34 张所需 source 表齐全，扩展版本不变。总计16秒，恢复16秒；其它阶段整秒计时小于1秒，不表示没有执行迁移。

七项旧数据计数升级前后完全一致：knowledges117、knowledge_bases4、models2、data_sources10、wiki_pages3004、wiki_page_revisions1281、chunks8936。本地脱敏结果 `C:/Users/28211/.codex/test-runners/t22-isolated-rehearsal-result-20261006.json`。104–119 实际15个 up 文件清单 SHA256 `CFC6742D73BCABE41110EFFE389F7BB9ECAE1180B0A6E69FEB62A10C0CC63024`，106 编号空洞保持。原 live 库未迁移。

根已构建完整已接受55322功能后端，exe SHA256 `8C617E82BFB6C954E357F6A785EB2BD33D60E0B8B94EA1D2C53B48947E8451F1`；前端真实 production build 通过。尚未启动克隆应用：先读取后台待处理任务及外部存储引用的聚合清单，必要时仅在新克隆库中停止旧任务，再使用独立文件目录、解析器57823和新隔离Redis57824。新Redis认证PING已通过，随机认证秘密仅在严格ACL本地文件/测试进程内；未读旧容器凭据或改共享Redis。

原批准KB两模型元数据真实只读核验通过：Doubao embedding dimension1024，tokenizer空、max_input_tokens0；Deepseek生成模型active。两者凭据配置存在，但尚未执行真实模型调用。未知输入预算不得当已验证厂商参数，后续验收配置需明确保守估算语义。

用户最新明确允许自行延长过期证书验证例外。后续仅为 `https://gitlab.p.it` 注入明确截止的进程级窗口；不延长系统/browser/global信任，也不修改旧8080进程的原截止。正常Git凭据管理器实际三次 master ls-remote均exit0：nsb `c5e128035cd1d0178184a24f164673988c3f3036`，dashboard `77d0e952808c8588d19e35f823dd887e347b637f`，mobile `15d9575ebea6d82d9bb1b69dfe2b9950c6eb4cd5`。这只证明 Git 读取权限；尚未取得API项目ID、同步、索引或30题评分。远端变更与旧本地题集定位需核对，不能假定相同版本。
