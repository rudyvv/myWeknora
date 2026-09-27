# WeKnora WeDrive Sync Agent

Windows 用户态常驻 Agent。它复用 `references/deepwiki-open/rpa/wedrive_collect.py` 已验证的 headed Playwright、持久化 Profile、XHR 清单捕获、目录 ID 校验及自动分享思路，但不在运行时依赖 reference 项目。

开发运行：

```powershell
py -m pip install -e .
playwright install chromium
weknora-wedrive-agent --server http://localhost:8080 --register-code <页面生成的一次性注册码> --install-startup
```

构建当前内部使用的 Windows 单文件版本（未签名）：

```powershell
.\build-windows.ps1 -AllowUnsigned -PublishUnsigned
```

脚本使用锁定依赖、Python 3.11 和 PyInstaller，校验源码版本、嵌入 Windows 文件版本并运行 `--version` 启动检查。发布后同时提供 `frontend/public/downloads/WeKnora-WeDrive-Tool-0.4.2.exe` 和兼容旧下载配置的 `WeKnora-WeDrive-Tool.exe`；页面显示 0.4.2 并带版本参数避开旧缓存。构建失败不会覆盖下载包，生成的 EXE 不提交到 Git。

2026-09-27 项目负责人决定内部使用暂不要求代码签名。仅传 `-AllowUnsigned` 时包留在 `dist/`，不会发布到下载目录；内部发布必须额外传 `-PublishUnsigned`。需要签名时使用 `-CertificateThumbprint <证书指纹> -TimestampServer <证书提供方时间戳服务地址>`；证书必须在当前用户或本机证书库中有私钥，签名及时间戳校验通过才会发布。

单文件版本首次双击运行会提示输入页面给出的 WeKnora 服务地址和一次性注册码；注册成功后自动添加当前用户开机启动项。升级时先在页面停止旧工具，再运行新包，既有设备注册和浏览器 Profile 会复用。同步工具优先复用 Windows 自带的 Microsoft Edge，也支持 Google Chrome，不需要另外下载 Playwright Chromium。

部署迁移、回滚限制与验收记录见 [0.4.0 发布说明](../../docs/plans/2026-09-27-wedrive-0.4.0-release.md)。

上面的注册命令会安装当前用户的开机启动项，并继续以前台进程运行；首次调试不需要再启动第二个实例。以后若只想手动启动，执行 `weknora-wedrive-agent`。网页运行在 Vite 的 `http://localhost:5173` 时，Agent 仍应连接 Go 后端的 `http://localhost:8080`。

设备私钥通过当前 Windows 用户的 DPAPI 保存；企业微信 Profile 只位于 `%APPDATA%\WeKnora\wedrive-agent\profile`。Agent 仅上传目录清单和分享链接，不下载正文文件。

选择扫描根目录时，先在 Agent 打开的微盘浏览器里进入目标文件夹，再在 WeKnora“RPA 同步”页面点击“读取 Agent 当前文件夹”。`https://drive.weixin.qq.com/#/webdisk/...` 只是 Agent 与服务端之间使用的内部目录定位信息，普通用户无需手工提供；`https://drive.weixin.qq.com/s?k=...` 是离线文件消费凭据，不能作为扫描根目录。

## Fast Development Mode

扫描尝试协议的最低工具版本为 `0.4.0`，当前内部候选包为 `0.4.2`。领取响应中的 `scan_attempt_id` 与租约仅保存在当前扫描调用中，失败上报必须携带该身份；旧工具会收到 HTTP 426 和 `agent_upgrade_required`。清单绑定、续租及重启恢复已实现。服务端新协议、新工具安装包和页面升级提示须同批交付；真实微盘人工验收状态以发布说明为准。

- `frontend` 页面修改由 Vite 热更新，刷新或重新进入左侧“RPA 同步”即可看到。
- Go API、路由和迁移只有在后端重新编译后生效。安装 Air 时 `make dev-app` 会自动重启；没有 Air 时需 `Ctrl+C` 后重新执行 `make dev-app`。
- 新增表依赖 `AUTO_MIGRATE=true`。若本地关闭了自动迁移，需要先手工执行对应迁移。
- Agent 是独立 Python 进程，不随 Vite 或 Go 热更新；修改 `agents/wedrive-sync` 后需要重启 Agent。
## 日常使用与停止 Agent


1. 在 WeKnora 的“RPA 同步”页确认 Agent 在线，点击“登录企业微信”。
2. 只在 **Agent 自动打开的 Edge 微盘窗口** 中进入需要同步的目标文件夹，再回到 WeKnora 点击“读取 Agent 当前文件夹”。
3. 读取成功后，Agent 会在本机记住该根目录；下次打开企业微信微盘时会优先恢复到该目录。

如果页面提示“共享空间首页”，说明 Agent 当前仍在企业微信微盘的空间列表，并未进入可同步文件夹。请在带 Agent 打开的 Edge 窗口里继续点进 EVIP 目标目录后重试。

## 停止与重新启动

新版 WeKnora 页面提供“停止本机 Agent”按钮：它会关闭本次运行的 Agent 和其受控 Edge 窗口，**不会**删除设备注册、企业微信登录资料或已提交同步源。由于 Agent 会随 Windows 登录自动启动，下次登录 Windows 会再次运行。

如果正在使用旧版同步工具（页面没有停止按钮），请在任务管理器中结束 `WeKnora-WeDrive-Agent.exe` 或 `WeKnora-WeDrive-Tool.exe`。PyInstaller 单文件程序可能出现两个同名进程，这是一个逻辑同步工具的正常父/子进程；结束其任务组即可。
