package main

// MCP（Streamable HTTP，POST /mcp）：agent 直接调工具登记清单、勾选、通知用户。
// 不支持 HTTP MCP 的客户端用 `tick mcp`（stdio 桥，见 bridge.go）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var mcpProtocols = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

var agentPatterns = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`(?i)claude`), "claude"}, {regexp.MustCompile(`(?i)cursor`), "cursor"},
	{regexp.MustCompile(`(?i)codex`), "codex"}, {regexp.MustCompile(`(?i)kiro`), "kiro"},
	{regexp.MustCompile(`(?i)copilot`), "copilot"}, {regexp.MustCompile(`(?i)windsurf|codeium`), "windsurf"},
}

const mcpInstructions = `Tick 是用户本机的任务看板和通知窗口，用户靠它看进度、接收你的通知。
- 多步任务（2 步以上或预计超过 5 分钟）必须用：开工前 tick_start 写完整清单，记住返回的 session_id，本对话一直用它。
- 一个对话只有一个会话：同一对话来了新任务，用 tick_plan mode=append 追加到原会话，不要再 tick_start（原会话 finish 过也照样追加）。不要省略或伪造 conversation_id 来另开重复会话；tick_start 返回里列出的“没收尾的会话”只处理本对话自己开的。
- 目标实质变化时用 tick_rename 更新标题，保留稳定主题，例如 Tick · 展开与标题优化。标题简短，普通步骤和追问不改名；旧名保留供搜索，清单、状态及对话关联不变。
- 每完成一条马上 done，做完全部（或用户说不用做了）必须 tick_finish，不要让做完的会话挂在进行中。
- 清单要完整：从第一步到验证、提交，整件事要做的每一步都写上，每条一个能检查的结果，验证单独成条；不要只写“分析 / 实现 / 测试”几个大阶段。跨多个模块时用 "# 模块名" 一项分段，后面的条目归这个模块。计划变了用 tick_plan 重发完整清单。
- 每条开始前 tick_item status=doing，做完马上 status=done 并在 note 写结果或证据。不要攒到最后一起勾。计划变了用 tick_plan。
- 需要用户决定或操作时必须走 Tick：tick_notify kind=waiting，message 写成一眼能答的问题（给选项和建议）。卡在环境或权限上用 kind=blocked。
- 用户回答后的第一个动作就是更新 Tick：马上 tick_item status=doing 下一条（会话自动回到进行中）；回答改变了计划就先 tick_plan 重发清单；只是追问就再 kind=waiting，不用做了就 tick_finish。不要让会话一直停在“等你”。
- 一条预计超过 10 分钟（全量编译、长测试）：doing 时在 note 写预计多久，跑完马上 done。Tick 30 分钟收不到更新会标成“可能掉线了”。
- 做完了只想问接下来做什么：只发一次 kind=waiting（先一句结论再给选项），不要先 tick_finish 再问，用户会收到两条矛盾的提醒。用户选完再 doing 或 tick_finish。
- 报“没有会话”：用户在 Tick 里删掉了它；只有接着做同一件事时才重新 tick_start（同样的 conversation_id）。
- 结束时 tick_finish 写总结（改动、验证结果、没覆盖的部分），用户会收到通知。
- 结束本轮回复前，会话状态必须和你一致：还有活就别结束回复（进度汇报写在工具调用之间）；要用户回答就 kind=waiting；做完了就 tick_finish。不要让会话停在“进行中”却没人在做。
- 其他值得用户知道的事用 tick_notify kind=info / success / warning。不要写密钥。Tick 连不上不影响主任务。`

// 每个 MCP 连接记住客户端名（推断 agent）和最近登记的会话；同一连接可能并发调用
type mcpConn struct {
	mu    sync.Mutex
	agent string
	last  string
}

func (c *mcpConn) get() (agent, last string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agent, c.last
}

func (c *mcpConn) setAgent(a string) { c.mu.Lock(); c.agent = a; c.mu.Unlock() }
func (c *mcpConn) setLast(id string) { c.mu.Lock(); c.last = id; c.mu.Unlock() }

type mcpSessions struct {
	mu    sync.Mutex
	conns map[string]*mcpConn
}

func newMCPSessions() *mcpSessions { return &mcpSessions{conns: map[string]*mcpConn{}} }

func (m *mcpSessions) create(id string) *mcpConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := &mcpConn{agent: "agent"}
	m.conns[id] = c
	return c
}

// lookup：没带会话头的客户端各次调用互不相关；带了但不认识（比如 Tick 重启过）返回 false，让客户端重新初始化
func (m *mcpSessions) lookup(id string) (*mcpConn, bool) {
	if id == "" {
		return &mcpConn{agent: "agent"}, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	return c, ok
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (srv *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		srv.mcp.mu.Lock()
		delete(srv.mcp.conns, r.Header.Get("Mcp-Session-Id"))
		srv.mcp.mu.Unlock()
		w.WriteHeader(204)
		return
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeJSON(w, 405, map[string]string{"error": "MCP 只接受 POST"})
		return
	}
	var req rpcRequest
	if err := decode(r, &req); err != nil {
		writeJSON(w, 400, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, err.Error()}})
		return
	}
	var c *mcpConn
	if req.Method == "initialize" {
		sid := newID("m_", 8)
		w.Header().Set("Mcp-Session-Id", sid)
		c = srv.mcp.create(sid)
	} else {
		var ok bool
		if c, ok = srv.mcp.lookup(r.Header.Get("Mcp-Session-Id")); !ok {
			writeJSON(w, 404, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32001, "MCP 会话已失效（Tick 可能重启过），请重新初始化"}})
			return
		}
	}
	res := srv.mcpCall(c, req)
	if res == nil { // 通知类消息没有回复
		w.WriteHeader(202)
		return
	}
	writeJSON(w, 200, res)
}

func (srv *Server) mcpCall(c *mcpConn, req rpcRequest) *rpcResponse {
	isRequest := len(req.ID) > 0 && string(req.ID) != "null"
	reply := func(result any) *rpcResponse { return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result} }
	fail := func(code int, msg string) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{code, msg}}
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if name := p.ClientInfo.Name; name != "" {
			agent := clip(strings.ToLower(name), maxAgent-1)
			for _, ap := range agentPatterns {
				if ap.re.MatchString(name) {
					agent = ap.name
					break
				}
			}
			c.setAgent(agent)
		}
		pv := mcpProtocols[len(mcpProtocols)-1]
		if slices.Contains(mcpProtocols, p.ProtocolVersion) {
			pv = p.ProtocolVersion
		}
		return reply(map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "tick", "version": version},
			"instructions":    mcpInstructions,
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		return reply(map[string]any{"tools": mcpTools})
	case "resources/list":
		return reply(map[string]any{"resources": []any{}})
	case "prompts/list":
		return reply(map[string]any{"prompts": []any{}})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(-32602, "参数不是合法的 tools/call")
		}
		if _, ok := mcpHandlers[p.Name]; !ok {
			return fail(-32602, "没有工具 "+p.Name)
		}
		text, err := mcpHandlers[p.Name](srv.store, c, toolArgs(p.Arguments))
		if err != nil {
			return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": "Tick：" + err.Error()}}, "isError": true})
		}
		return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	}
	if !isRequest {
		return nil
	}
	return fail(-32601, "不支持的方法 "+req.Method)
}

// ---------- 工具 ----------

type toolArgs map[string]any

func (a toolArgs) str(key string) *string {
	if v, ok := a[key].(string); ok {
		return &v
	}
	return nil
}

func (a toolArgs) need(key string) (*string, error) {
	v := a.str(key)
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, fmt.Errorf("参数 %s 缺失或不是字符串", key)
	}
	return v, nil
}

func (a toolArgs) items() ([]json.RawMessage, error) {
	raw, ok := a["items"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("items 必须是字符串数组")
	}
	out := make([]json.RawMessage, len(list))
	for i, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("items[%d] 必须是字符串", i)
		}
		out[i], _ = json.Marshal(s)
	}
	return out, nil
}

func (a toolArgs) session(c *mcpConn) (string, error) {
	if v := a.str("session_id"); v != nil && *v != "" {
		return *v, nil
	}
	_, last := c.get()
	if last == "" {
		return "", fmt.Errorf("还没有会话：先调用 tick_start，或传 session_id")
	}
	return last, nil
}

func describe(s *Session) string {
	done := 0
	for _, it := range s.Items {
		if it.Status == "done" || it.Status == "skipped" {
			done++
		}
	}
	marks := map[string]string{"todo": "[ ]", "doing": "[~]", "done": "[x]", "skipped": "[-]", "blocked": "[!]"}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n%s · %s", s.ID, s.Title, s.Agent, statusLabel[s.Status])
	if s.StatusNote != "" {
		b.WriteString("：" + s.StatusNote)
	}
	fmt.Fprintf(&b, " · 清单 %d/%d", done, len(s.Items))
	group := ""
	for i, it := range s.Items {
		if it.Group != group {
			group = it.Group
			fmt.Fprintf(&b, "\n  # %s", group)
		}
		fmt.Fprintf(&b, "\n  %s %d. %s", marks[it.Status], i+1, it.Text)
		if it.Note != "" {
			b.WriteString("\n        " + it.Note)
		}
	}
	if s.Summary != "" {
		b.WriteString("\n总结：" + s.Summary)
	}
	return b.String()
}

var statusLabel = map[string]string{"planning": "写清单", "running": "进行中", "waiting": "等你", "blocked": "受阻", "done": "完成", "failed": "失败", "cancelled": "已取消"}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var sidProp = map[string]any{"type": "string", "description": "会话 ID（tick_start 返回的 s_ 开头那串）。省略时用本连接最近登记的会话。"}

const itemsDesc = "完整清单：从第一步到验证、提交，按执行顺序把整件事要做的每一步都写上，每条一个能检查的结果，不要只写几个大阶段。" +
	"涉及多个模块时用 \"# 模块名\" 单独一项开一段，后面的条目归这个模块（例：[\"# 后端\", \"加接口\", \"补测试\", \"# 前端\", \"接页面\"]）；标题行不算条目、不占序号"

var strArr = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": itemsDesc}

var mcpTools = []map[string]any{
	{"name": "tick_start", "description": "登记一个任务会话并写下清单，用户的 Tick 小窗会立刻显示。多步任务开工前调用一次；同一对话里的后续任务用 tick_plan 追加，不另开会话。返回 session_id。",
		"inputSchema": schema(map[string]any{
			"title":             map[string]any{"type": "string", "description": "任务标题，一句话"},
			"items":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": itemsDesc},
			"goal":              map[string]any{"type": "string", "description": "完成标准（可选）"},
			"conversation_id":   map[string]any{"type": "string", "description": "你所在对话的 ID（能拿到就给）。同一 ID 再次调用会拿回原会话"},
			"link":              map[string]any{"type": "string", "description": "能回到这个对话的链接（可选），例如 codex://threads/<id>"},
			"workspace":         map[string]any{"type": "string", "description": "工作目录（可选）"},
			"parent_session_id": map[string]any{"type": "string", "description": "你是被派出去的子 agent 时，填派你出来的那个会话的 session_id；你的进度会显示在它下面"},
		}, "title")},
	{"name": "tick_rename", "description": "更新当前会话标题并保留旧名供搜索。任务目标实质变化时使用，标题保留稳定主题，例如 Tick · 展开与标题优化；普通步骤和追问不改名。不会新建会话或改变清单与状态。",
		"inputSchema": schema(map[string]any{"session_id": sidProp, "title": map[string]any{"type": "string", "description": "简短标题，保留主题并说明当前目标；最多 200 字"}}, "title")},
	{"name": "tick_plan", "description": "修改清单。mode=replace（默认）重发完整清单，文字相同的条目保留状态和耗时；mode=append 只追加。",
		"inputSchema": schema(map[string]any{"session_id": sidProp, "items": strArr, "mode": map[string]any{"type": "string", "enum": []string{"replace", "append"}}}, "items")},
	{"name": "tick_item", "description": "更新清单条目。开始前 status=doing，做完 status=done（note 写结果或证据），决定不做 status=skipped（note 写原因）。耗时由 Tick 记录。",
		"inputSchema": schema(map[string]any{
			"session_id": sidProp,
			"n":          map[string]any{"type": "integer", "minimum": 1, "description": "条目序号，从 1 开始"},
			"status":     map[string]any{"type": "string", "enum": []string{"doing", "done", "skipped", "todo", "blocked"}},
			"note":       map[string]any{"type": "string", "description": "结果、证据或原因，一句话"},
		}, "n", "status")},
	{"name": "tick_notify", "description": "给用户发通知：弹出系统通知，并在 Tick 小窗置顶显示。kind=waiting：需要用户决定或操作（会话转为“等你”）；kind=blocked：卡在环境、权限或依赖上；info / success / warning：其他值得知道的事。",
		"inputSchema": schema(map[string]any{
			"message":    map[string]any{"type": "string", "description": "通知内容。waiting 时写成用户一眼能答的问题"},
			"kind":       map[string]any{"type": "string", "enum": []string{"info", "success", "warning", "waiting", "blocked"}},
			"session_id": sidProp,
		}, "message")},
	{"name": "tick_finish", "description": "结束会话并通知用户。summary 写改动、验证结果和没覆盖的部分；没做成时 failed=true。",
		"inputSchema": schema(map[string]any{"session_id": sidProp, "summary": map[string]any{"type": "string"}, "failed": map[string]any{"type": "boolean"}}, "summary")},
	{"name": "tick_show", "description": "查看会话的清单和状态。上下文被压缩、不确定做到哪一步时用。",
		"inputSchema": schema(map[string]any{"session_id": sidProp})},
}

type toolFn func(st *Store, c *mcpConn, a toolArgs) (string, error)

var mcpHandlers = map[string]toolFn{
	"tick_start": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		title, err := a.need("title")
		if err != nil {
			return "", err
		}
		items, err := a.items()
		if err != nil {
			return "", err
		}
		agent, _ := c.get()
		s, attached, err := st.Start(StartInput{Agent: &agent, Title: title, ExternalID: a.str("conversation_id"), Goal: a.str("goal"),
			Workspace: a.str("workspace"), Link: a.str("link"), ParentID: a.str("parent_session_id"), Items: items})
		if err != nil {
			return "", err
		}
		c.setLast(s.ID)
		how := "新建"
		hint := ""
		if attached {
			how = "拿回了原会话"
		} else if s.ParentID == "" {
			if h := unfinishedHint(st.Unfinished(s.Agent, s.ID)); h != "" {
				hint = "\n" + h
			}
		}
		return fmt.Sprintf("session_id: %s（%s）。本对话后续调用都用它。\n%s%s", s.ID, how, describe(s), hint), nil
	},
	"tick_rename": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		id, err := a.session(c)
		if err != nil {
			return "", err
		}
		title, err := a.need("title")
		if err != nil {
			return "", err
		}
		s, err := st.Patch(id, PatchInput{Title: title})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("会话 %s 的标题已更新为「%s」，旧名保留供搜索。", s.ID, s.Title), nil
	},
	"tick_plan": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		id, err := a.session(c)
		if err != nil {
			return "", err
		}
		items, err := a.items()
		if err != nil {
			return "", err
		}
		if items == nil {
			return "", fmt.Errorf("缺少 items")
		}
		var s *Session
		switch mode := a.str("mode"); {
		case mode == nil || *mode == "replace":
			s, err = st.ReplacePlan(id, items, false)
		case *mode == "append":
			s, err = st.AppendItems(id, items, nil, false)
		default:
			return "", fmt.Errorf("mode 只能是 replace 或 append")
		}
		if err != nil {
			return "", err
		}
		return describe(s), nil
	},
	"tick_item": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		id, err := a.session(c)
		if err != nil {
			return "", err
		}
		n, ok := a["n"].(float64)
		if !ok || n < 1 || n != float64(int(n)) {
			return "", fmt.Errorf("n 是从 1 开始的整数")
		}
		status, err := a.need("status")
		if err != nil {
			return "", err
		}
		s, err := st.PatchItem(id, fmt.Sprint(int(n)), ItemPatch{Status: status, Note: a.str("note")})
		if err != nil {
			return "", err
		}
		it := s.Items[int(n)-1]
		done := 0
		next := -1
		for i, x := range s.Items {
			if x.Status == "done" || x.Status == "skipped" {
				done++
			}
			if next < 0 && x.Status == "todo" {
				next = i
			}
		}
		out := fmt.Sprintf("#%d %s → %s · 清单 %d/%d · %s", int(n), it.Text, it.Status, done, len(s.Items), statusLabel[s.Status])
		if next >= 0 && it.Status == "done" {
			out += fmt.Sprintf("\n下一条：#%d %s", next+1, s.Items[next].Text)
		}
		return out, nil
	},
	"tick_notify": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		msg, err := a.need("message")
		if err != nil {
			return "", err
		}
		kind := "info"
		if k := a.str("kind"); k != nil {
			kind = *k
		}
		if isWaiting(kind) {
			id, err := a.session(c)
			if err != nil {
				return "", err
			}
			s, err := st.Patch(id, PatchInput{Status: &kind, Note: msg})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已通知用户，会话转为「%s」。用户回复后接着做即可（doing 下一条会自动恢复）。", statusLabel[s.Status]), nil
		}
		in := NotifyInput{Kind: &kind, Text: msg}
		agent, last := c.get()
		if v := a.str("session_id"); v != nil && *v != "" {
			in.SessionID = v
		} else if last != "" {
			in.SessionID = &last
		} else {
			in.Agent = &agent
		}
		if _, err := st.Notify(in); err != nil {
			return "", err
		}
		return "已通知用户。", nil
	},
	"tick_finish": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		id, err := a.session(c)
		if err != nil {
			return "", err
		}
		summary, err := a.need("summary")
		if err != nil {
			return "", err
		}
		status := "done"
		if f, _ := a["failed"].(bool); f {
			status = "failed"
		}
		s, err := st.Patch(id, PatchInput{Status: &status, Summary: summary})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("会话已结束（%s），用户会收到通知。\n%s", statusLabel[s.Status], describe(s)), nil
	},
	"tick_show": func(st *Store, c *mcpConn, a toolArgs) (string, error) {
		id, err := a.session(c)
		if err != nil {
			return "", err
		}
		s, err := st.Session(id)
		if err != nil {
			return "", err
		}
		return describe(s), nil
	},
}
