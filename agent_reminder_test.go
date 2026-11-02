package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reminderSession(t *testing.T, st *Store, id string, at int64) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sessions[id] = &Session{ID: id, Title: id, Agent: "codex", Status: "running", CreatedAt: at - time.Hour.Milliseconds(), UpdatedAt: at, Items: []*Item{
		{ID: "i1", Text: "long step", Status: "doing", StartedAt: at - time.Hour.Milliseconds(), UpdatedAt: at - agentReminderDelay.Milliseconds()},
		{ID: "i2", Text: "other step", Status: "todo", UpdatedAt: at},
	}}
}

func TestAgentReminderBoundaryCooldownAndCurrentSessionOnly(t *testing.T) {
	h := newHarness(t, t.TempDir())
	st := h.srv.store
	at := now()
	reminderSession(t, st, "one", at)
	reminderSession(t, st, "two", at)
	before, _ := st.Session("one")
	if got := st.AgentReminder("one", false, at-1); got != "" {
		t.Fatalf("under30min reminded: %s", got)
	}
	got := st.AgentReminder("one", false, at)
	if !strings.Contains(got, "id=i1") || !strings.Contains(got, "[doing]") || !strings.Contains(got, "30 分钟") || strings.Contains(got, "two") {
		t.Fatalf("incorrect current-session reminder: %s", got)
	}
	if again := st.AgentReminder("one", false, at+agentReminderDelay.Milliseconds()-1); again != "" {
		t.Fatal("same problem not throttled")
	}
	if again := st.AgentReminder("one", false, at+agentReminderDelay.Milliseconds()); again == "" {
		t.Fatal("30min cooldown never expires")
	}
	if other := st.AgentReminder("two", false, at); other == "" {
		t.Fatal("another session reminder was consumed by first request")
	}
	after, _ := st.Session("one")
	if !reflect.DeepEqual(before, after) || st.UnreadNotices() != 0 {
		t.Fatal("reminder mutated state, events, timing or notices")
	}
}

func TestAgentReminderProtectsPausedWaitingAndUnknownOldTiming(t *testing.T) {
	h := newHarness(t, t.TempDir())
	st := h.srv.store
	at := now()
	for _, status := range []string{"paused", "waiting", "blocked", "done", "failed", "cancelled"} {
		reminderSession(t, st, "one", at)
		st.sessions["one"].Status = status
		if got := st.AgentReminder("one", false, at); got != "" {
			t.Fatalf("%s task nagged: %s", status, got)
		}
	}
	reminderSession(t, st, "one", at)
	it := st.sessions["one"].Items[0]
	it.UpdatedAt = 0
	it.StartedAt = 0
	if got := st.AgentReminder("one", false, at); got != "" {
		t.Fatal("unknown old timestamp fabricated stale duration")
	}
	it.StartedAt = at - agentReminderDelay.Milliseconds()
	if got := st.AgentReminder("one", false, at); got == "" {
		t.Fatal("legacy startedAt fallback missing")
	}
}

func TestAgentReminderUpdatesAndNoopsHaveDistinctSemantics(t *testing.T) {
	h := newHarness(t, t.TempDir())
	st := h.srv.store
	at := now()
	reminderSession(t, st, "one", at)
	original := st.sessions["one"].Items[0].UpdatedAt
	h.ok("PATCH", "/api/sessions/one", map[string]any{"title": "renamed session"})
	h.ok("PATCH", "/api/sessions/one/items/i2", map[string]any{"note": "updated another item"})
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"text": "long step"})
	h.ok("PUT", "/api/sessions/one/items", map[string]any{"items": []any{map[string]any{"id": "i1", "text": "long step"}, map[string]any{"id": "i2", "text": "other step"}}})
	if actual := st.sessions["one"].Items[0].UpdatedAt; actual != original {
		t.Fatal("session/other/noop/replan masked old item")
	}
	if got := st.AgentReminder("one", false, at); got == "" {
		t.Fatal("unchanged old item not reminded")
	}
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"status": "doing"})
	if got := st.AgentReminder("one", false, now()); got != "" {
		t.Fatal("explicit still-doing confirmation did not clear stale reminder")
	}
	st.sessions["one"].Items[0].UpdatedAt = original
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"evidence": "runs/new-evidence.json"})
	if actual := st.sessions["one"].Items[0].UpdatedAt; actual <= original {
		t.Fatal("actual item evidence update missing timestamp")
	}
	if got := st.AgentReminder("one", false, now()); got != "" {
		t.Fatal("actual item progress did not clear stale reminder")
	}
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"status": "done"})
	if got := st.AgentReminder("one", false, at+2*time.Hour.Milliseconds()); got != "" {
		t.Fatal("ended item still reminded")
	}
}

func TestAgentReminderMCPResponseAndFinishDoNotInventCompletion(t *testing.T) {
	h := newHarness(t, t.TempDir())
	at := now()
	reminderSession(t, h.srv.store, "one", at)
	_, conn := h.mcp("", "initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]string{"name": "codex"}}, 1)
	text, isErr := h.tool(conn, "tick_show", map[string]any{"session_id": "one"})
	if isErr || !strings.Contains(text, "Tick 提醒：") || !strings.Contains(text, "id=i1") {
		t.Fatalf("missing MCP response reminder: %s", text)
	}
	text, isErr = h.tool(conn, "tick_show", map[string]any{"session_id": "one"})
	if isErr || strings.Contains(text, "Tick 提醒：") {
		t.Fatal("MCP reminder not throttled")
	}
	text, isErr = h.tool(conn, "tick_finish", map[string]any{"session_id": "one", "summary": "execution round ended"})
	if isErr || !strings.Contains(text, "Tick 收尾核对") || !strings.Contains(text, "2 条未结束项") {
		t.Fatalf("finish reminder missing or finish rejected: %s", text)
	}
	s, _ := h.srv.store.Session("one")
	if s.Status != "done" || s.Items[0].Status != "doing" || s.Items[1].Status != "todo" || s.Items[0].Result != "" {
		t.Fatal("finish reminder invented status or result")
	}
	text, isErr = h.tool(conn, "tick_finish", map[string]any{"session_id": "one", "summary": "failed round", "failed": true})
	if isErr || strings.Contains(text, "Tick 收尾核对") {
		t.Fatal("explicit failed finish was urged to complete items")
	}
}

func TestAgentReminderHTTPIsOptInAndKeepsJSONShape(t *testing.T) {
	h := newHarness(t, t.TempDir())
	at := now()
	reminderSession(t, h.srv.store, "one", at)
	request := func(agent, user bool) string {
		req, _ := http.NewRequest(http.MethodGet, h.base+"/api/sessions/one", nil)
		if agent {
			req.Header.Set("X-Tick-Agent-Request", "1")
		}
		if user {
			req.Header.Set("X-Tick-User", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if !strings.Contains(string(body), `"id":"one"`) || strings.Contains(string(body), "Tick 提醒") {
			t.Fatal("HTTP reminder changed existing session JSON")
		}
		value, _ := url.QueryUnescape(res.Header.Get("X-Tick-Reminder"))
		return value
	}
	if request(false, false) != "" || request(true, true) != "" {
		t.Fatal("ordinary UI reads emitted reminder")
	}
	if got := request(true, false); !strings.Contains(got, "id=i1") {
		t.Fatalf("agent header reminder missing: %s", got)
	}
	if request(true, false) != "" {
		t.Fatal("HTTP reminder not throttled")
	}
}

func TestAgentReminderHTTPFinishIntentIsNotInferredFromEndedState(t *testing.T) {
	h := newHarness(t, t.TempDir())
	at := now()
	reminderSession(t, h.srv.store, "one", at)
	request := func(body string) string {
		req, _ := http.NewRequest(http.MethodPatch, h.base+"/api/sessions/one", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tick-Agent-Request", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("PATCH failed:%d", res.StatusCode)
		}
		value, _ := url.QueryUnescape(res.Header.Get("X-Tick-Reminder"))
		return value
	}
	h.srv.store.sessions["one"].Status = "done"
	if got := request(`{"title":"renamed ended session"}`); got != "" {
		t.Fatalf("rename treated as finish:%s", got)
	}
	if got := request(`{"status":"done","summary":"round ended"}`); !strings.Contains(got, "Tick 收尾核对") {
		t.Fatalf("actual finish missing reminder:%s", got)
	}
}

func TestAgentReminderHTTPAttachStartKeepsShapeAndIncludesReminder(t *testing.T) {
	h := newHarness(t, t.TempDir())
	created := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "existing", "externalId": "reminder-attach", "items": []string{"long step"}})
	id := created["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"status": "doing"})
	h.srv.store.mu.Lock()
	h.srv.store.sessions[id].Items[0].UpdatedAt = now() - time.Hour.Milliseconds()
	h.srv.store.mu.Unlock()
	req, _ := http.NewRequest(http.MethodPost, h.base+"/api/sessions", strings.NewReader(`{"agent":"codex","title":"existing","externalId":"reminder-attach"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tick-Agent-Request", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	reminder, _ := url.QueryUnescape(res.Header.Get("X-Tick-Reminder"))
	if res.StatusCode != 200 || !strings.Contains(string(body), `"attached":true`) || !strings.Contains(reminder, "id=i1") {
		t.Fatalf("attach reminder or existing JSON shape lost: %d %s %s", res.StatusCode, body, reminder)
	}
}

func TestAgentReminderResumeOnlyConfirmsTheRequestedItem(t *testing.T) {
	h := newHarness(t, t.TempDir())
	st := h.srv.store
	at := now()
	reminderSession(t, st, "one", at)
	old := at - time.Hour.Milliseconds()
	st.sessions["one"].Status = "paused"
	for _, it := range st.sessions["one"].Items {
		it.Status = "doing"
		it.StartedAt = 0
		it.UpdatedAt = old
	}
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"status": "doing"})
	s, _ := st.Session("one")
	if s.Items[1].StartedAt <= old || s.Items[1].UpdatedAt != old {
		t.Fatal("setup or single-item update contract wrong")
	}
	got := st.AgentReminder("one", false, now())
	if !strings.Contains(got, "id=i2") || strings.Contains(got, "id=i1") {
		t.Fatalf("session timer resume masked the other stale item: %s", got)
	}
}

func TestAgentReminderBoundedHeaderAndPerItemCooldown(t *testing.T) {
	h := newHarness(t, t.TempDir())
	st := h.srv.store
	at := now()
	reminderSession(t, st, "one", at)
	st.sessions["one"].Items[1].Status = "doing"
	st.sessions["one"].Items[1].UpdatedAt = at - agentReminderDelay.Milliseconds()
	first := st.AgentReminder("one", false, at)
	if !strings.Contains(first, "id=i1") || !strings.Contains(first, "id=i2") {
		t.Fatal("two stale items not reported")
	}
	h.ok("PATCH", "/api/sessions/one/items/i1", map[string]any{"status": "done"})
	if got := st.AgentReminder("one", false, at+time.Minute.Milliseconds()); got != "" {
		t.Fatalf("updating another item bypassed the remaining item's cooldown: %s", got)
	}
	st.mu.Lock()
	st.agentReminders = nil
	for i := 3; i <= 219; i++ {
		st.sessions["one"].Items = append(st.sessions["one"].Items, &Item{ID: fmt.Sprintf("i%d", i), Status: "doing", Text: strings.Repeat("验", 80), UpdatedAt: at - agentReminderDelay.Milliseconds()})
	}
	st.mu.Unlock()
	got := st.AgentReminder("one", false, at)
	if strings.Count(got, "(id=") != 3 || !strings.Contains(got, "另有 215 条") {
		t.Fatalf("header list not bounded with accurate remainder: %s", got)
	}
	if len(url.QueryEscape(got)) > 8000 {
		t.Fatal("encoded reminder exceeded a normal HTTP header budget")
	}
}
