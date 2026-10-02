# T15 90cb5d63 审查

完整干净冻结 `90cb5d63b076b4578e98553720508f18133f9224`，固定批准起点 `999e9398b8e4f67d17ba8e03c3fc65ba4b15332a`；命令 `git diff 999e9398...90cb5d63`、`git log 999e9398..90cb5d63 --oneline`。独立 Sol/high 两轴审查；已验收 T17 不重审，辅助 `git diff 3369e189...90cb5d63` 聚焦 T15 的实际变化。

## Standards

文档化硬性违例 0。两项判断性 P3 Duplicated Code：handler/source_wiki_batch.go:17–102 的预检/启动 request validation 与错误映射重复；service/source_wiki_batch_read.go:12–84 三个读取入口重复 scope/KB/tenant/source 查询设置。均为非阻断重构建议，不作为文档化规范违例，不重复已验收 T17 的旧 P3。

## Spec

六项必修（2 P1、4 P2）：

1. **P1：整批 QA 不阻断发布。** 父 Spec:99 要求检查失败保留草稿、不覆盖 ready 页面。source_wiki.go:298–319 在 batch QA 前创建/覆盖 published+source ready 页；source_wiki_batch_worker.go:246–261、278 后续拒绝只更改 coverage，不撤销公开资格且旧 ready 已可能被覆盖。应保留候选及 exact evidence owner，批检查通过后才按既有 CAS/源/模型/预算发布，失败保留原有效正文。
2. **P1：没有跨分组整体一致性检查。** ticket15:15 要求整批 QA；worker:280 每次仅四张且 repository/source_wiki_batch.go:513 起每组即 ready，不存在覆盖不同组声明的整体检查。分组 QA 可保留，但不能替代预算内的全部主题整体一致性检查；检查未通过不得发布。
3. **P2：批准的 QA 五分钟阶段上限缺失。** coordination 既定合同固定 QA 子阶段五分钟；batch_start.go:35、worker:92 只使用六十分钟 parent deadline。须持久 phase 起点及绝对子 deadline，恢复不重置，取 parent/phase 最小值，有限收尾。
4. **P2：有证据的流程卡没有图。** ticket15:14 要求有证据时生成图并保留引用；attempt.go:237 只请求 prose，source_wiki.go:620 仅渲染正文与引用。加入经验证的静态关系图，明确不确定边并转义不可信 label，图不能替代源码引用或绕 QA。
5. **P2：证据不足 coverage 状态无 producer。** ticket15:13 要求区别展示不足与失败；worker:251 把所有失败 attempt 映射 failed，insufficient_evidence 仅存在 enum/UI。应有可靠类型化原因，不能把权限/模型/预算失败冒充证据不足。
6. **P2：手动模块扩展后 coverage 仍待扩展。** ticket15:7/13 要求后续扩展及实际覆盖。既有手动 GenerateModule 可以生成首批之外的模块，但仅 batch worker 更新 coverage；对应 expansion 行没有 ready 关联。须按同源/主题/快照和授权关联手动成功结果，不修改活动 batch 的 cursor 或预算。

已核实并撤回永久四十页上限的说法：T14 手动模块生成仍可扩展。确定性骨架检查符合允许的实现选择，未发现 producer 可达的无证据主题，故不要求额外 LLM 骨架检查；6 call/120k 是上限而非必须消费。不将这些解释性疑虑列为必修。

## 根独立验证

- 独立 localhost:57821/source_test 实际五项：父子原子预算、deadline 费用保留、21 张卡完整生成与两 runner 同组 QA、停止/checkpoint/父过期恢复、HTTP scope/预检/启动均 PASS，包 61.469s。不是 compile-only 或跳过。
- 根 Temp overlay 实际反例：受控模型 per-card QA 通过、batch QA 返回 false，父 batch failed；通过真实 wiki.GetPageBySlug 仍读到 **两张** published+SourceProvenance.State ready 页面，断言 FAIL 7.49s、包 11.987s。冻结源码未修改；精确文件 `C:/Users/28211/AppData/Local/Temp/weknora-root-t15-90-counter-overlay.json`。失败不是环境或 fixture 错误。
- Docker 专用库已替代 Windows 保留端口 57521，环境入口见协调记录。未修改共享应用/主机网络、未读取旧容器凭据；本轮根所有测试会话均收取。

## 决定及返修边界

未验收，不集成/关闭 #23，不按完成 UI 状态派 T16，累计仍 17/22。六项具体反馈已派原 b0df Luna/xhigh；要求先向根提出候选→整批批准→发布恢复的最小持久接缝，其他收口继续独立实施，正式审查仍 root Sol/high。

为加速，原 T17/2119 对话从 accepted3369 创建 `codex/t15-flow-diagram-assist`，只新增 `source_wiki_flow_diagram.go`/同名测试，实现公开纯 helper `BuildSourceWikiFlowDiagram(relations,evidence)`，返回 Markdown/EvidenceIDs/Uncertain。不得修改主 worker 文件或使用共享 PG。主执行者负责将图接入候选/QA/引用链；最终整票新冻结再审。原 T11 等依赖，不能提前开始 blocked T16。

Standards 硬 0 / 判断 2（P3）；Spec 6（最严重 P1）。
