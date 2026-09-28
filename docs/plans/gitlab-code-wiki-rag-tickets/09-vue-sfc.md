# T09：[CodeWiki] Vue SFC 区域检索与整文件坐标

已发布：[Issue #17](https://github.com/rudyvv/myWeknora/issues/17)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

用户检索 Vue 2 页面中的 API 调用、模板或样式时，命中准确指向原 .vue 文件中的对应区域。

## Acceptance criteria

- [ ] 固定版本官方 Node SFC 组件只解析区域，不编译项目或加载目标插件；script 按声明语言走结构解析。
- [ ] template/script/style/custom block 坐标映射到原整文件，Node UTF-16、UTF-8、中文/emoji、CRLF 等测试逐切片核验。
- [ ] 外置 script 只关联仓库内获允许文件，不读任意服务器路径；不能读取的目标或未知预处理明确说明。
- [ ] 区域块经实际双索引及代码阅读可返回质量、符号/区域和原始位置，不用拼接区域伪造一个连续区间。
- [ ] 代表仓库 Vue 2 组件链及独立 lang=ts 语料都可验证，已有 JS/TS 直接文件仍正确。

## Blocked by

- [Issue #15 — JavaScript 与 TypeScript 结构检索和原始位置引用](https://github.com/rudyvv/myWeknora/issues/15)
