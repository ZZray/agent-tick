package main

// 命令行：给没接 MCP、只能跑 shell 的 agent 用（也免得在 PowerShell 5.1 里拼中文 JSON 乱码）。
// 走本机 HTTP API；服务没开会先在后台拉起。
//
//	tick start "标题" "第1步" "第2步" --agent codex [--ext 对话ID] [--link URL] [--goal 完成标准]
//	tick doing|done|skip|todo <会话> <序号> [--note 结果]
//	tick plan|add <会话> "条目" ...
//	tick rename <会话> "稳定主题 · 当前目标"
//	tick wait|blocked <会话> --note 问题
//	tick resume <会话>
//	tick finish <会话> --summary 总结 [--failed]
//	tick notify <会话> 文字 [--kind info|success|warning]
//	tick show <会话>   /   tick ls

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

var cliCommands = map[string]bool{"start": true, "plan": true, "add": true, "rename": true, "doing": true, "done": true, "skip": true, "todo": true,
	"wait": true, "blocked": true, "resume": true, "finish": true, "notify": true, "show": true, "ls": true, "help": true, "panel": true}

const cliHelp = `Tick 命令行（agent 用；协议见 http://127.0.0.1:7357/protocol）
  tick start "标题" "第1步" "第2步" --agent <名称> [--ext <对话ID>] [--parent <主会话ID>] [--link <URL>] [--goal <完成标准>]
       子 agent 用 --parent 挂到派它出来的会话下；--agent 可用环境变量 TICK_AGENT 代替
  tick doing|done|skip|todo <会话> <序号> [--note "结果或原因"]
  tick plan <会话> "条目"...      重发完整清单（文字相同的条目保留状态）
  tick add <会话> "条目"...       追加
  tick rename <会话> "稳定主题 · 当前目标"    更新标题，保留旧名供搜索
  tick wait <会话> --note "要你决定的问题"      会通知用户
  tick blocked <会话> --note "卡在哪"           会通知用户
  tick resume <会话>
  tick finish <会话> --summary "改动、验证、没覆盖的部分" [--failed]
  tick notify <会话> "文字" [--kind info|success|warning]
  tick show <会话>  |  tick ls  |  tick panel [会话]（打开完整面板）
  tick install [--dry-run]  |  tick uninstall  |  tick update  |  tick version
参数也可以写成 --note=… ；单独的 -- 之后全部当正文。退出码：0 成功，1 用法或接口错误，2 连不上 Tick（不影响主任务）`

func runCLI(port int, args []string) int {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if args[0] == "help" {
		fmt.Println(cliHelp)
		return 0
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return 1
	}
	// 参数：--k v 或 --k=v；--failed 是开关；单独的 -- 之后全部当正文
	pos, flags := []string{}, map[string]string{}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			k, v, inline := strings.Cut(a[2:], "=")
			switch {
			case k == "failed":
				flags[k] = "1"
			case inline:
				flags[k] = v
			case i+1 < len(args):
				flags[k] = args[i+1]
				i++
			default:
				return fail("--%s 后面缺少值", k)
			}
			continue
		}
		pos = append(pos, a)
	}
	for i := range pos[:min(len(pos), 1)] { // 会话 ID 会拼进路径，先转义
		if args[0] != "start" {
			pos[i] = url.PathEscape(pos[i])
		}
	}
	need := func(n int, what string) bool {
		if len(pos) < n {
			fail("缺少%s。\n\n%s", what, cliHelp)
			return false
		}
		return true
	}
	if !pingTick(port) {
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe)
			hideConsole(cmd)
			_ = cmd.Start()
			for i := 0; i < 25 && !pingTick(port); i++ {
				time.Sleep(200 * time.Millisecond)
			}
		}
		if !pingTick(port) {
			fmt.Fprintf(os.Stderr, "Tick 服务没连上（%s）。这不影响主任务。\n", base)
			return 2
		}
	}
	call := func(method, path string, body any) (*Session, error) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, r)
		req.Header.Set("Content-Type", "application/json")
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 300 {
			var e struct{ Error string }
			if json.Unmarshal(raw, &e) != nil || e.Error == "" {
				e.Error = fmt.Sprintf("HTTP %d %s", res.StatusCode, clip(strings.TrimSpace(string(raw)), 200))
			}
			return nil, fmt.Errorf("%s", e.Error)
		}
		var s Session
		_ = json.Unmarshal(raw, &s)
		var h struct{ Hint string }
		_ = json.Unmarshal(raw, &h)
		s.Hint = h.Hint
		return &s, nil
	}
	opt := func(k string) *string {
		if v, ok := flags[k]; ok {
			return &v
		}
		return nil
	}
	cmd := args[0]
	var s *Session
	var err error
	switch cmd {
	case "start":
		if !need(1, "标题") {
			return 1
		}
		agent := flags["agent"]
		if agent == "" {
			agent = os.Getenv("TICK_AGENT")
		}
		if agent == "" {
			return fail("缺少 --agent（claude / codex / cursor / kiro …）")
		}
		cwd, _ := os.Getwd()
		body := map[string]any{"agent": agent, "title": pos[0], "workspace": cwd, "items": pos[1:]}
		for _, k := range []string{"link", "goal"} {
			if v := opt(k); v != nil {
				body[k] = *v
			}
		}
		if v := opt("parent"); v != nil {
			body["parentId"] = *v
		}
		if v := opt("ext"); v != nil {
			body["externalId"] = *v
		}
		s, err = call("POST", "/api/sessions", body)
		if err == nil {
			fmt.Println(s.ID) // 第一行永远是会话 ID（agent 靠它取 ID）
			if s.Hint != "" {
				fmt.Println(s.Hint)
			}
		}
	case "rename":
		if !need(2, "会话 ID 和标题") {
			return 1
		}
		s, err = call("PATCH", "/api/sessions/"+pos[0], map[string]any{"title": strings.Join(pos[1:], " ")})
	case "plan", "add":
		if !need(2, "会话 ID 和条目") {
			return 1
		}
		method := "PUT"
		if cmd == "add" {
			method = "POST"
		}
		s, err = call(method, "/api/sessions/"+pos[0]+"/items", map[string]any{"items": pos[1:]})
	case "doing", "done", "skip", "todo":
		if !need(2, "会话 ID 和条目序号") {
			return 1
		}
		status := map[string]string{"doing": "doing", "done": "done", "skip": "skipped", "todo": "todo"}[cmd]
		body := map[string]any{"status": status}
		if v := opt("note"); v != nil {
			body["note"] = *v
		} else if len(pos) > 2 {
			body["note"] = strings.Join(pos[2:], " ")
		}
		s, err = call("PATCH", "/api/sessions/"+pos[0]+"/items/"+pos[1], body)
	case "wait", "blocked":
		if !need(1, "会话 ID") {
			return 1
		}
		note := flags["note"]
		if note == "" && len(pos) > 1 {
			note = strings.Join(pos[1:], " ")
		}
		if note == "" {
			return fail("写清要用户做什么：--note \"…\"")
		}
		status := map[string]string{"wait": "waiting", "blocked": "blocked"}[cmd]
		s, err = call("PATCH", "/api/sessions/"+pos[0], map[string]any{"status": status, "note": note})
	case "resume":
		if !need(1, "会话 ID") {
			return 1
		}
		s, err = call("PATCH", "/api/sessions/"+pos[0], map[string]any{"status": "running"})
	case "finish":
		if !need(1, "会话 ID") {
			return 1
		}
		summary := flags["summary"]
		if summary == "" && len(pos) > 1 {
			summary = strings.Join(pos[1:], " ")
		}
		if summary == "" {
			return fail("收尾要写总结：--summary \"…\"")
		}
		status := "done"
		if flags["failed"] != "" {
			status = "failed"
		}
		s, err = call("PATCH", "/api/sessions/"+pos[0], map[string]any{"status": status, "summary": summary})
	case "notify":
		if !need(2, "会话 ID 和文字") {
			return 1
		}
		kind := flags["kind"]
		if kind == "" {
			kind = "info"
		}
		_, err = call("POST", "/api/notices", map[string]any{"sessionId": pos[0], "kind": kind, "text": strings.Join(pos[1:], " ")})
		if err == nil {
			fmt.Println("已通知")
			return 0
		}
	case "show":
		if !need(1, "会话 ID") {
			return 1
		}
		s, err = call("GET", "/api/sessions/"+pos[0], nil)
	case "panel":
		body := map[string]any{}
		if len(pos) > 0 {
			body["session"] = pos[0]
		}
		if _, err := call("POST", "/api/desktop/panel", body); err != nil {
			return fail("Tick：%v", err)
		}
		return 0
	case "ls":
		res, e := http.Get(base + "/api/sessions")
		if e != nil {
			return fail("Tick：%v", e)
		}
		defer res.Body.Close()
		var list []*Session
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&list) != nil {
			return fail("Tick：读会话列表失败（HTTP %d）", res.StatusCode)
		}
		for _, x := range list {
			if !isEnded(x.Status) {
				sub := ""
				if x.ParentID != "" {
					sub = "  ↳ " + x.ParentID
				}
				fmt.Printf("%s  %-8s %-4s  %s%s\n", x.ID, x.Agent, statusLabel[x.Status], x.Title, sub)
			}
		}
		return 0
	}
	if err != nil {
		return fail("Tick：%v", err)
	}
	if cmd == "start" {
		fmt.Println(describe(s))
		return 0
	}
	// 写操作只回一行（会话、状态、进度），不把整张清单再打一遍，省 agent 的上下文；要看全貌用 tick show
	if s != nil {
		head, _, _ := strings.Cut(describe(s), "\n  ")
		fmt.Println(strings.Replace(head, "\n", "  ", 1))
	}
	return 0
}
