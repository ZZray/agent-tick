package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type harness struct {
	t    *testing.T
	srv  *Server
	ts   *httptest.Server
	base string
	dir  string
}

// 用真实端口起服务：Host 校验依赖端口号
func newHarness(t *testing.T, dir string) *harness {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() }) // 在临时目录删掉之前写完，免得定时器之后再写
	srv := &Server{store: st, port: ln.Addr().(*net.TCPAddr).Port, mcp: newMCPSessions()}
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	return &harness{t: t, srv: srv, ts: ts, base: ts.URL, dir: dir}
}

func (h *harness) do(method, path string, body any, headers ...string) (int, map[string]any, []byte) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, h.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m, raw
}

func (h *harness) ok(method, path string, body any) map[string]any {
	h.t.Helper()
	code, m, raw := h.do(method, path, body)
	if code >= 300 {
		h.t.Fatalf("%s %s → %d %s", method, path, code, raw)
	}
	return m
}

func items(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["items"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func num(v any) float64 { f, _ := v.(float64); return f }

func (h *harness) notices(sid string) []map[string]any {
	_, _, raw := h.do("GET", "/api/notices", nil)
	var all []map[string]any
	_ = json.Unmarshal(raw, &all)
	var out []map[string]any
	for _, n := range all {
		if sid == "" || n["sessionId"] == sid {
			out = append(out, n)
		}
	}
	return out
}

func TestStartAndAttach(t *testing.T) {
	h := newHarness(t, t.TempDir())
	code, a, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "Codex", "title": "修复重连后状态不刷新", "externalId": "thread-1", "items": []string{"复现", "修复"}})
	if code != 201 || a["attached"] != false || a["agent"] != "codex" || a["status"] != "planning" || len(items(a)) != 2 {
		t.Fatalf("新建：%d %v", code, a)
	}
	code, b, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "换个标题", "externalId": "thread-1", "items": []string{"修复", "验证"}})
	if code != 200 || b["attached"] != true || b["id"] != a["id"] || b["title"] != "修复重连后状态不刷新" || len(items(b)) != 3 {
		t.Fatalf("拿回原会话：%d %v", code, b)
	}
	other := h.ok("POST", "/api/sessions", map[string]any{"agent": "claude", "title": "别的", "externalId": "thread-1"})
	if other["id"] == a["id"] {
		t.Fatal("不同 agent 不应共用会话")
	}
}

func TestItemTiming(t *testing.T) {
	h := newHarness(t, t.TempDir())
	s := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "计时", "items": []string{"一", "二"}})
	id := s["id"].(string)
	d := h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "doing"})
	if d["status"] != "running" || num(items(d)[0]["startedAt"]) == 0 {
		t.Fatalf("doing：%v", d)
	}
	time.Sleep(60 * time.Millisecond)
	d = h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"status": "done", "note": "好了"})
	it := items(d)[0]
	if it["status"] != "done" || it["note"] != "好了" || num(it["startedAt"]) != 0 || num(it["spentMs"]) < 50 || num(it["doneAt"]) == 0 {
		t.Fatalf("done：%v", it)
	}
	code, m, _ := h.do("PATCH", "/api/sessions/"+id+"/items/9", map[string]any{"status": "done"})
	if code != 404 || !strings.Contains(m["error"].(string), "共 2 项") {
		t.Fatalf("越界：%d %v", code, m)
	}
}

func TestWaitPausesAndResumes(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "claude", "title": "等待", "items": []string{"一", "二"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "doing"})
	w := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "要不要推送？"})
	if w["status"] != "waiting" || w["statusNote"] != "要不要推送？" || num(w["waitSince"]) == 0 || num(items(w)[0]["startedAt"]) != 0 {
		t.Fatalf("waiting：%v", w)
	}
	frozen := num(items(w)[0]["spentMs"])
	time.Sleep(60 * time.Millisecond)
	r := h.ok("PATCH", "/api/sessions/"+id+"/items/2", map[string]any{"status": "doing"})
	if r["status"] != "running" || r["statusNote"] != "" || num(r["waitSince"]) != 0 || num(r["waitMs"]) < 50 {
		t.Fatalf("恢复：%v", r)
	}
	if num(items(r)[0]["spentMs"]) != frozen || num(items(r)[0]["startedAt"]) == 0 {
		t.Fatal("等待时间不该算进条目耗时，恢复后原 doing 条目应继续计时")
	}
	f := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "done", "summary": "做完了"})
	if f["status"] != "done" || num(f["endedAt"]) == 0 {
		t.Fatalf("结束：%v", f)
	}
	for _, it := range items(f) {
		if num(it["startedAt"]) != 0 {
			t.Fatal("结束后不该还有在跑的计时")
		}
	}
}

func TestReopenExcludesIdle(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "重开", "externalId": "t-r", "items": []string{"一"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "done", "summary": "第一轮"})
	time.Sleep(60 * time.Millisecond)
	again := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "重开", "externalId": "t-r", "items": []string{"二"}})
	if again["status"] != "running" || num(again["endedAt"]) != 0 || num(again["idleMs"]) < 50 {
		t.Fatalf("重开：%v", again)
	}
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "?"})
	p := h.ok("PUT", "/api/sessions/"+id+"/items", map[string]any{"items": []any{"一", "二", map[string]string{"text": "三", "status": "doing"}}})
	if p["status"] != "running" || num(items(p)[2]["startedAt"]) == 0 {
		t.Fatalf("暂停中写入 doing 条目应自动恢复：%v", p)
	}
}

func TestReplacePlanKeepsState(t *testing.T) {
	h := newHarness(t, t.TempDir())
	s := h.ok("POST", "/api/sessions", map[string]any{"agent": "cursor", "title": "重排", "items": []string{"A", "B", "C"}})
	id := s["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "done"})
	p := h.ok("PUT", "/api/sessions/"+id+"/items", map[string]any{"items": []string{"A", "C", "D"}})
	got := []string{}
	for _, it := range items(p) {
		got = append(got, it["text"].(string)+":"+it["status"].(string))
	}
	if strings.Join(got, ",") != "A:done,C:todo,D:todo" || items(p)[0]["id"] != items(s)[0]["id"] {
		t.Fatalf("重发清单：%v", got)
	}
	if a := h.ok("POST", "/api/sessions/"+id+"/items", map[string]any{"text": "E"}); len(items(a)) != 4 {
		t.Fatal("追加失败")
	}
}

func TestNoticesFollowStatus(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "claude", "title": "通知", "items": []string{"一"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "推不推？"})
	n := h.notices(id)
	if len(n) != 1 || n[0]["kind"] != "waiting" || n[0]["text"] != "推不推？" || n[0]["read"] != false || n[0]["agent"] != "claude" || n[0]["title"] != "通知" {
		t.Fatalf("等你通知：%v", n)
	}
	h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "doing"})
	if h.notices(id)[0]["read"] != true {
		t.Fatal("恢复工作后等你通知应自动已读")
	}
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "done", "summary": "好了"})
	n = h.notices(id)
	if n[0]["kind"] != "done" || n[0]["text"] != "好了" || n[0]["read"] != false {
		t.Fatalf("做完通知：%v", n[0])
	}
	if r := h.ok("POST", "/api/notices/read", map[string]any{"sessionId": id}); num(r["updated"]) != 1 {
		t.Fatalf("标记已读：%v", r)
	}
}

func TestAgentNotify(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "主动通知"})["id"].(string)
	code, a, _ := h.do("POST", "/api/notices", map[string]any{"sessionId": id, "text": "已推到 dev1", "kind": "success"})
	if code != 201 || a["agent"] != "codex" || a["title"] != "主动通知" || a["kind"] != "success" {
		t.Fatalf("挂会话：%d %v", code, a)
	}
	code, b, _ := h.do("POST", "/api/notices", map[string]any{"agent": "Cursor", "text": "索引重建好了"})
	if code != 201 || b["agent"] != "cursor" || b["sessionId"] != "" || b["kind"] != "info" {
		t.Fatalf("只带 agent：%d %v", code, b)
	}
	for _, body := range []map[string]any{{"agent": "x", "text": "y", "kind": "waiting"}, {"text": "y"}} {
		if code, _, _ := h.do("POST", "/api/notices", body); code != 400 {
			t.Fatalf("应拒绝 %v，得到 %d", body, code)
		}
	}
	if code, _, _ := h.do("POST", "/api/notices/read", map[string]any{}); code != 400 {
		t.Fatal("空的已读请求应 400")
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t, t.TempDir())
	if code, m, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "codex"}); code != 400 || !strings.Contains(m["error"].(string), "title") {
		t.Fatalf("缺标题：%d %v", code, m)
	}
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "校验"})["id"].(string)
	if code, m, _ := h.do("PATCH", "/api/sessions/"+id, map[string]any{"status": "finished"}); code != 400 || !strings.Contains(m["error"].(string), "status 只能是") {
		t.Fatalf("坏状态：%d %v", code, m)
	}
	for _, l := range []string{"javascript://%0aalert(1)", "JavaScript://x", "file:///c:/x", "no-scheme"} {
		if code, _, _ := h.do("PATCH", "/api/sessions/"+id, map[string]any{"link": l}); code != 400 {
			t.Fatalf("应拒绝链接 %s", l)
		}
	}
	if m := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"link": "codex://threads/019a"}); m["link"] != "codex://threads/019a" {
		t.Fatal("合法链接没存上")
	}
	if code, _, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "x", "items": "A"}); code != 400 {
		t.Fatal("items 不是数组应 400")
	}
}

func TestGuards(t *testing.T) {
	h := newHarness(t, t.TempDir())
	port := h.srv.port
	if code, _, _ := h.do("GET", "/api/health", nil, "Host", fmt.Sprintf("evil.example:%d", port)); code != 403 {
		t.Fatalf("外部 Host 应 403，得到 %d", code)
	}
	body := map[string]any{"agent": "x", "title": "y"}
	if code, _, _ := h.do("POST", "/api/sessions", body, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("外部 Origin 应 403，得到 %d", code)
	}
	req, _ := http.NewRequest("POST", h.base+"/api/sessions", strings.NewReader(`{"agent":"x","title":"y"}`))
	req.Header.Set("Content-Type", "text/plain")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatalf("非 JSON 应 415，得到 %d", res.StatusCode)
	}
	if code, _, _ := h.do("POST", "/api/sessions", body, "Origin", fmt.Sprintf("http://127.0.0.1:%d", port)); code != 201 {
		t.Fatalf("同源应放行，得到 %d", code)
	}
}

func TestStream(t *testing.T) {
	h := newHarness(t, t.TempDir())
	res, err := http.Get(h.base + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	expect := func(prefix string) string {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l := <-lines:
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-deadline:
				t.Fatalf("没等到 %q", prefix)
			}
		}
	}
	expect("event: hello")
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "推送测试"})["id"].(string)
	expect("event: session")
	if !strings.Contains(expect("data:"), "推送测试") {
		t.Fatal("推送内容不对")
	}
	h.ok("POST", "/api/notices", map[string]any{"sessionId": id, "text": "推送通知"})
	expect("event: notice")
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "持久化", "items": []string{"一"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "?"})
	if err := h.srv.store.Flush(); err != nil { // 正常退出时 main 会调 Flush
		t.Fatal(err)
	}
	h2 := newHarness(t, dir)
	if m := h2.ok("GET", "/api/sessions/"+id, nil); m["title"] != "持久化" {
		t.Fatalf("重启后：%v", m)
	}
	if len(h2.notices(id)) != 1 {
		t.Fatal("通知没持久化")
	}
}

func TestProtocolAndPage(t *testing.T) {
	h := newHarness(t, t.TempDir())
	_, _, raw := h.do("GET", "/protocol", nil)
	if !strings.Contains(string(raw), h.base) || strings.Contains(string(raw), "{{BASE}}") {
		t.Fatal("协议文档没替换地址")
	}
	if _, _, raw := h.do("GET", "/", nil); !strings.Contains(string(raw), "<title>Tick") {
		t.Fatal("首页没内嵌")
	}
}

// ---------- MCP ----------

func (h *harness) mcp(session, method string, params any, id int) (map[string]any, string) {
	h.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id > 0 {
		msg["id"] = id
	}
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest("POST", h.base+"/mcp", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m, res.Header.Get("Mcp-Session-Id")
}

func (h *harness) tool(session, name string, args map[string]any) (string, bool) {
	h.t.Helper()
	m, _ := h.mcp(session, "tools/call", map[string]any{"name": name, "arguments": args}, 7)
	r := m["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := r["isError"].(bool)
	return text, isErr
}

func TestRenewedWaitingNotifies(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "claude", "title": "连问两次"})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "第一个问题"})
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "第二个问题"})
	n := h.notices(id)
	if len(n) != 2 || n[0]["text"] != "第二个问题" || n[0]["read"] != false || n[1]["read"] != true {
		t.Fatalf("新问题应生成新通知、旧的标已读：%v", n)
	}
}

func TestLinkAllowlist(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "链接"})["id"].(string)
	for _, l := range []string{"C://Windows/System32/calc.exe", "ms-msdt://x", "search-ms://x", "https://"} {
		if code, _, _ := h.do("PATCH", "/api/sessions/"+id, map[string]any{"link": l}); code != 400 {
			t.Fatalf("应拒绝 %s，得到 %d", l, code)
		}
	}
	for _, l := range []string{"https://example.com/x", "codex://threads/1", "vscode://file/x"} {
		h.ok("PATCH", "/api/sessions/"+id, map[string]any{"link": l})
	}
}

func TestMCPUnknownSession(t *testing.T) {
	h := newHarness(t, t.TempDir())
	b := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", h.base+"/mcp", strings.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "m_stale")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("过期会话应 404，得到 %d", res.StatusCode)
	}
}

func TestParentSession(t *testing.T) {
	h := newHarness(t, t.TempDir())
	p := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "主任务"})["id"].(string)
	c := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro-sub", "title": "子任务", "parentId": p})
	if c["parentId"] != p {
		t.Fatalf("子会话应记下父会话：%v", c["parentId"])
	}
	if code, m, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "x", "title": "y", "parentId": "s_nope"}); code != 400 || !strings.Contains(m["error"].(string), "没有父会话") {
		t.Fatalf("不存在的父会话应 400：%d %v", code, m)
	}
	if code, _, _ := h.do("POST", "/api/sessions", map[string]any{"agent": "x", "title": "孙", "parentId": c["id"]}); code != 400 {
		t.Fatal("只支持一层子任务")
	}
}

// 归档过的会话，agent 又接着报进度：自动移出归档；用户手动归档、结束后的状态变化不受影响
func TestUnarchiveOnActivity(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "归档后又来", "externalId": "conv-1", "items": []string{"一", "二", "三"}})["id"].(string)
	archive := func() {
		if s := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"archived": true}); s["archived"] != true {
			t.Fatal("应能归档")
		}
	}
	for _, c := range []struct {
		name         string
		method, path string
		body         map[string]any
	}{
		{"开始做一条", "PATCH", "/items/1", map[string]any{"status": "doing"}},
		{"报等你", "PATCH", "", map[string]any{"status": "waiting", "note": "?"}},
		{"追加条目", "POST", "/items", map[string]any{"text": "四"}},
		{"重发清单", "PUT", "/items", map[string]any{"items": []string{"一", "二", "三", "四", "五"}}},
	} {
		archive()
		if s := h.ok(c.method, "/api/sessions/"+id+c.path, c.body); s["archived"] != false {
			t.Fatalf("%s 后应自动移出归档：%v", c.name, s["archived"])
		}
	}
	// 同一对话重新登记
	archive()
	if s := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "归档后又来", "externalId": "conv-1"}); s["archived"] != false || s["id"] != id {
		t.Fatalf("重新登记应拿回原会话并移出归档：%v", s)
	}
	// 结束后归档，再补一个结束状态：不该被拉回来
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "done", "summary": "好了"})
	archive()
	if s := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "done", "summary": "改个总结"}); s["archived"] != true {
		t.Fatal("结束状态不应移出归档")
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t, t.TempDir())
	p := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "删我", "items": []string{"一", "二", "三"}})["id"].(string)
	c := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro-sub", "title": "子", "parentId": p})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+p, map[string]any{"status": "waiting", "note": "?"})
	s := h.ok("DELETE", "/api/sessions/"+p+"/items/2", nil)
	got := []string{}
	for _, it := range items(s) {
		got = append(got, it["text"].(string))
	}
	if strings.Join(got, ",") != "一,三" {
		t.Fatalf("删条目：%v", got)
	}
	if code, _, _ := h.do("DELETE", "/api/sessions/"+p+"/items/9", nil); code != 404 {
		t.Fatal("删不存在的条目应 404")
	}
	m := h.ok("DELETE", "/api/sessions/"+p, nil)
	if len(m["deleted"].([]any)) != 2 {
		t.Fatalf("删会话应连同子任务：%v", m)
	}
	for _, id := range []string{p, c} {
		if code, _, _ := h.do("GET", "/api/sessions/"+id, nil); code != 404 {
			t.Fatalf("%s 应已删除", id)
		}
	}
	if len(h.notices(p)) != 0 {
		t.Fatal("相关通知应一起删掉")
	}
	// 清空通知历史：会话还在，通知全没了
	q := h.ok("POST", "/api/sessions", map[string]any{"agent": "kiro", "title": "留着", "items": []string{"一"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+q, map[string]any{"status": "waiting", "note": "?"})
	if got := h.ok("DELETE", "/api/notices", nil); got["deleted"].(float64) < 1 {
		t.Fatalf("清空应删掉至少一条：%v", got)
	}
	if len(h.notices(q)) != 0 {
		t.Fatal("清空后不该还有通知")
	}
	if code, _, _ := h.do("GET", "/api/sessions/"+q, nil); code != 200 {
		t.Fatal("清空通知不该动会话")
	}
}

// 图标：各种状态、各种尺寸都能画出来，大小正确；设了 TICK_ICON_OUT 就把 PNG 写出来人工看
func TestIconRender(t *testing.T) {
	dir := os.Getenv("TICK_ICON_OUT")
	states := map[string]iconState{"app": appIcon, "idle": {Progress: 1}, "p0": {}, "p35": {Progress: 0.35}, "wait": {Progress: 0.6, Tone: toneOrange}, "flash": {Progress: 1, Tone: toneFlash}}
	for name, st := range states {
		for _, sz := range []int{16, 20, 32, 64, 128} {
			b := iconPNG(sz, st)
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil || img.Bounds().Dx() != sz {
				t.Fatalf("%s %d: %v", name, sz, err)
			}
			if dir != "" {
				_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.png", name, sz)), b, 0o644)
			}
		}
	}
	if ico := iconICO(appIcon); len(ico) < 100 || ico[2] != 1 {
		t.Fatal("ICO 头不对")
	}
}

// 自动更新的替换：校验通过才换，旧文件挪成 .old；校验不过、缺资产时原文件一个字节都不动
func TestApplyUpdate(t *testing.T) {
	good := []byte("new tick binary v9")
	sum := sha256.Sum256(good)
	sums := hex.EncodeToString(sum[:]) + "  " + assetName() + "\n"
	body := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin":
			_, _ = w.Write(body)
		case "/sums":
			_, _ = io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rel := &release{Tag: "v9.0.0", Assets: []asset{{assetName(), srv.URL + "/bin"}, {"SHA256SUMS", srv.URL + "/sums"}}}
	dir := t.TempDir()
	exe := filepath.Join(dir, "tick.exe")
	write := func() { _ = os.WriteFile(exe, []byte("old"), 0o755); _ = os.Remove(exe + ".old") }
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	write()
	if err := applyUpdateTo(rel, exe); err != nil {
		t.Fatalf("正常更新失败：%v", err)
	}
	if read(exe) != string(good) || read(exe+".old") != "old" {
		t.Fatalf("替换结果不对：exe=%q old=%q", read(exe), read(exe+".old"))
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal("临时文件没清掉")
	}

	write()
	body = []byte("tampered")
	if err := applyUpdateTo(rel, exe); err == nil || !strings.Contains(err.Error(), "校验不通过") {
		t.Fatalf("校验不过应该报错：%v", err)
	}
	if read(exe) != "old" {
		t.Fatal("校验不过时原文件被动了")
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal("校验不过时临时文件没删")
	}

	write()
	if err := applyUpdateTo(&release{Tag: "v9.0.0", Assets: rel.Assets[1:]}, exe); err == nil || read(exe) != "old" {
		t.Fatalf("缺资产应该报错且不动原文件：%v", err)
	}
	// 版本比较：开发版永远比正式版旧，发布版之间按数字比
	for _, c := range [][3]string{{"v0.5.0", "0.4.1", "1"}, {"v0.4.1", "0.4.1", "0"}, {"v0.4.1", "0.4.1-dev", "1"}, {"v0.4.10", "0.4.9", "1"}, {"v0.4.0", "0.5.0", "0"}} {
		if got := newer(c[0], c[1]); got != (c[2] == "1") {
			t.Fatalf("newer(%s, %s) = %v", c[0], c[1], got)
		}
	}
}

// API 限流（403）时改走网页跳转拿最新 tag；没有发布时网页跳到 /releases
func TestLatestReleaseFallback(t *testing.T) {
	published := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/releases/latest":
			http.Error(w, `{"message":"API rate limit exceeded"}`, 403)
		case r.URL.Path == "/o/r/releases/latest" && published:
			http.Redirect(w, r, "/o/r/releases/tag/v9.9.9", http.StatusFound)
		case r.URL.Path == "/o/r/releases/latest":
			http.Redirect(w, r, "/o/r/releases", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldAPI, oldWeb := githubAPI, githubWeb
	githubAPI, githubWeb = srv.URL, srv.URL
	defer func() { githubAPI, githubWeb = oldAPI, oldWeb }()

	r, err := latestRelease("o/r")
	if err != nil || r.Tag != "v9.9.9" {
		t.Fatalf("限流后应从网页拿到 tag：%v %+v", err, r)
	}
	want := map[string]string{assetName(): srv.URL + "/o/r/releases/download/v9.9.9/" + assetName(), "SHA256SUMS": srv.URL + "/o/r/releases/download/v9.9.9/SHA256SUMS"}
	for _, a := range r.Assets {
		if want[a.Name] != a.URL {
			t.Fatalf("资产地址不对：%s → %s", a.Name, a.URL)
		}
		delete(want, a.Name)
	}
	if len(want) != 0 {
		t.Fatalf("缺资产：%v", want)
	}
	published = false
	if _, err := latestRelease("o/r"); err != errNoRelease {
		t.Fatalf("没有发布时应报 errNoRelease：%v", err)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		remote, local string
		want          bool
	}{
		{"v0.4.1", "0.4.0", true}, {"v0.4.0", "0.4.0", false}, {"v0.3.9", "0.4.0", false},
		{"v1.0.0", "0.9.9", true}, {"v0.4.0", "0.4.0-dev", true}, {"v0.10.0", "0.9.0", true},
	}
	for _, c := range cases {
		if got := newer(c.remote, c.local); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.remote, c.local, got)
		}
	}
}

func TestInstallConfigMerge(t *testing.T) {
	dir := t.TempDir()
	exe := `C:\Program Files\Tick\tick.exe`
	// JSON：保留已有配置，只加 tick；再装一次不重复；卸载只删 tick
	js := filepath.Join(dir, "mcp.json")
	os.WriteFile(js, []byte(`{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`), 0o644)
	if _, err := mergeJSONMCP(js, exe, false, false); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	b, _ := os.ReadFile(js)
	json.Unmarshal(b, &doc)
	servers := doc["mcpServers"].(map[string]any)
	if doc["theme"] != "dark" || servers["other"] == nil || servers["tick"].(map[string]any)["command"] != exe {
		t.Fatalf("合并结果不对：%s", b)
	}
	if msg, _ := mergeJSONMCP(js, exe, false, false); !strings.Contains(msg, "已有") {
		t.Fatalf("重复安装应跳过：%s", msg)
	}
	if baks, _ := filepath.Glob(js + ".bak-tick-*"); len(baks) == 0 {
		t.Fatal("改之前应备份")
	}
	mergeJSONMCP(js, exe, false, true)
	b, _ = os.ReadFile(js)
	if strings.Contains(string(b), "tick") || !strings.Contains(string(b), "other") {
		t.Fatalf("卸载只该删 tick：%s", b)
	}
	// 带注释的 JSON 不改
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{ // comment\n}"), 0o644)
	if _, err := mergeJSONMCP(bad, exe, false, false); err == nil {
		t.Fatal("不是标准 JSON 时不该改")
	}
	// TOML：追加一段、路径转义、卸载删掉这一段但保留别的
	toml := filepath.Join(dir, "config.toml")
	os.WriteFile(toml, []byte("model = \"x\"\n\n[mcp_servers.other]\ncommand = \"y\"\n"), 0o644)
	codexMCP(toml, exe, false, false)
	b, _ = os.ReadFile(toml)
	if !strings.Contains(string(b), `command = "C:\\Program Files\\Tick\\tick.exe"`) {
		t.Fatalf("TOML 路径应转义：%s", b)
	}
	codexMCP(toml, exe, false, true)
	b, _ = os.ReadFile(toml)
	if strings.Contains(string(b), "mcp_servers.tick") || !strings.Contains(string(b), "mcp_servers.other") || !strings.Contains(string(b), `model = "x"`) {
		t.Fatalf("TOML 卸载结果不对：%s", b)
	}
}

func TestMCPFlow(t *testing.T) {
	h := newHarness(t, t.TempDir())
	init, sess := h.mcp("", "initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]string{"name": "claude-code"}}, 1)
	res := init["result"].(map[string]any)
	if sess == "" || res["protocolVersion"] != "2025-06-18" || !strings.Contains(res["instructions"].(string), "tick_start") {
		t.Fatalf("initialize：%v %s", init, sess)
	}
	if m, _ := h.mcp(sess, "notifications/initialized", nil, 0); m != nil {
		t.Fatal("通知类消息不该有回复")
	}
	list, _ := h.mcp(sess, "tools/list", nil, 2)
	names := []string{}
	for _, x := range list["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "tick_start,tick_plan,tick_item,tick_notify,tick_finish,tick_show" {
		t.Fatalf("工具列表：%v", names)
	}
	if m, _ := h.mcp(sess, "foo/bar", nil, 3); num(m["error"].(map[string]any)["code"]) != -32601 {
		t.Fatal("未知方法应 -32601")
	}

	text, isErr := h.tool(sess, "tick_start", map[string]any{"title": "MCP 流程", "items": []string{"一", "二"}, "conversation_id": "conv-1"})
	id := regexp.MustCompile(`session_id: (s_\w+)`).FindStringSubmatch(text)
	if isErr || id == nil {
		t.Fatalf("tick_start：%s", text)
	}
	if s := h.ok("GET", "/api/sessions/"+id[1], nil); s["agent"] != "claude" {
		t.Fatalf("应从 clientInfo 推断 agent：%v", s["agent"])
	}
	if text, _ := h.tool(sess, "tick_item", map[string]any{"n": 1, "status": "doing"}); !strings.Contains(text, "#1 一 → doing") {
		t.Fatal(text)
	}
	if text, _ := h.tool(sess, "tick_item", map[string]any{"n": 1, "status": "done", "note": "好了"}); !strings.Contains(text, "下一条：#2 二") {
		t.Fatal(text)
	}
	if text, _ := h.tool(sess, "tick_notify", map[string]any{"kind": "waiting", "message": "要推送吗？"}); !strings.Contains(text, "等你") {
		t.Fatal(text)
	}
	h.tool(sess, "tick_notify", map[string]any{"message": "顺便一提", "kind": "success"})
	if text, _ := h.tool(sess, "tick_finish", map[string]any{"summary": "完成了"}); !strings.Contains(text, "完成") {
		t.Fatal(text)
	}
	kinds := []string{}
	for _, n := range h.notices(id[1]) {
		kinds = append(kinds, n["kind"].(string))
	}
	if strings.Join(kinds, ",") != "done,success,waiting" {
		t.Fatalf("通知顺序：%v", kinds)
	}
	if text, _ := h.tool(sess, "tick_start", map[string]any{"title": "MCP 流程", "conversation_id": "conv-1"}); !strings.Contains(text, id[1]+"（拿回了原会话）") {
		t.Fatal(text)
	}
	if _, isErr := h.tool(sess, "tick_item", map[string]any{"n": 0, "status": "done"}); !isErr {
		t.Fatal("n=0 应返回 isError")
	}
	if text, isErr := h.tool("", "tick_show", map[string]any{}); !isErr || !strings.Contains(text, "tick_start") {
		t.Fatalf("没有会话时应提示先 tick_start：%s", text)
	}
}
