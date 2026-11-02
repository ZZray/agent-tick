package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenameHistoryAndPersistence(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	id := h.ok("POST", "/api/sessions", map[string]any{
		"agent": "codex", "externalId": "title-thread", "title": "了解 Tick 项目", "items": []string{"核对标题"}, "link": "codex://threads/title-thread",
	})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "doing"})
	h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "waiting", "note": "等原生验证"})
	before, _ := h.srv.store.Session(id)
	for _, title := range []string{"Tick · 标题优化", "Tick · 标题优化", "了解 Tick 项目", "Tick · 展开优化"} {
		h.ok("PATCH", "/api/sessions/"+id, map[string]any{"title": title})
	}
	after, _ := h.srv.store.Session(id)
	want := []string{"了解 Tick 项目", "Tick · 标题优化"}
	if !reflect.DeepEqual(after.TitleHistory, want) || after.Title != "Tick · 展开优化" {
		t.Fatalf("标题或去重旧名不符：%+v", after)
	}
	if after.ID != before.ID || after.Link != before.Link || after.ExternalID != before.ExternalID || after.Status != before.Status || after.StatusNote != before.StatusNote || after.WaitSince != before.WaitSince || !reflect.DeepEqual(after.Items, before.Items) {
		t.Fatalf("改名改变了会话身份、状态或清单：before=%+v after=%+v", before, after)
	}
	if len(h.notices(id)) != 1 {
		t.Fatal("改名不应新增状态通知")
	}
	after.TitleHistory[0] = "不应写回"
	stored, _ := h.srv.store.Session(id)
	if !reflect.DeepEqual(stored.TitleHistory, want) {
		t.Fatal("返回值的旧名切片没有独立复制")
	}
	for i := 0; i <= maxEvents; i++ {
		h.ok("POST", "/api/sessions/"+id+"/log", map[string]any{"text": "后续动态"})
	}
	if err := h.srv.store.Flush(); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, dir)
	reloaded, _ := h2.srv.store.Session(id)
	if !reflect.DeepEqual(reloaded.TitleHistory, want) || reloaded.Title != stored.Title {
		t.Fatalf("动态裁剪或重启丢失旧名：%+v", reloaded)
	}
	for _, event := range reloaded.Events {
		if event.Type == "rename" {
			t.Fatal("用例应先把改名动态挤出历史，再独立验证旧名")
		}
	}
}

func TestRenameLegacySessionAndValidation(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":2,"sessions":[{"id":"s_legacy","agent":"codex","title":"旧版任务","status":"done","items":[],"events":[]}],"notices":[]}`
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, dir)
	for _, invalid := range []string{"", "  ", strings.Repeat("长", maxTitle+1)} {
		if code, _, _ := h.do("PATCH", "/api/sessions/s_legacy", map[string]any{"title": invalid}); code != 400 {
			t.Fatalf("应拒绝无效标题，得到%d", code)
		}
	}
	s := h.ok("GET", "/api/sessions/s_legacy", nil)
	if s["title"] != "旧版任务" || s["titleHistory"] != nil {
		t.Fatalf("无效改名不应改变旧数据：%v", s)
	}
	s = h.ok("PATCH", "/api/sessions/s_legacy", map[string]any{"title": " Tick · 新目标 "})
	if s["title"] != "Tick · 新目标" || s["status"] != "done" || !reflect.DeepEqual(s["titleHistory"], []any{"旧版任务"}) {
		t.Fatalf("旧数据首次改名应保留原名及状态：%v", s)
	}
}

func TestMCPAndCLIRename(t *testing.T) {
	h := newHarness(t, t.TempDir())
	_, connection := h.mcp("", "initialize", map[string]any{"clientInfo": map[string]string{"name": "codex"}}, 1)
	_, isErr := h.tool(connection, "tick_start", map[string]any{"title": "Tick · 初始目标", "conversation_id": "rename-thread"})
	if isErr {
		t.Fatal("登记失败")
	}
	for _, args := range []map[string]any{{}, {"title": " "}, {"title": 123}, {"title": strings.Repeat("长", maxTitle+1)}} {
		if _, failed := h.tool(connection, "tick_rename", args); !failed {
			t.Fatalf("应拒绝无效改名参数：%v", args)
		}
	}
	if text, failed := h.tool(connection, "tick_rename", map[string]any{"title": "Tick · 当前目标"}); failed || !strings.Contains(text, "Tick · 当前目标") {
		t.Fatalf("MCP改名失败：%s", text)
	}
	text, failed := h.tool(connection, "tick_show", nil)
	if failed || !strings.Contains(text, "Tick · 当前目标") {
		t.Fatal(text)
	}
	var id string
	for _, session := range h.srv.store.Sessions(false) {
		id = session.ID
	}
	if runCLI(h.srv.port, []string{"rename", id, "Tick · CLI标题"}) != 0 {
		t.Fatal("CLI改名失败")
	}
	if runCLI(h.srv.port, []string{"rename", id}) != 1 {
		t.Fatal("CLI应拒绝缺失标题")
	}
	s, _ := h.srv.store.Session(id)
	if s.Title != "Tick · CLI标题" || !reflect.DeepEqual(s.TitleHistory, []string{"Tick · 初始目标", "Tick · 当前目标"}) {
		t.Fatalf("MCP/CLI未共用改名链路：%+v", s)
	}
	if _, failed := h.tool("", "tick_rename", map[string]any{"session_id": id, "title": "Tick · 显式会话"}); failed {
		t.Fatal("显式session_id改名失败")
	}
	if _, failed := h.tool("", "tick_rename", map[string]any{"title": "没有会话"}); !failed {
		t.Fatal("没有最近会话时应要求session_id")
	}
}
