# Tick 协议：多步任务的清单、进度与通知

Tick 是用户本机的任务看板和通知窗口（{{BASE}}）。用户靠它看每个 agent 会话的清单、进度和耗时，也靠它接收你的通知。

三种接入方式，按顺序选：

1. **MCP**：服务地址 `{{BASE}}/mcp`。工具名是 `tick_start`、`tick_rename`、`tick_item`、`tick_plan`、`tick_notify`、`tick_finish`、`tick_show`，用法见各工具说明。
2. **命令行**：只能跑 shell 时用 `tick.exe`（见下文“命令行”）。PowerShell 5.1 下也不会出现中文乱码。
3. **HTTP**：见文末接口表。

## 何时登记

任务需要 2 步以上，或预计超过 5 分钟，就登记。一问一答和单点小改不用登记。

## 会话关联

- 一个对话对应一个 Tick 会话。开工前调一次 `tick_start`；能拿到对话 ID 就填 `conversation_id`。
- 记住返回的 `session_id`，本对话后续调用都带上它。在给用户的第一条进度回复里写一次，上下文被压缩后还能从对话里找回。
- 同一对话里来了新任务：继续用这个会话，`tick_plan` 用 append 追加条目，不另开会话。
- 当前目标明显变化时，用 `tick_rename` 更新标题，保留容易辨认的主题，例如“Tick · 展开与标题优化”。普通步骤和追问继续显示在当前步骤行，不反复改名。标题尽量简短，最多 200 字；旧标题会保留供搜索，清单、状态和对话关联不变。
- 不要省略或伪造 `conversation_id` 来绕过去重。重新 `tick_start` 只会拿回原会话，不会更新标题。
- 上下文丢了：用相同的 `conversation_id` 再调一次 `tick_start`，会拿回原会话。拿不到对话 ID 时，每次 `tick_start` 都会新建，所以只调一次。
- `link` 可选，填能回到这个对话的链接，例如 `codex://threads/<id>`。用户点通知时能直接跳回来。

## 清单怎么写

- 先列清单再动手。一般 3 到 12 条，按执行顺序排。
- 每条写一个能检查的结果，动作加对象，必要时带完成标准。例如“重连成功后补发一次会话刷新（含单测）”，不写“分析一下”。
- 验证单独成条（编译、测试、自测）。
- 计划变了就改清单：`tick_plan` 重发完整清单（已有条目文字不变，状态和耗时会保留），或用 append 追加。清单要和实际在做的事一致。

## 执行与勾选

- 开始某条前 `tick_item status=doing`，做完马上 `status=done`，`note` 写一句结果或证据。耗时由 Tick 按 doing 到 done 计算，你不用报时间。
- 不要攒到最后一起勾，用户靠实时勾选看进度。
- note 写能核对的结果：改了哪些文件、几条测试通过、产物在哪。没验证就写没验证。
- 决定不做的条目用 `status=skipped`，note 写原因，不要直接从清单里删掉。

## 通知用户

- 需要用户决定或操作：`tick_notify kind=waiting`，message 写成用户一眼能答的问题。屏幕右下角会弹提醒卡，悬浮窗的通知抽屉里也会有卡片。用户回复后直接 doing 下一条即可，会话会自动恢复。
- 卡在环境、权限或依赖上：`kind=blocked`，写清卡在哪、试过什么。
- 其他值得用户知道的事：`kind=info`、`success` 或 `warning`。只发真正有用的，不要每步都发。
- 结束：`tick_finish`，summary 写改动、验证结果和没覆盖的部分；没做成加 `failed=true`。用户会收到通知。
- **结束本轮回复前，会话状态必须和你一致。** 你停下来以后，Tick 上的“进行中”就不再是真的。结束回复前三选一：还有活就别结束回复，进度汇报写在工具调用之间；要用户回答就 `kind=waiting`（正在做的条目暂停计时）；做完了就 `tick_finish`。
- 清单、备注、通知和总结里不写密钥、令牌、密码。
- Tick 连不上不影响主任务。在回复里提一句，不要反复重试。

## 子 agent

你派出子 agent 时，把你的 session_id 交给它，让它登记时挂到你的会话下：MCP 用 `parent_session_id`，命令行用 `--parent`。这样用户会在你的会话下面看到它的进度，不会和别的会话混在一起。只支持一层：子 agent 不能再挂孙任务，它自己再派出去的 agent 也挂到你的会话下。

## 命令行

`tick.exe` 和服务是同一个程序。服务没开时，命令会先在后台把它拉起来。

```text
tick start "标题" "第1步" "第2步" --agent <名称> [--ext <对话ID>] [--parent <主会话ID>] [--link <URL>] [--goal <完成标准>]
tick doing|done|skip|todo <会话> <序号> [--note "结果或原因"]
tick plan <会话> "条目"...        重发完整清单（文字相同的条目保留状态）
tick add <会话> "条目"...         追加
tick rename <会话> "稳定主题 · 当前目标"     更新标题，保留旧名供搜索
tick wait <会话> --note "要用户决定的问题"     会通知用户
tick blocked <会话> --note "卡在哪"           会通知用户
tick resume <会话>
tick finish <会话> --summary "改动、验证、没覆盖的部分" [--failed]
tick notify <会话> "文字" [--kind info|success|warning]
tick show <会话>   |   tick ls   |   tick help
```

- `start` 输出的第一行是会话 ID。`--agent` 可以用环境变量 `TICK_AGENT` 代替；工作目录自动记为当前目录。
- `tick.exe` 是窗口程序。在 PowerShell 里存输出要走管道：`$sid = tick.exe start … | Select-Object -First 1`；直接赋值或 `(tick.exe …)[0]` 拿到的是空值。
- 其他写操作只输出一行：会话 ID、标题、状态和进度（如 `清单 12/15`）。要看完整清单用 `tick show`。
- 参数可以写成 `--note "…"`，也可以写成 `--note=…`。单独一个 `--` 之后的内容全部当正文。
- 退出码：0 成功；1 用法或接口错误，错误原因写在 stderr；2 连不上 Tick，这时不影响主任务，提一句就行。
- 端口用环境变量 `TICK_PORT` 指定，默认 7357。

## HTTP 接口

JSON，UTF-8，只接受本机访问。写请求要带 `Content-Type: application/json`。

| 方法 | 路径 | 请求体 |
|---|---|---|
| POST | `/api/sessions` | `{agent, title, externalId?, parentId?, link?, goal?, workspace?, items?: string[]}`；同 agent + externalId 返回原会话；parentId 挂到主会话下 |
| PATCH | `/api/sessions/:id` | `{status?, note?, summary?, title?, goal?, workspace?, link?, archived?}`；改名时旧标题存入只读 `titleHistory`，同名不重复记录。status 取 planning / running / waiting / blocked / done / failed / cancelled。要发“等你 / 受阻”，就 `status=waiting|blocked` 并带上 `note`，note 就是给用户的问题，会生成通知 |
| PUT | `/api/sessions/:id/items` | `{items}`，重发完整清单；条目可以是字符串或 `{text, status?, note?}` |
| POST | `/api/sessions/:id/items` | `{items}` 或 `{text}`，追加；会话已结束时会重新打开 |
| PATCH | `/api/sessions/:id/items/:n` | `{status?, note?, text?}`；`:n` 是从 1 开始的序号或条目 ID；status 取 todo / doing / done / skipped / blocked |
| POST | `/api/sessions/:id/log` | `{text}`，记一条动态（不通知） |
| POST | `/api/notices` | `{sessionId, kind?, text}`，或不挂会话时 `{agent, title?, kind?, text}`；kind 取 info / success / warning（等你、受阻、做完由状态变化自动生成） |
| POST | `/api/notices/read` | `{ids: string[]}`、`{sessionId}` 或 `{all: true}`，标为已读 |
| GET | `/api/sessions/:id`、`/api/sessions[?archived=1]`、`/api/notices` | 查看 |
| GET | `/api/health`、`/api/stream`（SSE 推送 session / notice 事件） | 状态与实时推送 |
