# T17 09179ca1 验收审查

完整干净冻结 `09179ca182400a83f8815b5a05110740893dff8e`；用户已批准的固定起点 `f040e5e838cfc1d755d332780882758f65610369`。两位独立 Sol/high 审查者复核全票及本次五文件返修，已验收 T18 不重复审查。

## Standards

文档化硬性违例 0。FailRecoveryTarget 锁定精确 attempt ID、tenant、KB、source、epoch 和 running 状态，核对未变化的 owner、lease expiry、deadline；未到期的有效租约不能被抢占。终止、保留未知调用消费与释放 exact evidence owner 在一个事务完成。恢复任务授权仅在持久 KB/source 绑定核实后生成；启动 Invoke 已置于全部 Provide 之后。

既有两项判断性 P3 Duplicated Code 未变：首次 QA 与辅助校验重复；新 attempt 的模型选择、slug 派生与既有 helper 重复。非阻断，不扩大返修范围。

## Spec

确定性偏差 0。上一轮 P1 注册未完成便启动恢复器已修复；P2 过期/失去绑定的前八条候选永久占队列且保留 owner 已修复。过期收尾先于 KB/source 加载，失去绑定可以安全终止；知识库不存在与临时数据库错误分别处理。持久预算、同 ID/epoch 恢复、page CAS、模型与发布目标保护仍保留。

## 根独立验证

- 冻结树真实构造链与 bootstrap 注册顺序测试通过。根 overlay 原反例：八条过期失效绑定并持有 exact owner，随后一条合法 due attempt，两轮有界扫描后旧行 failed、owner 释放、合法行 ready 且实际两次受控 provider 调用，PASS 5.87s。
- 同轮真实 PostgreSQL：未返回 ID 前崩溃恢复、活租约后同 ID QA 恢复、过期草稿/owner 收尾、未知 context 零 provider 全部通过；七项合计包 34.066s。使用独立 localhost:57521/source_test，源码和测试凭据不发布。
- 第一次尝试因 Docker 引擎停止、57521 拒绝连接而失败，只计环境失败。用户手动启动后根仅启动专用测试容器，再实际运行以上用例，没有修改/restart 共享应用或读取旧容器凭据。
- 无冲突合入 `e6ea43dcd07bae36899468ec756cea3f03be7598` 后六项实际验证全部通过，包 33.884s：构造链/注册顺序、异租户绑定终止、失效前八行恢复队列、T11 固定发布与授权业务链、T18 修订 pin/旧索引 GC。access/types 分别 PASS 4.912s/6.197s；diff --check 通过。
- 不宣称完整生产 BuildContainer 已运行：原生 SQLite/DuckDB 宽编译限制仍在，真实服务构造函数图和源注册顺序检查用于本次针对性验证。未重复宽套件或安装无关原生依赖。

## 决定

T17 通过，允许推送集成与关闭 #25；累计 17/22。T15 的 `ef34c7a2`、`6c080201` 是干净部分实现，整批生成/共同预算仍未接入，不能算验收。发布后在原 T06 对话派发准确 accepted SHA 续做 T15；原 T11 等 T15 解锁 T16，原 T17 等后续依赖，不提前进入 blocked 票。

Standards 硬 0 / 判断 2（P3）；Spec 0。
