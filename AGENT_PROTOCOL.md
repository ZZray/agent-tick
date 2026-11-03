# Tick 协议：多步任务的清单、进度与通知

Tick 是用户本机的任务看板和通知窗口（{{BASE}}）。用户靠它看每个 agent 会话的清单、进度和耗时，也靠它接收你的通知。

三种接入方式，按顺序选：

1. **MCP**：服务地址 `{{BASE}}/mcp`。工具名是 `tick_start`、`tick_rename`、`tick_item`、`tick_plan`、`tick_notify`、`tick_pause`、`tick_finish`、`tick_show`，用法见各工具说明。
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
- 多个流程或职责必须使用 `# 模块名` 或 `# 父流程 / 子流程` 分段，让无限画布能显示“大模块 → 子模块 → 当前步骤”。按实际工作单元划分，通常一至三层足够，简单短任务可不分组，不要每条步骤创建一个模块。大屏跟随 doing 显示当前路径，不需要为模块另建会话；模块名简短稳定，步骤写清对象、动作和可检查的结果。
- 业务模块不是子 Agent：真实独立委派才建子会话，不为展示层级或绕过条目上限造子会话。模块从条目派生汇总，不再追加“模块汇总任务”。API 以方法为验收单位，复测沿用同一条目，多次 Run 的证据入口放 evidence，不增加方法总数。
- 例如 `# 首轮验证 / 数据处理 / 二维处理` 下登记“调用二维傅里叶变换，核对输出矩阵尺寸”，开始执行时将该条标 doing。开工前列出已知模块范围，未来项保持 todo，已完成项及时收尾，模块与总进度从条目汇总。追加任务时带上模块标题，重发时保留仍适用的分组和步骤文字；标题行不占条目序号，不标 doing/done。
- 计划变了就改清单：`tick_plan` 重发完整清单（已有条目文字不变，状态和耗时会保留），或用 append 追加。清单要和实际在做的事一致。

## 执行与勾选

- 开始某条前 `tick_item status=doing`，做完马上 `status=done`，`note` 写一句结果或证据。耗时由 Tick 按 doing 到 done 计算，你不用报时间。
- 不要攒到最后一起勾，用户靠实时勾选看进度。
- 只有实际并行执行的步骤才同时 doing，避免大屏把已经停下的分支显示为当前流程。
- 切换模块前核对旧的 doing：已结束的补 done，依赖受阻的标 blocked 并写恢复条件。长步骤用 note 更新真实阶段与证据入口，不能只更新会话标题或编造百分比。
- note 写能核对的阶段或结果：正在验证哪个对象、改了哪些文件、几条测试通过、产物在哪。没验证就写没验证。
- done 只表示执行结束。验证结果用 result=passed/failed/unknown/unverified 显式记录，evidence 写实际日志/Run 链接；skipped 单列，不算通过。旧 done 没有结果时不能追认 passed。修改计划可给现有条目 id，改名和移组保持耗时、结果与证据历史；单清单最多 1000 项。
- 决定不做的条目用 `status=skipped`，note 写原因，不要直接从清单里删掉。
- 请求返回遗漏提醒时，先核对稳定条目的真实执行证据，再确认 doing、标依赖 blocked、补 done/result 或说明取消原因 skipped。提醒只要求核对，不要求把未知结果写成成功，也不自动构成用户问题。

## 通知用户

- 需要用户决定或操作：`tick_notify kind=waiting`，message 写成用户一眼能答的问题。屏幕右下角会弹提醒卡，悬浮窗的通知抽屉里也会有卡片。用户回复后直接 doing 下一条即可，会话会自动恢复。
- 卡在环境、权限或依赖上：`kind=blocked`，写清卡在哪、试过什么。
- 其他值得用户知道的事：`kind=info`、`success` 或 `warning`。只发真正有用的，不要每步都发。
- 结束：`tick_finish`，summary 写改动、验证结果和没覆盖的部分；没做成加 `failed=true`。用户会收到通知。
- **结束回复前，会话状态必须准确。** 完成才 tick_finish；waiting 必须有用户能回答的具体问题；等待依赖用 blocked；待续用 tick_pause 写恢复条件；执行器确实仍在后台运行时保留 running 并给真实执行证据。失败不自动阻断整个目标，可继续的分支继续做，不为结束回复制造“等你”。等待/受阻都可暂停执行计时，历史 WaitMs/WaitSince 保留原等待及依赖耗时口径，“等你”人数只统计 waiting。
- 清单、备注、通知和总结里不写密钥、令牌、密码。
- Tick 连不上不影响主任务。在回复里提一句，不要反复重试。

## 子 agent

你派出子 agent 时，把你的 session_id 交给它，让它登记时挂到你的会话下：MCP 用 `parent_session_id`，命令行用 `--parent`。这样用户会在你的会话下面看到它的进度，不会和别的会话混在一起。只支持一层：子 agent 不能再挂孙任务，它自己再派出去的 agent 也挂到你的会话下。

主 agent 收尾前，核对本次已经返回的子 agent 已 `tick_finish`，取消的工作说明原因；仍在执行的保留真实状态。归档由用户管理历史，不替代 Agent 结束自己的会话，也不能把未验证的工作标成成功。

## 命令行

`tick.exe` 和服务是同一个程序。服务没开时，命令会先在后台把它拉起来。

```text
tick start "标题" "第1步" "第2步" --agent <名称> [--ext <对话ID>] [--parent <主会话ID>] [--link <URL>] [--goal <完成标准>]
tick doing|done|skip|todo <会话> <序号> [--note "结果或原因"]
tick plan <会话> "条目"...        重发完整清单（文字相同的条目保留状态）
tick add <会话> "条目"...         追加
tick rename <会话> "稳定主题 · 当前目标"     更新标题，保留旧名供搜索
tick pause <会话> --note "待续原因和恢复条件"
tick done <会话> <条目ID> --result failed --evidence "日志或Run链接" --note "错误摘要"
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
| POST | `/api/sessions` | `{agent, title, externalId?, parentId?, link?, goal?, workspace?, items?}`；items 是字符串或 `{text, group?, status?, note?, result?, evidence?}` 对象数组。同 agent + externalId 返回原会话；parentId 挂到主会话下 |
| PATCH | `/api/sessions/:id` | `{status?, note?, summary?, title?, goal?, workspace?, link?, archived?}`；改名时旧标题存入只读 `titleHistory`。status 取 planning / running / waiting / blocked / paused / done / failed / cancelled。waiting 的 note 是具体用户问题；blocked 的 note 是依赖、影响与恢复条件；paused 表示待续，均不冒充完成 |
| POST | `/api/sessions/:id/archive` | 用户整组归档/恢复：`{archived: true|false}`，包含全部后代；返回 `{sessions, changedIds}`，不改变完成状态。Agent 应正常 finish 自己的任务，不能用归档代替收尾 |
| PUT | `/api/sessions/:id/items` | `{items}`，重发完整清单；条目为字符串或 `{id?, text, group?, status?, note?, result?, evidence?}`。现有 id 可改名/移组并保留历史，未知/重复 id 拒绝；新增条目省略 id |
| POST | `/api/sessions/:id/items` | `{items}` 或 `{text}`，追加；会话已结束时会重新打开 |
| PATCH | `/api/sessions/:id/items/:n` | `{status?, note?, text?, result?, evidence?}`；`:n` 是序号或稳定条目 ID；status 取 todo / doing / done / skipped / blocked，result 取 passed / failed / unknown / unverified。只改结果不会恢复执行或计时；resultHistory 只读保存显式结果记录，不等同完整 Run 台账 |
| POST | `/api/sessions/:id/log` | `{text}`，记一条动态（不通知） |
| POST | `/api/notices` | `{sessionId, kind?, text}`，或不挂会话时 `{agent, title?, kind?, text}`；kind 取 info / success / warning（等你、受阻、做完由状态变化自动生成） |
| POST | `/api/notices/read` | `{ids: string[]}`、`{sessionId}` 或 `{all: true}`，标为已读 |
| GET | `/api/sessions/:id`、`/api/sessions[?archived=1]`、`/api/notices` | 查看 |
| GET | `/api/health`、`/api/stream`（SSE 推送 session / sessions / notice 事件） | 状态与实时推送；sessions 是批量会话快照数组 |

`paused` 暂停正在执行的步骤计时，会话总历时仍按墙钟计算。`resultHistory` 为只读结果记录数组，每项包含 `{at, result, evidence?, note?}`：只有显式上报 result 且 `(result, evidence, note)` 与最后记录不同才追加，相同重放不重复记录；只改备注或证据不追加。历史不静默截断。该字段不统计调用次数，也不代替完整 Run 台账。

## Agent 遗漏提醒

MCP 的成功工具调用会在返回文本里提示本次会话可能遗漏的条目：仍标 doing 且至少 30 分钟没有单项更新，或成功收尾时还有未结束条目。每次最多列三条及其余数量，同一条问题 30 分钟内不重复提醒。waiting、blocked、paused 会话不按运行超时催促；没有可靠时间的旧条目不推断超时。

条目的只读 `updatedAt` 由服务端维护。显式确认 `status=doing` 表示仍在实际执行；修改本条状态、文字、模块、备注或结果会更新该时间。同值元数据、重发相同计划、修改其他条目或会话标题不延后该条提醒。旧数据可用 startedAt 判断已开始的执行时间。

CLI 自动接收并显示提醒。直接调用会话 REST 的 Agent 可发送 `X-Tick-Agent-Request: 1`，从响应头 `X-Tick-Reminder` 读取 URL 编码文本并解码；响应 JSON 形状不变。普通 UI 请求不消费 Agent 提醒。提醒不改变状态、计时、已读、会话活动时间或自动归档条件，不产生通知弹窗；节流仅保存在当前服务进程内，重启后可能再次提醒。
