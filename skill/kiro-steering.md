# Tick 状态同步（强制）

用户看 Tick 小窗了解你在做什么。Tick 的状态必须随时和实际一致，按下面的时机立即同步，不攒到最后补。完整用法见 `tick` 技能（`~/.kiro/skills/tick/SKILL.md`）。

| 时机 | 必须做的事 |
|---|---|
| 开始 2 步以上的任务 | 先 `tick start` 写完整清单；本对话已有会话就 `tick add` 追加，不另开 |
| 开始某一条 | 先标 `doing` |
| 做完某一条 | 马上标 `done`，note 写能核对的结果 |
| 卡住（环境、权限、失败） | `tick notify kind=blocked` 写清卡在哪 |
| 需要用户回答或操作 | `tick wait` 写成一眼能答的问题 |
| 收到用户回复或新指令 | 第一个工具调用就是更新 Tick：下一条标 `doing`；新任务先 `add` 再标 `doing`（只 `add` 不算更新）；不用做的 `skip` 写原因 |
| 全部做完或用户说不做了 | `tick finish` 写总结 |

结束一轮回复前检查一次：还有活就继续做，不能停在“进行中”却没人执行；要用户回答就 `wait`；做完就 `finish`。

调用方式：有 `tick_*` MCP 工具优先用；没有就用 `{{EXE}}`，它是窗口程序，PowerShell 里要接 `| Out-String` 才能拿到输出。
