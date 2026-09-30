# T09 0a595184 双轴复审

完整干净冻结 `0a59518479bcab1edbfe80458f526d1dfc6723cd`，用户批准固定起点 `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d`。两位独立 Sol/high 审查完整差异；旧 b8a 的重复块正文、空块区域与逐字符 UTF-16 内存问题原反例已修。此轮未通过；#17 保持 open，不集成。

## Standards

两项 ADR-0008 硬违反：

- `sourceparser/runtime.py:244-252` 把外部 `NODE_ENV` 传给 Node SFC 解析进程。官方 compiler-sfc 在 production 关闭部分 descriptor 告警；同一不完整模板在 development 有原文范围诊断，在 production 无诊断，违反 ADR-0008 对告警来源证据的要求。解析语义须固定且不受部署环境改变。
- `sourceparser/runtime.py:270-274` 对官方 descriptor 丢弃后重新枚举的重复块强制 `lang=''`。先出现的 `<template lang="pug">` 被标 `degraded`，而 ADR-0008 要求按块类型及声明语言标 `unknown_preprocess`，保留原文。

判断性 P3 possible Duplicated Code：`SourceCodeView.vue:16` 与 `SourceRegionBadge.vue:5` 重复质量文案，可在 T09/T10 集成共享 helper 时统一。逐字符偏移映射已改为紧凑 checkpoint。

## Spec

两项 P2 部分实现：

- `sourceparser/sfc/parse_sfc.cjs:26-29` 把官方 Vue 2.7.16 自闭合顶层块的 `end=0` 当作非法，合法 `<template/>`、`<script src="./api.js"/>` 使整个文件 `invalid compiler range`，丢失区域和外置引用证据。须保留零宽 body 与原包装范围，并测试所有相应块类。
- `internal/application/repository/source_sfc.go:72-85` 在归一化前拒绝任何 `..` 段，导致 `src/pages/Panel.vue` 的同仓库内引用 `../shared/api.js` 无法解析到被选中、获授权的 `src/shared/api.js`。应规范化后检查不越仓库边界，再用现有快照成员与授权条件核验。

两轴统计：Standards 硬2、判断1，最严重 P2；Spec 2，最严重 P2。四项必修已派回原 T09 Luna/xhigh；等待新完整干净 SHA。

## Root 独立验证

锁定捆绑 Node 24.19.0 官方 SFC 单测 8/8 PASS，`go test -p 1 ./internal/source ./internal/application/repository -count=1` 两包 PASS。Root 用相同锁定 Node 直接复现自闭合 `<template/>` 与外置 script 均抛 `invalid compiler range`。Worker 本轮直接适配器检查、隔离 PG Java 快照验证通过，但锁定 Vue HTTP 回归与 Python PG 未运行；它们不算此 SHA 验收通过。Go/UI 之外尚需在修复后补实际 Vue HTTP/PG 证据。
