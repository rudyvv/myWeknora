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
