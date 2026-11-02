# 远端 agent 向本机 Tick 上报进度

适用：Tick 0.5.17 起的 `gateway` 命令。Tick 所在电脑保存会话并显示悬浮窗；另一台电脑上的 agent 通过 HTTP MCP 登记任务、勾选清单和发送通知。远端电脑无需安装 Tick。

## 在显示 Tick 的电脑上启动网关

先启动这台电脑的 Tick。原服务继续监听 `127.0.0.1:7357`，网关另监听指定的局域网 IP 和端口。

```powershell
$tickExe = "$env:LOCALAPPDATA\Programs\Tick\tick.exe"
$tokenFile = Join-Path $env:APPDATA 'Tick\private\gateway.token'
New-Item -ItemType Directory -Force (Split-Path $tokenFile) | Out-Null
Start-Process -FilePath $tickExe -WindowStyle Hidden -ArgumentList @(
  'gateway', '--listen', '<本机局域网IP>:7358', '--token-file', "`"$tokenFile`""
)
```

`--listen` 要填这台电脑的具体 IP。`--token-file` 指向本机私有文件；不存在时生成随机 32 字节令牌并保存为十六进制文本，不打印令牌。已有文件会复用。Windows 下还应使用文件夹 ACL 限制读取；不要把它放进项目、共享目录或日志。需要把令牌安全地交给目标电脑上的 agent 客户端配置。

非默认 Tick 端口通过 `--port <端口>` 指定。例如本机服务在 `17357` 时，给上述参数追加 `'--port', '17357'`。这是网关要连接的本机服务端口；客户端仍连接网关的 `7358`。

网关只在当前进程运行期间可用，不注册开机任务。关闭网关会断开远端接入；本机 Tick 的会话数据和窗口保持原有归属。

## 允许局域网访问网关端口

如果 Windows 防火墙拦截，在管理员 PowerShell 为选定的本机地址添加限定局域网来源的规则：

```powershell
New-NetFirewallRule -DisplayName 'Tick MCP Gateway' `
  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 7358 `
  -LocalAddress '<本机局域网IP>' -RemoteAddress LocalSubnet
```

不要开放本机会话服务的 `7357` 端口。发布电脑自访问成功不证明另一台电脑可达；路由和防火墙需从目标电脑确认。

## 在另一台电脑配置 HTTP MCP

地址填写网关电脑的局域网 IP。所有示例中的令牌都是占位符，不要提交实际令牌。

Codex 的 `config.toml`：

```toml
[mcp_servers.tick]
url = "http://<Tick电脑IP>:7358/mcp"
bearer_token_env_var = "TICK_REMOTE_TOKEN"
```

在启动 Codex 的环境里设置 `TICK_REMOTE_TOKEN` 为实际令牌。该字段只给这个 MCP server 附加认证头。保存配置后重新连接该 MCP server，或重开客户端；不要把其他模型/API 的认证变量改成此令牌。

Cursor / Kiro 的 MCP 配置：

```json
{
  "mcpServers": {
    "tick": {
      "url": "http://<Tick电脑IP>:7358/mcp",
      "headers": {
        "Authorization": "Bearer <实际令牌>"
      }
    }
  }
}
```

已有 `tick` 配置时修改这一项，避免另加一个连接到其他 Tick 的同名入口。配置文件留在目标电脑本地，令牌不放进任务描述或清单。若客户端不支持 HTTP MCP 或认证头，需要先确认它支持的接入方式，不能直接把宿主机的 `127.0.0.1` 地址填到远端电脑。

连接后可告诉 agent：

> 通过已连接的 Tick MCP，把本对话的任务和进度上报到这台 Tick。一个对话复用一个会话，逐条更新清单，需要我决定或完成时通知；不要在远端启动另一份 Tick。

## 权限和数据归属

网关只接受带正确 Bearer 令牌的 `POST /mcp` 与 `DELETE /mcp`，拒绝带 `Origin` 的请求；网页、会话 REST、桌面设置、技能写入和其他路径不开放。本机服务的 `127.0.0.1` 与 Host 校验规则不变。

持有令牌的 agent 可以调用 MCP 已提供的任务工具；令牌不是每个项目或每台客户端的权限隔离。只把它交给允许使用这份看板的客户端。普通 HTTP 适用于可信局域网；跨不可信网络需要带身份验证的加密通道。

会话保存在 Tick 电脑的原数据目录。远端 agent 使用 MCP 工具即可，不需要执行 `tick.exe`，也不需要复制 Tick 电脑的会话、窗口设置或本地安装配置。

## 接入验证

1. 在目标电脑执行 `Test-NetConnection <Tick电脑IP> -Port 7358`，确认 TCP 可达。
2. 连接 MCP，确认客户端能列出 `tick_start`、`tick_item`、`tick_finish` 等工具。认证失败时检查令牌和认证头，不能通过去掉网关鉴权处理。
3. 让 agent 登记一条明确标注的测试任务，更新一条清单并结束；在 Tick 电脑确认同一会话的标题、进度和通知。
4. 测试完只归档该测试会话，不清理其他 agent 的任务。

TCP 成功只证明网络端口可达；MCP 初始化和工具调用证明协议可用，宿主窗口中的显示与通知需另行观察。没有目标电脑的实际调用记录时，跨机接入仍记为未验证。
