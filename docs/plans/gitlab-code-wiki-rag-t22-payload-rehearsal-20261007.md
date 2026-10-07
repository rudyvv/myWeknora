# T22：专用 clone 的 T09 缓存回填实际验收

本轮起点 `7a7146e009e823ba04f3558856b8b45dc5b9d139`，分支 `codex/gitlab-code-wiki-rag`，开始时干净、领先远端97提交。原 D 盘 main `b477f690` 和原8080应用保持不动。GitHub #30仍Open；本项通过不代表T22完成。

## 冻结与审查

原忽略helper的五Go文件与用户交接哈希一致；原plan实际 `9E82E3185E8196698063DA8FD64BB61A6284E8098375EA065AD30E6730512510`，不符交接 `A67E92…`，不能称原冻结材料完全相同。根将可复核副本跟踪到 `scripts/acceptance/source-artifact-payload/`，初冻 `72b408c858a7b2e8d0942495f6bfbedbf3c30833`。migration121 Git内容未变；原集成工作树CRLF导致原字节哈希不符，窄 `.gitattributes` 将其固定LF，实际SHA恢复批准的 `2D09D4CAEEA5B2528661CBCB282A3CF7BFFC059225995C5387FD2D14B9D86540`。

初次真实upgrade退出2（`migration_upgrade_rejected`），未到写入前proof。随后只读核验仍120/clean、无活动会话或任务。公开CLI `--preflight` 在只读事务真实复现 `SQLSTATE42601`：旧合并SET LOCAL语法无效。拆成两条合法语句后真实回归绿。代码冻 `23d2cb5543ce230069fdd52fb03718ce33659af1`，文档修订最终冻 `5cdadd8ee848935ab0e6914155911bcb2dc39252`，冻结时干净。

独立Sol/high Standards：0硬违规，保留一次性helper职责集中（Divergent Change）的非阻断判断；文档矛盾已修。独立Sol/high Spec：0必修；预检不替代升级路径的三state行锁与存在性校验。针对性Sol/high安全复核：0新增阻断；进程/会话检查后的外部TOCTOU由根独占维护窗口控制，整个升级、回填及连接关闭期间未启动后端或其他clone客户端。

最终main SHA `919F7D51FA2DC755E913D9C0A2B13E34BD899F7A6BBBA1E302A8F86C3ED3E9E2`；plan `B9B5C31C60B562D81ED347BC1AEE4E7E1937BB9F5FFF555FAD7EE97472C7B533`；公开只读integration test `D492C4C322659D8F05DD4B8BBC4FD5CC0F37DDA38B55B4317F18FFDAA54B794F`。根Windows exe SHA `913347AA1E556429B799106F3D1F0A55AC86B00D189EFB615B7C7E2146F62583`。

## 真实执行

只连接既定 `source_t22_live_rehearsal_20261006/public`。写入前只读检查120/clean、固定tenant/KB/三源存在且暂停、三state无active/pending/lease、全局任务清静、固定backend哈希有效且进程0、其他clone会话0、prepared事务0。源缓存为parsed2147/vector4276。

升级仅经审过helper、正常advisory锁、单事务SQL+账本执行120→121：exit0，外部耗时1559ms；随后只读121/clean。回填仅固定三源NULL元数据：exit0，parsed2147/vector4276/总6423、remainingNULL0，helper耗时50852ms、外部51503ms。102次写入前proof记录三计数全零。单批不超过64、总行数不超过10000；外部watchdog硬限180秒，未触发。原文、payload、索引、发布、配额及授权未由回填改动。

三对旧/新完整配额聚合各在同一REPEATABLE READ READ ONLY事务执行，顺序交替。每源三类字节完全相同；三次旧/新查询集合耗时1142/59、964/57、994/61ms，中位数994/59ms。只证明此clone/数据/硬件的同公式一致性与查询观测，不是全量吞吐或T22性能结论。

| 固定源顺序 | 原文字节 | parsed-cache字节 | vector字节 |
| --- | ---: | ---: | ---: |
| nsb | 100870577 | 153208314 | 16003072 |
| dashboard | 3417087 | 6998675 | 15228928 |
| mobile | 661125 | 2126090 | 5726208 |

配额统计包含该来源仍保留的原文与缓存，不能拿上表替代批准入场范围5611文件/约92.35MB。CPU实际i7-13700H，14核/20逻辑处理器，总内存34016759808bytes。

## 可复现命令和证据限制

凭据只由既定受保护runner注入进程，不打印环境。定向 `go test -count=1 ./scripts/acceptance/source-artifact-payload` PASS2.030s；`go vet` PASS；`go test -tags=integration -count=1 ./scripts/acceptance/source-artifact-payload -run '^TestPreflightChecksRealMigrationTransactionReadOnly$'` 真实只读红42601后PASS2.544s。Windows构建成功；最终修复版Linux/amd64交叉构建也已成功（2026-10-07根独立执行，产物t22-payload-backfill-fixed-20261007-linux）。未跑最终全套测试。

真实顺序为公开 `--preflight`，根外部180秒watchdog执行 `--upgrade-only --stop-proof=process-and-clone-sessions`，只读账本核验，再同watchdog执行 `--backfill --confirm-app-stopped --stop-proof=process-and-clone-sessions`，只读确认NULL0，最后 `--measure --confirm-app-stopped`。升级已到121，不可再次执行120→121；若watchdog/提交结果不明，先只读核态、审查再决定，禁止盲重跑。

本地脱敏结果保留于根test-runners的 `t22-payload-upgrade-fixed-20261007.json`、`t22-payload-backfill-fixed-20261007.json`、`t22-payload-backfill-fixed-proof-20261007.jsonl`、`t22-payload-measure-fixed-20261007.json`。报告不含DSN、密码、模型密钥或源码。全范围nsb发布、大仓Wiki/mobileQA、真实前端引用、固定30题评分、增量/故障/完整性能仍待后续实际验收。
