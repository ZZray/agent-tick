package main

// `tick mcp`：stdio ↔ HTTP MCP 桥。每行一条 JSON-RPC，转发到本机 /mcp；服务没开就在后台拉起 tick.exe。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func runBridge(port int) int {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	if !pingTick(port) {
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe)
			hideConsole(cmd)
			_ = cmd.Start()
			for i := 0; i < 25 && !pingTick(port); i++ {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	client := http.Client{Timeout: 30 * time.Second}
	session := ""
	out := bufio.NewWriter(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), maxBody)
	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(line))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		res, err := client.Do(req)
		if err != nil {
			// 只给请求回错误；通知类消息（没有 id）丢掉即可
			if id := rpcID(line); id != "" {
				fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"Tick 服务没连上（%s）。这不影响主任务。"}}`+"\n", id, url)
				out.Flush()
			}
			continue
		}
		if s := res.Header.Get("Mcp-Session-Id"); s != "" {
			session = s
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		body = bytes.TrimSpace(body)
		// 只把 JSON-RPC 消息交给客户端；HTTP 层的错误（403/415/404 等）转成 JSON-RPC 错误
		var probe struct {
			JSONRPC string `json:"jsonrpc"`
			Error   any    `json:"error"`
		}
		if len(body) > 0 && (json.Unmarshal(body, &probe) != nil || probe.JSONRPC != "2.0") {
			if id := rpcID(line); id != "" {
				msg, _ := json.Marshal(fmt.Sprintf("Tick 返回 HTTP %d：%s", res.StatusCode, clip(string(body), 200)))
				body = fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%s}}`, id, msg)
			} else {
				body = nil
			}
		}
		if res.StatusCode == 404 && probe.JSONRPC == "2.0" {
			session = "" // 会话失效，客户端会重新初始化，桥也换新的会话
		}
		if len(body) > 0 {
			out.Write(body)
			out.WriteByte('\n')
			out.Flush()
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "tick mcp：读标准输入失败（单条消息上限 %d 字节）：%v\n", maxBody, err)
		return 1
	}
	return 0
}

func rpcID(line []byte) string {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
		return ""
	}
	return string(m.ID)
}
