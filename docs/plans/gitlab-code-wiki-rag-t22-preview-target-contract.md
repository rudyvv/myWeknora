# T22：预览确认目标的条件手动同步

验收起点98b6b7428def37c527717916961e86ec9aeb8040。这是nsb完整范围发布前发现的预览→worker HEAD竞态修复，不代表已经发布或T22通过。

## 产品合同

现有Admin/KB授权的手动同步API允许可选 expected_commit_sha（完整小写40位hex）。旧空请求行为保持。字段不是仓库/分支配置，不允许任意历史选择：服务应在当前已登记源、分支和授权下解析HEAD并核对请求值；不符则在登记同步任务前拒绝。源码以外模式或没有durable协调器时拒绝该选项，不静默忽略。解析完成后在协调事务中登记请求的确定target_commit_sha，现有配置代数/租约fencing保留。worker及崩溃恢复已支持已登记target；分支随后正常推进仍处理原固定目标。获取旧目标失败必须明确失败并保留上一发布，不替换为新HEAD。

与普通信号共用同源串行和待追赶规则；被更新信号取代的待执行请求不能冒称它已发布。原GitLab只读权限、master分支和完整5448文件范围不变，不要求/声称冻结真实远端分支。产品实现及真实回归完成后才可解除acceptance source-publish --publish的准备态拒绝。

## 先冻结审查的真实回归夹具

新增公开HTTP→service→durable PG→worker→GetSyncLog seam测试：使用现有真实Git/parser/双索引夹具，先预览并用HTTP提交expected commit，再修改测试仓库分支HEAD，断言最终发布仍是旧目标及重复投递保持同一发布。未执行目标源码；模型仅受控边界替身，不代表真实模型业务评分。

既有SOURCE_TEST_POSTGRES_DSN仍须127.0.0.1/source_test。新增明确opt-in仅可把数据库字段换成已存在source_t22_live_rehearsal_20261006，且必须port57822/sslmode=disable恰好单值/无附加URI参数；有效pgx配置须无TLS/fallback。只在随机source_test_UUID schema创建synthetic fixture表/索引并清理自身schema，不写public的真实验收KB/源/账本/任务，不改扩展、Redis或原8080。clone路径只只读核验vector/pg_search均已存在，缺失拒绝，不安装扩展。禁止输出DSN或密码。根在精确测试/fixture冻结经独立审查后才执行它；基线的真实失败是预期red，不得算验收成功。只跑该新增测试，不重复已接受T06/T09/T10。

初冻8435594独立审查要求收紧连接与public回退。修正的测试helper仅在根自有57822 PostgreSQL既有admin权限下创建UUID同名的NOLOGIN/NOINHERIT/非superuser/无CREATEDB、CREATEROLE、REPLICATION、BYPASSRLS的临时角色，令其只拥有新synthetic schema，绝不给原应用或既有角色增权。测试工作连接在每次连接ValidateConnect前固定runtime role/search_path，并证明current_database/current_schema/current_user精确、角色无管理能力、public无CREATE、全部public表无写权限、全部非扩展应用表无SELECT、public sequence无USAGE/SELECT/UPDATE。只允许pg_depend明确标记为扩展成员的对象沿用既有只读权限，不授予或撤销public权限。保留public搜索路径仅为现存extension类型/操作符解析；缺少synthetic应用表时应permission denied，禁止回退读写真实public应用表。退出先关闭工作pool（在Open前注册，即便连接拒绝也关闭），再只删除自己创建的UUID schema和NOLOGIN角色，最后关admin。admin也在每次连接证明固定clone，仅创建/清理自有schema/role和读extension元数据，不写验收对象。

实际5b/144测试均在fixture连接证明失败，尚非竞态red：安全诊断确认clone/schema/role均正确、非管理角色/publicCREATEfalse，但“全部public对象无SELECT”过严。根只读ACL核验得到public扩展只读授权5、应用只读授权0、所有写授权0、测试schema/role残留0、clone121clean/全局静默；因此将证明区分扩展元数据与应用数据，并增加public sequence拒绝，不降低实际应用数据隔离。再次审查后才可重跑该测试。

测试fixture的额外导出只存在于integration测试二进制。第一产品slice仅handler的有界JSON/完整SHA校验与typed context条件：公开API malformed expected commit测试先真实FAIL400vs200，再PASS3.619s；服务验证与协调器持久化尚待后续slice，不能启动或称race修复。integration编译4.286s/no tests to run不是行为通过。后续冻结代码需独立Standards/Spec与安全Sol审查，测试命令/结果和准确SHA分别记录；最后整票完成时才一次全套测试。

## 2026-10-07 服务slice实际进度（TRAE接手）

在 `553afa0` 基线上，三个未提交产品文件（service expected-commit验证、repository `RegisterSourceTrigger` 持久化 `target_commit_sha`、types 内部传递字段）与真实竞态red（`t22-preview-target-red-553afa0.txt`：worker发布新HEAD而非旧预览SHA）一致。定向candidate一度失败 `unable to fetch the fixed GitLab commit`。

根因诊断（非GitLab token权限）：fixture本地upload-pack默认拒绝分支推进后非tip的unadvertised want。根以只读Git凭据对真实 `gitlab.p.it/zhangruiliang/nsb` 实测：产品同款 `git fetch --no-tags --no-write-fetch-head --depth=1 <url> <非tip可达SHA>` exit 0，证明真实GitLab服务端允许按可达SHA fetch。修复为fixture基础设施对齐真实服务器行为：repo配置 `uploadpack.allowReachableSHA1InWant=true`；同时fixture Git HTTP handler不再因upload-pack按设计写ERR包后非零退出而判死测试。产品三文件未改动。

定向green后补充四个真实回归（均通过，日志 `t22-isolated-runtime-logs/t22-preview-target-trae.txt` 与五测合并运行 exit0）：登记前HEAD已变拒绝且不留sync log/run；目标因force-push不可达时明确失败并保留上一发布；崩溃后 `RecoverSourceTriggers` 重投仍发布登记的固定目标；配置更新通过公开 `UpdateDataSource` seam使queued expected-commit run被fence取消、迟到投递不发布、新trigger在新配置下发布。

## 2026-10-07 冻结与三轴审查（TRAE子代理，非Sol）

首冻 `49f0238b`（产品三文件+fixture对齐+四回归+文档）。并行独立子代理三轴审查：Standards 0硬违规（判断项：connector→resolve形状在包内第三次出现、repository层弃用ctx返回值仅作校验、兄弟测试fixture未同步align、CONTEXT.md缺"expected commit"词条，均非阻断）；Spec 1缺口——document模式与无durable协调器两条拒绝路径已实现未验证；安全 0阻断/0 major（授权链完整、SHA格式处处强制、argv无注入面、TOCTOU符合合同设计、错误信息仅泄露1位HEAD漂移oracle且预览本可见、租约/代数fencing完整）。

Spec缺口以两个新回归修复（document模式400且不留log；接口裁剪掉SourceSyncControlRepository的服务拒绝该选项），七测合并 `^TestSourceManualSync(…)$` exit0（日志 `t22-preview-target-all-seven-trae.txt`）。**最终冻结 `4e4fc3b425935eddf334823e5ea226cf042db568`**，树clean。

全量service integration包与基线对照（最终修正）：45m超时完整重跑共37个失败。其中9个在干净 `553afa0` stash基线同样失败；其余28个（Wiki batch 42P07顺序依赖、AES-key/Python-verifier/sandbox环境依赖、parser rules漂移类）经**已验证真正切换**的 `553afa0` 工作区基线（fixture的allowReachableSHA1InWant改动确认已从工作区消失）同样失败——**37个全部为预存失败，非本slice回归**。注意：一次先前的28测“基线”因后台沙箱阻止checkout文件写入、工作区实际仍是新代码而无效，已作废并用前台强制切换重做；全包此前从未整体运行，这些预存失败属既有树内/环境问题，留待整票最终全套时归口处理，不在本slice顺手修。整票结束时的最终全套测试仍待执行。
