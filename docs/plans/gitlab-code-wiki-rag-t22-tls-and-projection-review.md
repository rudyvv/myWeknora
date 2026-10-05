# T22 临时 TLS 与问答固定卡片审查

正式整票起点 `1d32c08e`。TLS 窄起点 `24ea2183`，冻结 `ded7780a8914dec39bc2d5e600cc630779f87226`；Wiki 窄起点 `bae1c65a`，冻结 `507e29656915d3985c86ed9ff0927747346d9fcf`，夹具修复 `0980e92cacd283e61db8ba2fae96f138517df723`。两树 clean/nonempty diff 已核验。采用 code-review 双轴，审查者按用户指定 Sol/high；两次容量错误不是审查通过，之后恢复并取得完整终态。

## Standards

TLS：文档硬违规 0，smell 判断 0。

Wiki：文档硬违规 0，possible Duplicated Code 判断 1。projection 的 allRefs 与 live predicate 重复部分来源引用/权限检查；两者的固定投影及实时读取差异支持分开实现，这是维护风险而非必修。0980 仅删重复迁移，不增加 finding。

## Spec

TLS：明确合同违例 0。精确 HTTPS origin、最长24小时、默认验证、合法到期配置恢复验证、逐请求独立 transport、redirect token 隔离、原 SSRF 规则与 API/bridge 专用策略均有对应代码。未实际启用。

Wiki 静态：明确合同违例 0，单MVCC捕获、完整正文/贡献/owner、预算全量或零、当前权限与 raw 完整性、租约 owner GC、普通 Wiki union 可追溯。0980 删除 Java 子夹具重复115/116/119，共用夹具仍执行真实迁移，未改生产schema/断言。静态审查不替代数据库行为。

## 根独立复验与接受边界

TLS：正常宿主实际12顶层定向 tests全部 PASS，datasource pkg5.084s / GitLab client4.820s / source5.024s，session39554已收；默认拒绝自签名、精确例外、其它origin、既有client到期、合法expired constructor、非法配置、跨origin/降级token隔离、Git进程环境白名单实际执行。日志 `%TEMP%/weknora-root-t22-tls-ded7780a.txt`。已本地 merge `93b6e8ac23d64b779b956e0b9c0f336eb190efd0`，仅此 slice 接受。

部署候选 `C:/Users/28211/.codex/test-runners/weknora-t22-tls-server-ded7780a.exe` 已构建成功。首次构建缺系统sqlite3.h失败；按repo既有setup_sqlite_cgo.sh方式，在专用runner目录生成转发至go-sqlite3官方binding头文件的shim，进程内CGO_CPPFLAGS再build成功，session96207已收。未修改Go module cache/项目代码/系统安装。日志 `%TEMP%/weknora-root-t22-tls-build-shim.txt`。此二进制未启动、TLS例外仍关闭，现有用户8080后端未停。

Wiki：507e 根两case实际 FAIL17.843s，newSourceWikiFixture重复115导致42P07，尚未到行为。0980首次重跑实际FAIL4.778s：专用PG/parser在暂停期间退出，未到行为；只恢复根自有57822及57823容器后再跑。第二次两case真实 FAIL31.981s：公开 Agent search/read新SQL引用不存在的source_files.deleted_at，42703；源码grep旧快照仍正确。日志 `%TEMP%/weknora-root-t22-wiki-0980e92c-runtime.txt`，session46616已收。已交同T06最小product修，用真实knowledges删除状态/source lifecycle，不加虚构schema，不放宽断言。产品未接受，原Hybrid未绿，不集成Wiki/关闭T22。

独立新增投影测试由原T10负责；负例必须识别明确not-found/授权/租约拒绝，不允许任意SQL或环境错误算通过。仍等待干净冻结与候选上的根实际执行。现场真实GitLab/模型/性能/30题评分未通过，工具/切片通过不等于整票通过。未写GitHub过程评论、未读取旧Docker凭据。

## 9800 修复与独立测试前沿

冻结 `9800de09679184c7bdceb8373cfe67b27e7472df` 仅在 owner SQL 加入知库 id/tenant/KB/type 精确 join，并使用真实 `knowledges.deleted_at`；未修改schema/断言。Sol/high窄 Standards 0新硬/0新判断、Spec 0。根原 carry-forward 与原 Hybrid 两用例全部实际 PASS，pkg21.851s，Hybrid子3.79s；session36317已收，日志 `%TEMP%/weknora-root-t22-wiki-9800de09.txt`。旧raw与新applicability分离、旧问答跨发布检索正文均已真实验证。

T10新单文件由Root host保存干净 `a7cb1ac8d1850c46a90d8380a47d471944f87b99`，compile-only pkg4.089s `[no tests to run]`，不是PG行为通过。Standards完整0硬/0判断；Spec发现两个测试阻断：GC case的source lease本来固定raw旧快照、故整个旧检索快照正确受保护，不能要求索引被GC；单页read负例只排BODYmarker可能忽略标题/summary/ref泄露。已交同Luna修GC前置carry-forward及严格单页拒绝/search空结果区分。Spec最终容量故障不算完整终态，下一冻结仍需完整review。Root曾把先前WIP的marker值捕获问题误派给该冻结；读取精确 `git show a7cb` 确认已是atomic generation，及时撤回，不要求重复修改。

TLS实际启用脚本只在原main准备外部runner，默认metadata-only dry-run；编译后的后端仍未启动。必须在准备完成、复审及明确启用/后端重启确认之后启动；不会为busy-port测试提前执行 `-start`，避免端口退出竞态导致实际启用。三个chat被用户中断后已恢复各自未完成的原任务；T06产品保持9800冻结等待独立门禁，不把idle当新票验收。

## 现场 TLS 启用、回退与兼容构建

用户明确批准仅 `https://gitlab.p.it` 临时例外1小时并重启8080。根先保留原server二进制及完整已接受构建，精确校验当前listener/process identity再停止旧server；初次ded构建启动health200，截止 `2026-10-05T15:23:33Z`（北京时间23:23:33）。实际知识库设置读取失败；根只提取受保护本地log的错误类型，确认现有数据库缺source_cleanup_operations/publication_outbox/snapshot_gc_candidates/sync_states/wiki_attempts与新字段，不能把health200当完整页面可用。根立即恢复旧二进制、移除进程TLS例外，health200并实际UI设置恢复；未迁移共享数据库或更改原Dmain/系统trust。

现有原D clean基线 `b477f690d2090dfebdf3524ec31e8cb78158f997`（原二进制无VCS字段）上建立独立兼容分支，复用原a8ea聊天/树，不创建重复聊天。Luna冻结 `88fa74c5ec1cfd75feb780f169def00f5644b920`，仅4files：相同GitLab专用policy、API构造接线、合成测试；不携带源码模块/Wiki/schema变化。Root两policy blob与ded完全相同；完整Sol/high Standards0硬/0判断，Spec0；根实际9顶层定向TLS/API tests全部PASS，pkg6.255/5.979，session54864收，原runtime protobufwarn/shim build成功，session39956收。此兼容slice接受，原完整功能DB升级仍需单独准备。

新外部launcher源码SHA256 `71B4179C22A4FF767B64336282CBE74518EDC578B426091D1D5601FF9E4E5C46`，binary `CF264B74D961065D85C4F24EE43501245A5CCA9106759D7E9E94650FD0A87BD2`。根比hash/读源码/实际dryrun后，只启88fa兼容backend；沿用最初批准15:23:33Z截止，未延长窗口。当前server PID38476，exe `C:/Users/28211/.codex/test-runners/weknora-t22-live-tls-88fa74c5.exe`，launcher PID13692，health200。启动信息onlymetadata在test-runners/weknora-t22-live-tls-launch.txt，app内容log保持本地不向聊天/GitHub输出。

原IAB表单已关闭，新表单被密码管理器自动填入WeKnora登录邮箱及来源不明秘密值；根只更正GitLab base地址，未提交自动填充值。用户明确确认已覆盖个人令牌为GitLab只读令牌后，根才点击测试。实际UI显示「已连接」，令牌未读取/打印；用户会话及知识库设置正常。只证明API连接，未创建数据源、拉取代码或发送源码给模型，仓库权限/模型/性能/30题评分仍待现场门禁。临时模式不是正常证书链预检通过。

## 最新投影行为与独立覆盖修复

真实17MiB public metadata-only容量用例 `db91409b` 已通过6.88/pkg11.414（session69661收）：候选确实ready/完整owner、触发实际16MiB预算、Wiki明确capacity_exceeded且零partial，同lease源码关键词RAG仍可用，release FK/cascade全清。完整独立两轴0。候选3f2077c上的四独立门禁首轮2PASS/2FAIL pkg36.940：oldbody12.71/after-start-ready排除4.43通过；share负例只因严格helper漏识别实际access.ErrForbidden失败，GC最后索引因合法1分钟next_attempt_at延期未到期失败。Root已具体诊断，不改生产授权/GC保护。

独立Spec还发现旧getter会在读取固定投影后重新用live贡献/发布判定，把captured ready改stale，违反冻结元数据；原测试仅body/version遗漏。T06最小service修冻 `7fc39ff396dd32d6e4173c8488d8407c33010475` 两文件，仅durable WikiAnswer getter保持投影state，普通导航/编辑仍live。T10修冻 `0112223192370cbfaae57c4674bbf560214ef82e`：slug/ID完整provenance+ready断言、matching Scheduling的stale/late搜索、明确access.ErrForbidden、证明lease/scopes/所有owner释放后仅推进对应GC测试时钟。根候选 `842f17407613c580df0034d3142edaddd9e79d8a` 未接受；用精确旧getter3f版本的Gooverlay验证新ready断言（61683运行中），然后执行修后实际门禁；不把测试候选合并当根branch集成/票接受。
