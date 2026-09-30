# T09 b8a2b071 双轴复审与独立验证

冻结 b8a2b07101d13a803c35726ef51ccd268e9f1427，用户批准基线 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。完整差异 34 文件、9 提交；工作树干净，`git diff --check` 通过。两位 GPT-6 Sol/high 独立审 Standards 与 Spec。本轮未通过；#17 保持 open，不集成或派下一票。

## Standards

发现两项 P2 实现质量问题，以及一项 P3 判断建议。

- P2，`sourceparser/runtime.py:104-113,220` 对每个 Unicode 字符在 Python dict 建 UTF-16→UTF-8 映射。Root 以 1 MiB ASCII 等量键值只读探针得到 1,048,577 条、dict 自身 41,943,128 字节，键值样本各 28 字节；仅映射约 100 MiB/输入 MiB。合法 8 MiB SFC 加 Node/JSON 很容易超过 `docker-compose.source.yml:21` 的 768m 限额。应按实际 descriptor/诊断边界稀疏转换或使用紧凑映射，并保留 Unicode/CRLF/emoji 校验。
- P2，`runtime.py:269-274,354-380` 对普通空块 `<template>` 的 opening wrapper 产生 `region:null` 块；warning 回退挂到此块仍不补 region。审查员直接复现 degraded+diagnostic 但 region:null，模型证据无 template 区域。应给可验证的包装块标注所属区域，保留警告真实原文范围。
- P3，possible Duplicated Code：`SourceCodeView.vue:16` 与 `SourceRegionBadge.vue:5` 各定义质量文案。根集成会保留 T09 完整共享 helper，不要求在此轮另改公共 UI。

## Spec

部分实现 1（P2）；旧 c9 的正文 warning 归属和外置脚本软删除可读性两项已核对修复。

- `sourceparser/sfc/parse_sfc.cjs:17-23` 只收集 Vue descriptor 保留的块。重复顶层 `<script>` 时 Vue 2.7.16 只保留后一个，`runtime.py:245-326` 把先前脚本正文作为无区域的 structural gap，违反 T09 ticket 的准确区域、所有块原文件坐标及质量。Root 使用真实 Node `parseSFC` 验证双 script：只返回后块 body [38,52]，duplicate warning 在 byte30。应保留被丢弃块的正宽原文区域证据并明确降级，不能猜测其结构符号。

## Root 复验与处置

Root 在冻结树独立运行官方 Node SFC 单测 7/7 PASS、`go test -p 1 ./internal/source ./internal/application/repository` 两包 PASS（source3.980s、repository3.850s）。Root 无法在本轮运行锁定 Vue HTTP 和真实 PostgreSQL：Docker daemon 与独立 localhost:57521 当前不可用，现有 Python 环境缺锁定 Tree-sitter pack；worker 报告该提交的锁定 HTTP/PG 也未跑，不能沿用旧 SHA 的通过结果。三项必修已直接交回原 T09 Luna/xhigh 对话，等新 clean SHA 和可用依赖后复审复验。父任务仍 8/22。
