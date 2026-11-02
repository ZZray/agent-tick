package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func rawPlan(t *testing.T, values any) []json.RawMessage {
	t.Helper()
	b, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExplicitItemResultsRemainIndependentFromExecution(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "result semantics", "items": []string{"ordinary", "verification", "skipped"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/1", map[string]any{"status": "done"})
	s := h.ok("PATCH", "/api/sessions/"+id+"/items/i2", map[string]any{"status": "done", "result": "failed", "evidence": "runs/first.json"})
	its := items(s)
	if _, exists := its[0]["result"]; exists {
		t.Fatal("ordinary done was treated as passed")
	}
	if its[1]["result"] != "failed" || its[1]["status"] != "done" {
		t.Fatalf("execution and result conflated: %v", its[1])
	}
	h.ok("PATCH", "/api/sessions/"+id+"/items/i3", map[string]any{"status": "skipped"})
	for _, result := range []string{"unknown", "unverified", "passed"} {
		s = h.ok("PATCH", "/api/sessions/"+id+"/items/i2", map[string]any{"result": result, "evidence": "runs/" + result + ".json"})
		if items(s)[1]["result"] != result || items(s)[1]["status"] != "done" {
			t.Fatal("result-only update changed execution")
		}
	}
	for _, body := range []map[string]any{{}, {"result": "success"}, {"result": ""}, {"evidence": strings.Repeat("x", maxNote+1)}} {
		if code, _, _ := h.do("PATCH", "/api/sessions/"+id+"/items/i2", body); code != 400 {
			t.Fatalf("invalid result update accepted: %v %d", body, code)
		}
	}
	if err := h.srv.store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Session(id)
	if saved.Items[1].Result != "passed" || saved.Items[1].Evidence != "runs/passed.json" || saved.Items[2].Result != "" {
		t.Fatalf("wrong saved results: %+v", saved.Items)
	}
}

func TestStablePlanIDsPreserveResultsAndTimingAcrossRenameMove(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "stable IDs", "items": []string{"same", "other"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"status": "doing", "result": "unknown", "evidence": "runs/one"})
	h.srv.store.mu.Lock()
	h.srv.store.sessions[id].Items[0].SpentMs = 4321
	h.srv.store.mu.Unlock()
	before, _ := h.srv.store.Session(id)
	s := h.ok("PUT", "/api/sessions/"+id+"/items", map[string]any{"items": []any{"same", map[string]any{"id": "i1", "text": "renamed", "group": "API / module"}, map[string]any{"id": "i2", "text": "other"}}})
	its := items(s)
	if its[0]["id"] == "i1" {
		t.Fatal("text match consumed explicitly reserved ID")
	}
	if its[1]["id"] != "i1" || its[1]["text"] != "renamed" || its[1]["group"] != "API / module" || its[1]["result"] != "unknown" || its[1]["evidence"] != "runs/one" || num(its[1]["spentMs"]) != 4321 || int64(num(its[1]["startedAt"])) != before.Items[0].StartedAt {
		t.Fatalf("ID move lost original item: %v", its[1])
	}
	baseline, _ := h.srv.store.Session(id)
	for _, plan := range []any{[]any{map[string]any{"id": "unknown", "text": "bad"}}, []any{map[string]any{"id": "i1", "text": "a"}, map[string]any{"id": "i1", "text": "b"}}} {
		if code, _, _ := h.do("PUT", "/api/sessions/"+id+"/items", map[string]any{"items": plan}); code != 400 {
			t.Fatalf("invalid ID plan accepted: %d", code)
		}
		after, _ := h.srv.store.Session(id)
		if !reflect.DeepEqual(after.Items, baseline.Items) {
			t.Fatal("invalid ID update partially mutated plan")
		}
	}
	if code, _, _ := h.do("POST", "/api/sessions/"+id+"/items", map[string]any{"items": []any{map[string]any{"id": "i1", "text": "bad append"}}}); code != 400 {
		t.Fatal("append must not reuse stable IDs")
	}
}

func TestLargePlan219MethodsAndCapacity1000(t *testing.T) {
	h := newHarness(t, t.TempDir())
	values := []any{}
	for i := 0; i < 219; i++ {
		if i%20 == 0 {
			values = append(values, fmt.Sprintf("# API / module%d", i/20+1))
		}
		values = append(values, fmt.Sprintf("method%d", i))
	}
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "219 APIs", "items": values})["id"].(string)
	s, _ := h.srv.store.Session(id)
	if len(s.Items) != 219 {
		t.Fatalf("219 unique methods not retained: %d", len(s.Items))
	}
	ids := map[string]bool{}
	for _, it := range s.Items {
		if ids[it.ID] {
			t.Fatal("duplicate IDs")
		}
		ids[it.ID] = true
	}
	appendValues := make([]string, 781)
	for i := range appendValues {
		appendValues[i] = fmt.Sprintf("extra%d", i)
	}
	h.ok("POST", "/api/sessions/"+id+"/items", map[string]any{"items": appendValues})
	if code, _, _ := h.do("POST", "/api/sessions/"+id+"/items", map[string]any{"text": "overflow"}); code != 400 {
		t.Fatal("1000 item bound missing")
	}
	s, _ = h.srv.store.Session(id)
	if len(s.Items) != 1000 || s.Items[218].ID != "i219" {
		t.Fatal("append lost existing stable methods")
	}
}

func TestPausedStopsItemTimingAndMetadataDoesNotResume(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "pause", "items": []string{"step"}})["id"].(string)
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"status": "doing"})
	time.Sleep(5 * time.Millisecond)
	s := h.ok("PATCH", "/api/sessions/"+id, map[string]any{"status": "paused", "note": "next time"})
	spent := num(items(s)[0]["spentMs"])
	if s["status"] != "paused" || num(items(s)[0]["startedAt"]) != 0 || num(s["waitSince"]) != 0 {
		t.Fatalf("pause did not stop timer independently: %v", s)
	}
	for _, body := range []map[string]any{{"result": "unknown"}, {"evidence": "runs/paused"}, {"note": "retained"}, {"text": "renamed"}} {
		s = h.ok("PATCH", "/api/sessions/"+id+"/items/i1", body)
		if s["status"] != "paused" || num(items(s)[0]["startedAt"]) != 0 || num(items(s)[0]["spentMs"]) != spent {
			t.Fatal("metadata-only update resumed timer")
		}
	}
	s = h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"status": "doing"})
	if s["status"] != "running" || num(items(s)[0]["startedAt"]) == 0 {
		t.Fatal("explicit doing did not resume paused task")
	}
	for _, n := range h.notices(id) {
		if n["kind"] == "waiting" {
			t.Fatal("pause created waiting-for-user notice")
		}
	}
}

func TestMCPStableItemsResultsAndPause(t *testing.T) {
	h := newHarness(t, t.TempDir())
	_, conn := h.mcp("", "initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]string{"name": "codex"}}, 1)
	if text, isErr := h.tool(conn, "tick_start", map[string]any{"title": "MCP result", "items": []any{map[string]any{"text": "check", "group": "API / one"}}}); isErr {
		t.Fatal(text)
	}
	for _, args := range []map[string]any{{"item_id": "i1", "status": "done", "result": "failed", "evidence": "runs/fail"}, {"item_id": "i1", "result": "passed", "evidence": "runs/pass"}} {
		text, isErr := h.tool(conn, "tick_item", args)
		if isErr || !strings.Contains(text, "result=") {
			t.Fatalf("MCP stable update: %s", text)
		}
	}
	if text, isErr := h.tool(conn, "tick_pause", map[string]any{"note": "continue tomorrow"}); isErr || !strings.Contains(text, "待续") {
		t.Fatalf("pause: %s", text)
	}
	for _, args := range []map[string]any{{"item_id": "i1"}, {"n": 1, "item_id": "i1", "result": "passed"}, {"item_id": "i1", "result": 3}, {"item_id": "missing", "result": "passed"}, {"item_id": "1", "result": "failed"}} {
		if text, isErr := h.tool(conn, "tick_item", args); !isErr {
			t.Fatalf("invalid MCP update accepted: %s", text)
		}
	}
	actual := h.srv.store.Sessions(false)[0]
	if actual.Items[0].Result != "passed" || actual.Status != "paused" {
		t.Fatal("invalid item_id caused a positional mutation")
	}
}

func TestResultHistoryIsExplicitIdempotentAndCloneIsolated(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "history", "items": []string{"check"}})["id"].(string)
	first := map[string]any{"status": "done", "result": "failed", "evidence": "runs/failed.json", "note": "first failure"}
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", first)
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", first)
	s, _ := h.srv.store.Session(id)
	if len(s.Items[0].ResultHistory) != 1 {
		t.Fatal("identical result report created a duplicate history entry")
	}
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"result": "passed", "evidence": "runs/passed.json", "note": "verified fix"})
	h.ok("PATCH", "/api/sessions/"+id+"/items/i1", map[string]any{"evidence": "runs/metadata-only"})
	s, _ = h.srv.store.Session(id)
	history := s.Items[0].ResultHistory
	if len(history) != 2 || history[0].Result != "failed" || history[0].Evidence != "runs/failed.json" || history[1].Result != "passed" || history[1].Evidence != "runs/passed.json" || history[0].At == 0 {
		t.Fatalf("history lost: %+v", history)
	}
	s.Items[0].ResultHistory[0].Evidence = "tampered"
	actual, _ := h.srv.store.Session(id)
	if actual.Items[0].ResultHistory[0].Evidence != "runs/failed.json" {
		t.Fatal("read snapshot aliases stored history")
	}
	h.ok("PUT", "/api/sessions/"+id+"/items", map[string]any{"items": []any{map[string]any{"id": "i1", "text": "renamed", "group": "API / current"}}})
	if err := h.srv.store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ = reopened.Session(id)
	if len(actual.Items[0].ResultHistory) != 2 || actual.Items[0].Text != "renamed" || actual.Items[0].ResultHistory[0].Evidence != "runs/failed.json" {
		t.Fatal("rename or persistence lost history")
	}
	item := &Item{Status: "done"}
	for i := 0; i < maxEvents+2; i++ {
		item.Evidence = fmt.Sprintf("runs/%d", i)
		reportItemResult(item, "passed")
	}
	if len(item.ResultHistory) != maxEvents+2 || item.ResultHistory[0].Evidence != "runs/0" {
		t.Fatal("result history was silently truncated like transient events")
	}
}

func TestCLIResultEvidenceAndPause(t *testing.T) {
	h := newHarness(t, t.TempDir())
	id := h.ok("POST", "/api/sessions", map[string]any{"agent": "codex", "title": "CLI result", "items": []string{"check"}})["id"].(string)
	if code := runCLI(h.srv.port, []string{"done", id, "i1", "--result", "failed", "--evidence", "runs/cli.json"}); code != 0 {
		t.Fatalf("CLI result failed: %d", code)
	}
	s, _ := h.srv.store.Session(id)
	if s.Items[0].Result != "failed" || s.Items[0].Evidence != "runs/cli.json" {
		t.Fatal("CLI did not transmit evidence/result")
	}
	if code := runCLI(h.srv.port, []string{"pause", id, "--note", "待续"}); code != 0 {
		t.Fatal("CLI pause failed")
	}
	s, _ = h.srv.store.Session(id)
	if s.Status != "paused" {
		t.Fatal("CLI pause not applied")
	}
}
