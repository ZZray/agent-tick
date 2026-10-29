# Tick

Tick 记录本机 agent 的会话进度并发送通知，运行在 Windows 上。Claude Code、Codex、Cursor、Kiro 等 agent 开工时登记一份清单，做完一项勾一项；需要你拍板、卡住或做完时发通知。所有会话显示在一个置顶小窗里。一个对话对应一个会话，子 agent 挂在主会话下面。

<p align="center"><img src="docs/tour.gif" width="700" alt="悬浮窗：展开刚结束、拉开通知抽屉、收成胶囊后悬停出现会话转盘"></p>

整个程序是一个 `tick.exe`（Go，约 11 MB，不依赖 cgo；本机 amd64 构建 10.8 MB），包含：

- HTTP API、SSE 推送和 MCP 服务（`/mcp`），只监听 127.0.0.1；
- 给只能跑 shell 的 agent 用的命令行子命令；
- Windows 托盘、原生置顶悬浮窗和完整面板（WebView2）、系统通知、提示音；
- 安装到各个 agent（`tick install`）和自动更新。

## 快速开始

1. 从 [Releases](https://github.com/ZZray/agent-tick/releases/latest) 下载 `tick-windows-amd64.exe`（ARM 设备选 `arm64`）。
2. 在下载目录运行：

   ```powershell
   .\tick-windows-amd64.exe install --dry-run   # 先看会改哪些文件
   .\tick-windows-amd64.exe install             # 安装并启动
   ```

3. 重开 agent，在对话里说“用 tick 记录进度”，或者直接 `@tick`。

需要 Microsoft Edge WebView2 运行时，Windows 10/11 一般自带。

## 悬浮窗

- **会话列表：** 正在跑的排最上面，然后是等你和受阻的，再是还在写清单的，30 分钟没动静的放最后。同一档默认最新登记的在前，组头的“最新在前 / 最早在前”可以倒过来，面板里也有同一个开关。每行显示当前步骤、进度条和耗时，点开看完整清单。
- **刚结束：** 默认收成一行，后面带几个标题做预览，点组头展开。窗口收起、贴边或收成胶囊时会自动再收回去。“清掉”把这些会话归档，6 秒内可以撤销。
- **通知抽屉：** 标题栏的铃铛显示未读数，有人等你时变成橙色。点它，抽屉从朝屏幕中间的一侧拉开：窗口靠右时往左长，右边缘不动。
  - 最上面是“等你 / 受阻”卡片，可以回到对话、复制接力提示、展开清单，或标为知道了；
  - 下面是通知列表，分“未读”和“全部”。点一行标为已读，并在左边的列表里定位那个会话；
  - 底部选提示音。
  - 新的“等你 / 受阻”到达时抽屉自动拉开。
- **底栏：** 右边显示最近一条动态，比如“勾选 #3 补单测”和时间，点一下定位到那个会话。
- **胶囊和会话转盘：** 横线按钮或双击标题栏把悬浮窗收成一行，只显示最要紧的一件事：先是等你，然后是正在做的那一步。鼠标在胶囊上停 0.16 秒，会话沿半圆排开，贴着屏幕边那一侧。鼠标停在哪个气泡上，哪个就放大，两边的邻居跟着放大一点；左边换成它的标题、当前一步和进度。点气泡展开悬浮窗并打开那个会话。鼠标离开后气泡收回圆心，窗口缩回胶囊。
- **子 agent：** 子 agent 登记时带上主会话（`--parent` / `parent_session_id`），就显示在主会话下面，一个一行，点开看它自己的清单。
- **贴边：** 拖到屏幕左、右、上边缘附近松手，悬浮窗吸过去贴齐。鼠标离开 0.65 秒后缩成 30px 宽的小标签，里面是每个活跃会话的进度环，最多 4 个。环的弧长是完成比例；等你是橙色，标签贴边的那条边会亮一道橙线；卡住是红色；做完是绿色对勾。鼠标停在环上会放大并提示标题和当前步骤，点环直接打开那个会话。收着的时候来了消息，标签会长成一个气泡，显示谁、什么事。
- **快捷键：** `Ctrl+Alt+T` 叫出或收起悬浮窗。被别的程序占用时不注册。
- **勿扰 1 小时：** 托盘菜单开启。期间不弹系统通知、不响提示音、不出气泡，悬浮窗照常记录。
- **全屏时让开：** 前台是全屏游戏、视频或演示时自动隐藏，退出全屏后回来。
- **托盘：** 左键显示或收起，右键菜单里有置顶、贴边后自动隐藏、全屏时让开、勿扰、通知声音、打开面板、复制 MCP 地址、开机启动、检查更新和自动更新。有人在等你时托盘图标右上角出现橙点。

## 完整面板

悬浮窗标题栏的面板按钮、会话里的“详情”、托盘“打开面板”，或者命令 `tick panel [会话]`，都会打开一个本地窗口。窗口无边框，标题栏由页面自己画：拖动、双击最大化，右上角有最小化、最大化和关闭；系统阴影、圆角、边缘缩放和贴靠照常可用。窗口会记住上次的大小和位置。

<p align="center"><img src="docs/panel.png" width="900" alt="完整面板：左侧视图和 agent 筛选，中间会话列表，右侧会话详情"></p>

在面板里可以：

- 搜索标题、条目、备注、总结、目录和 agent，按 `/` 聚焦，已归档的也搜；
- 看历史：按天分组，组头写当天勾了几项、干活多久；
- 看“没动静”：说在跑、但 30 分钟没更新的会话单独放一组；
- 编辑：双击改标题和条目，点圆圈手动勾选或取消，底部“添加一项”，删除条目可以撤销，删除会话要点两次。

托盘里的“在浏览器里打开”会用浏览器打开同一个面板（`http://127.0.0.1:7357/`）。

## 提示音

默认不响。在通知抽屉底部或托盘“通知声音”里选一种，选中时会试听一次。内置 8 种：清脆、清脆 · 单声、清脆 · 两下、轻铃、轻铃 · 三音、柔和、柔和 · 小三度、柔和 · 下行。

想用自己的声音，把 `.wav` 文件放进 `%APPDATA%\Tick\sounds\`（抽屉里的文件夹按钮能直接打开）。抽屉里的列表会马上出现新文件，托盘菜单要重开 Tick 才会更新。

Windows 系统通知本身设为静音，声音只由 Tick 播放，不会响两遍。

## 安装与更新

```powershell
tick.exe install --dry-run   # 列出要做的事，不改文件
tick.exe install             # 复制程序、给本机已装的 agent 写 skill 和 MCP 配置，然后启动 Tick
tick.exe uninstall           # 撤掉 skill 和 MCP 配置（会话数据不删）
tick.exe update              # 检查并更新
tick.exe version             # 当前版本和更新源
```

`install` 不需要任何配置：

- 程序复制到 `%LOCALAPPDATA%\Programs\Tick\tick.exe`，所有配置都指向这里。旧版正在运行、文件被占用时沿用旧文件，提示你退出后再装或用 `tick update`。
- 只给本机装了的 agent 配，依据是配置目录是否存在：Claude Code、Codex、Kiro、Cursor、Gemini CLI，以及通用的 `~/.agents`。
- 每个 agent 得到两样东西：
  - **skill**（`skills/tick/SKILL.md`）：写明怎么登记清单、勾选、通知和派子 agent；
  - **MCP**：stdio 方式（`tick.exe mcp`），服务没开时自动拉起。Claude Code 通过 `claude mcp add --scope user` 添加，其余直接改配置文件。
- 改已有配置前先备份成 `*.bak-tick-时间`；已经配过 tick 的跳过；不是标准 JSON 的文件（比如带注释）不改，会提示你手动加。

自动更新：

- 更新源是本仓库的 GitHub Releases，校验 SHA256 通过才替换。
- 启动 1 分钟后检查一次，之后每 12 小时检查一次。托盘里可以手动检查，也可以关掉自动更新。
- 新版下载好后，等到没有会话在跑时才重启。
- 自己编译、没有设置更新源的版本不会自动更新。

## 接入 agent（手动）

用了 `tick install` 就不用手动配。想自己配，面板里的“接入 agent”可以复制下面这些配置，里面是本机的实际地址：

```text
Claude Code：  claude mcp add --transport http tick http://127.0.0.1:7357/mcp
Cursor / Kiro：{"mcpServers": {"tick": {"url": "http://127.0.0.1:7357/mcp"}}}
Codex：        [mcp_servers.tick]
               url = "http://127.0.0.1:7357/mcp"
只支持 stdio： {"mcpServers": {"tick": {"command": "C:\\path\\to\\tick.exe", "args": ["mcp"]}}}
```

MCP 初始化时会把用法交给 agent：开工写清单、逐条勾选、需要你时通知、做完写总结。agent 名称从客户端信息自动识别。完整约定见 [AGENT_PROTOCOL.md](AGENT_PROTOCOL.md)，服务启动后也能从 `/protocol` 读取，其中有不用 MCP 时的 HTTP 接口。

## 命令行

没接 MCP、只能跑 shell 的 agent 用同一个 `tick.exe`。中文参数直接传，不经过 JSON，所以不会遇到 PowerShell 5.1 的编码乱码：

```powershell
$sid = (& tick.exe start "修复重连后状态不刷新" "复现" "修复" "编译验证" --agent codex --ext <对话ID>)[0]
tick.exe doing $sid 1
tick.exe done $sid 1 --note "本机复现，日志键 session.refresh"
tick.exe wait $sid --note "要推送到 dev1 吗？"
tick.exe finish $sid --summary "改了 2 个文件，单测 3/3，未上真机"
# 派子 agent 时把 $sid 交给它，它登记时加 --parent $sid
```

`start` 输出的第一行是会话 ID；其他写操作只输出一行（会话、状态、进度），要看完整清单用 `tick show`。全部子命令见 `tick.exe help`。退出码：0 成功，1 用法或接口错误，2 连不上 Tick。

## 计时口径

- **条目耗时：** 从 doing 到 done 的累计时间；会话处于等你、受阻或已结束时暂停。
- **会话总耗时：** 从登记到结束。结束后同一对话又来了新任务、会话被重新打开时，中间空着的时间不算。
- **等你：** 处于 waiting / blocked 的累计时间。
- **干活：** 总耗时减去等你。

## 安全

- 只绑定 127.0.0.1，并校验 `Host` 头，防 DNS rebinding。
- 写请求必须是 `application/json`；带 `Origin` 时必须是本机地址，防别的网页跨站提交。
- **没有鉴权**：本机任何进程都能读写会话和发通知。不要在清单和通知里写密钥。
- 链接只接受 `scheme://`，拒绝 `javascript:`、`data:`、`file:` 等；打开链接时宿主侧会再查一遍。
- 首次启动会在 `HKCU\Software\Classes` 下写入 Windows 通知需要的应用注册。勾选“开机启动”会写 `HKCU\...\Run\Tick`。

## 数据与参数

| 参数 / 环境变量 | 默认值 | 作用 |
|---|---|---|
| `-port` / `TICK_PORT` | 7357 | 端口（命令行子命令和 `tick mcp` 只认 `TICK_PORT`） |
| `-data` / `TICK_DATA_DIR` | `%APPDATA%\Tick` | 数据目录：会话 `sessions.json`、窗口设置 `desktop.json`、日志 `tick.log`、自定义提示音 `sounds\` |
| `-headless` | 关 | 只开服务，不开托盘和窗口，在浏览器里看 |

再次运行 `tick.exe` 不会开第二份，只会把已有的悬浮窗叫出来。

## 开发

```powershell
$env:CGO_ENABLED = '0'
go build -ldflags "-H windowsgui -s -w" -o tick.exe .
go vet ./...
go test ./...
```

测试覆盖会话新建与重新关联、计时（等待暂停、重开不计空档）、重发清单保留状态、通知生成与自动已读、agent 主动通知、删除、子会话、输入校验、Host / Origin / Content-Type 拦截、SSE 推送、持久化、MCP 全流程、安装时的配置合并和版本比较。托盘、悬浮窗、系统通知和提示音没有自动化测试，靠实机运行检查。

推送 `vX.Y.Z` 标签会触发 `.github/workflows/release.yml`：测试、编译 amd64 和 arm64、生成 `SHA256SUMS` 并发布到 Releases。
