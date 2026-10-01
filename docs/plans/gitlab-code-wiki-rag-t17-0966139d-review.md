# T17 0966139d 双轴审查

完整干净冻结 `0966139deadd0762acdb147f897f5dd21ec872c7`，用户批准起点 `f040e5e838cfc1d755d332780882758f65610369`。两位独立 Sol/high 审查者分别检查 Standards 和 Spec。已验收 T18 合并不重复审查，重点为本票 ledger/runner/生成恢复及 113 迁移。

## Standards

文档化硬性违例 0。两项判断性 P3 Duplicated Code：首次生成逐章节 QA 在 source_wiki_attempt.go:238 重复同文件 :556 共用校验；新 attempt 的模型选择/slug 派生在 :430/:448 重复 :614/:619 已有 helper。非阻断，不要求扩大重构。

## Spec

**P1：未接入自动重启恢复/过期收尾。** 唯一生产 Claim 调用是 source_wiki_attempt.go:76，由显式 HTTP AttemptID 触发。未发现启动/周期消费者；Create 已提交但响应尚未返回时崩溃，用户没有 ID，旧 running 行持续占据 105 迁移的唯一模块槽。三分钟绝对 deadline 到期也不会自行变成 failed 或释放原文 owner。违反 ticket17:7/16、Spec:67/92 的服务重启恢复目标。需要实际启动的有界领取/收尾器，复用同 ID/阶段/计数/epoch；过期在同事务保留草稿和原因、释放 owner；活租约不抢占。不能用显式 ID 单元夹具代替生产入口。

**P2：未知模型上下文窗口绕过预检。** source_wiki_attempt.go:455 直接保存允许为零的窗口；ledger.Create:40 接受零；runner:53 仅正数才检查输入和完成额度。model.go:143–150 明确零表示未知。违反 ticket17:12 的模型预检固定有限额度及根批准合同。须在 provider 前解析已确认正窗口，否则可操作配置错误且零调用；不能沿用普通 Agent 的通用默认窗口作为已确认模型 profile。

原多来源 CAS 怀疑因未证实可达的公开写入口而撤回，不要求本票重造 T19 多源模型。现有 epoch、调用计费、页面 CAS、exact owner 事务没有发现其他已证实偏差。

## 根独立验证

- 真实 PostgreSQL ledger 五项：预留与旧 epoch、并发调用上限、过期崩溃 owner 释放、已知/失败 usage 结算、同 ID checkpoint，全部 PASS，包 4.938s。
- 真实 PostgreSQL service 四项及其子场景：限流共用预算、租约丢失晚返回、显式 ID 的 QA 阶段恢复、模型/源/页面/发布围栏，全部 PASS，包 27.516s。显式 ID 通过不能证明自动重启恢复。
- 根临时 overlay 的未知 context 实际 provider 反例 FAIL 6.98s：配置未知仍两次 provider 调用且生成 ready，证实 P2。
- 过期 crash-create 后新的手动生成反例 FAIL 8.04s，包 12.676s：旧 attempt 仍 running，唯一槽未收尾，证实 P1。夹具只写隔离 source_test，不修改冻结源码；临时脚本、JSON 日志保留本机。
- 根首次过期反例使用 jsonb_set 对 json 字段导致夹具 SQL 类型错误；修正显式 cast 后仅重新跑该反例。初次类型错误不计产品 finding。
- Worker 全部控制 runner/HTTP/编译/vet 证据已收取；旧宽 PG suite 的 HTTP 夹具漏 113 已修并定向通过，未把未收取或运行中结果算 PASS。

## 决定

未通过，不集成/关闭 #25，不派该对话下一票，累计仍 16/22。两项具体必修已直接交原 T10 Luna/xhigh 对话，限定实际启动恢复与确定模型预检；不重跑无关宽套件。T15 在原 T18 对话继续独立 planner/覆盖和最小父 budget 接缝提案，不能复制未验收 T17 或同时修改共用 runner。原 T11 已验收，等待 T15 解锁 T16。

Standards 硬 0 / 判断 2（P3）；Spec 2（P1、P2）。
