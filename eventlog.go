package main

// 日志文件：数据目录里的 tick.log。进程日志（启动、报错）和事件日志（谁对哪个会话做了什么）写在同一个文件，按时间顺序读。
// 事件日志的行首带标签，方便搜：
//   [mcp]  MCP 连接、断开、每次工具调用（连接号 conn、客户端、来源端口、会话、参数里的状态/序号）
//   [api]  REST 写请求（方法、路径、状态码、来源端口；(界面) 表示来自 Tick 自己的页面）
//   [会话] 登记、状态变化
//   [通知] 新通知、已读、过期
//   [贴边] 松手吸附、收起、展开
// 只记 ID、状态、条数这类，不记清单文字、备注和通知正文（可能带敏感内容）。
// 来源端口可以对上 `Get-NetTCPConnection -RemotePort <端口>` 找到是哪个进程在连。

import (
	"bytes"
	"os"
	"sync"
)

const logLimit = 2 << 20 // tick.log 超过 2MB 就只留后一半

// trimWriter 给标准库 log 用：每写 200 行检查一次大小，超了就裁掉前一半。
// 守护进程和干活的子进程同时往这个文件追加，所以不能改名轮转（Windows 上改名会因文件被占用失败），只能原地裁短
type trimWriter struct {
	mu sync.Mutex
	f  *os.File
	n  int
}

func (w *trimWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n++; w.n%200 == 0 {
		trimLog(w.f.Name(), logLimit)
	}
	return w.f.Write(p)
}

// trimLog 文件超过 limit 就只留后一半，并从下一个整行开始
func trimLog(path string, limit int64) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < limit {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	b = b[len(b)/2:]
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	_ = os.WriteFile(path, b, 0o644)
}
