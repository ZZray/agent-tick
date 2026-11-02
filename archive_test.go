package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func archiveFixture(t *testing.T, st *Store) int64 {
	t.Helper()
	at := now()
	old := at - (2 * time.Hour).Milliseconds()
	st.mu.Lock()
	st.sessions["p"] = &Session{ID: "p", Agent: "codex", Status: "done", CreatedAt: old, UpdatedAt: old, EndedAt: old, Items: []*Item{{ID: "i1", Text: "parent", Status: "done"}}}
	st.sessions["c"] = &Session{ID: "c", ParentID: "p", Agent: "codex", Status: "running", CreatedAt: old + 1, UpdatedAt: old, Items: []*Item{{ID: "i1", Text: "child", Status: "doing"}}}
	st.sessions["g"] = &Session{ID: "g", ParentID: "c", Agent: "codex", Status: "done", CreatedAt: old + 2, UpdatedAt: old, EndedAt: old}
	st.sessions["other"] = &Session{ID: "other", Agent: "codex", Status: "running", CreatedAt: old, UpdatedAt: old}
	st.mu.Unlock()
	return at
}

func TestArchiveTreeAPIAndRestore(t *testing.T) {
	h := newHarness(t, t.TempDir())
	archiveFixture(t, h.srv.store)
	ch, cancel := h.srv.store.Subscribe()
	defer cancel()
	m := h.ok("POST", "/api/sessions/p/archive", map[string]any{"archived": true})
	list := m["sessions"].([]any)
	if len(list) != 3 {
		t.Fatalf("whole descendant tree missing: %v", m)
	}
	if len(m["changedIds"].([]any)) != 3 {
		t.Fatalf("missing actually changed IDs: %v", m)
	}
	select {
	case frame := <-ch:
		var pushed []*Session
		if frame.Event != "sessions" || json.Unmarshal(frame.Data, &pushed) != nil || len(pushed) != 3 {
			t.Fatalf("wrong batch SSE: %+v", frame)
		}
		for _, s := range pushed {
			if !s.Archived {
				t.Fatalf("batch included stale state: %+v", s)
			}
		}
	default:
		t.Fatal("missing archive batch SSE")
	}
	for _, id := range []string{"p", "c", "g"} {
		s, _ := h.srv.store.Session(id)
		if !s.Archived {
			t.Fatalf("%s not archived", id)
		}
	}
	c, _ := h.srv.store.Session("c")
	if c.Status != "running" || c.Items[0].Status != "doing" {
		t.Fatal("archive changed execution status")
	}
	other, _ := h.srv.store.Session("other")
	if other.Archived {
		t.Fatal("unrelated root was archived")
	}
	h.ok("POST", "/api/sessions/p/archive", map[string]any{"archived": false})
	for _, id := range []string{"p", "c", "g"} {
		s, _ := h.srv.store.Session(id)
		if s.Archived {
			t.Fatalf("%s not restored", id)
		}
	}
	if err := h.srv.store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Session("c")
	if saved.Archived || saved.Status != "running" || saved.Items[0].Text != "child" {
		t.Fatalf("wrong persisted session: %+v", saved)
	}
}

func TestArchiveTreeValidationAndMissingRoot(t *testing.T) {
	h := newHarness(t, t.TempDir())
	archiveFixture(t, h.srv.store)
	if code, _, _ := h.do("POST", "/api/sessions/p/archive", nil); code != 415 {
		t.Fatalf("missing JSON content type: %d", code)
	}
	for _, body := range []any{map[string]any{}, map[string]any{"archived": "true"}, map[string]any{"archived": nil}} {
		if code, _, _ := h.do("POST", "/api/sessions/p/archive", body); code != 400 {
			t.Fatalf("invalid body accepted: %v => %d", body, code)
		}
	}
	if code, _, _ := h.do("POST", "/api/sessions/missing/archive", map[string]any{"archived": true}); code != 404 {
		t.Fatalf("missing root: %d", code)
	}
	if code, _, _ := h.do("POST", "/api/sessions/p/archive", map[string]any{"archived": true}, "Origin", "https://elsewhere.example"); code != 403 {
		t.Fatalf("missing Origin guard: %d", code)
	}
	p, _ := h.srv.store.Session("p")
	if p.Archived {
		t.Fatal("failed request changed group")
	}
}

func TestArchiveTreeIdempotenceAndProgressRecovery(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() })
	archiveFixture(t, st)
	before, _ := st.Session("c")
	if _, err := st.ArchiveTree("p", true); err != nil {
		t.Fatal(err)
	}
	first, _ := st.Session("c")
	if first.UpdatedAt != before.UpdatedAt {
		t.Fatal("archive falsified last progress time")
	}
	if _, err := st.ArchiveTree("p", true); err != nil {
		t.Fatal(err)
	}
	again, _ := st.Session("c")
	if len(again.Events) != len(first.Events) {
		t.Fatal("repeated archive added duplicate events")
	}
	status := "doing"
	recovered, err := st.PatchItem("c", "i1", ItemPatch{Status: &status})
	if err != nil || recovered.Archived || recovered.Status != "running" {
		t.Fatalf("new progress must recover archived child: %+v %v", recovered, err)
	}
	parent, _ := st.Session("p")
	if !parent.Archived {
		t.Fatal("child progress should not reopen an ended parent")
	}
}

func TestAutoArchiveProtectsCurrentGroups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Store, int64, map[string]int64)
	}{
		{"running root", func(st *Store, at int64, a map[string]int64) { st.sessions["p"].Status = "running" }},
		{"waiting descendant", func(st *Store, at int64, a map[string]int64) { st.sessions["g"].Status = "waiting" }},
		{"blocked descendant", func(st *Store, at int64, a map[string]int64) { st.sessions["g"].Status = "blocked" }},
		{"paused descendant", func(st *Store, at int64, a map[string]int64) { st.sessions["g"].Status = "paused" }},
		{"recent progress", func(st *Store, at int64, a map[string]int64) {
			st.sessions["c"].UpdatedAt = at - time.Minute.Milliseconds()
		}},
		{"recent ended parent transcript", func(st *Store, at int64, a map[string]int64) { a["p"] = at - time.Minute.Milliseconds() }},
		{"recent child transcript", func(st *Store, at int64, a map[string]int64) { a["c"] = at - time.Minute.Milliseconds() }},
		{"recent ended descendant transcript", func(st *Store, at int64, a map[string]int64) { a["g"] = at - time.Minute.Milliseconds() }},
		{"parent ended under hour", func(st *Store, at int64, a map[string]int64) {
			st.sessions["p"].EndedAt = at - (59 * time.Minute).Milliseconds()
		}},
		{"unknown parent end", func(st *Store, at int64, a map[string]int64) { st.sessions["p"].EndedAt = 0 }},
		{"unknown descendant end", func(st *Store, at int64, a map[string]int64) { st.sessions["g"].EndedAt = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Flush() })
			at := archiveFixture(t, st)
			activity := make(map[string]int64)
			tc.mutate(st, at, activity)
			if count := st.autoArchive(at, activity); count != 0 {
				t.Fatalf("protected group archived: %d", count)
			}
			for _, id := range []string{"p", "c", "g"} {
				s, _ := st.Session(id)
				if s.Archived {
					t.Fatalf("%s archived", id)
				}
			}
		})
	}
}

func TestAutoArchiveDelaysAndRetainsPriorArchive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		unfinished bool
		age        time.Duration
		want       int
	}{
		{"ended under delay", false, 29 * time.Minute, 0},
		{"ended at delay", false, 30 * time.Minute, 3},
		{"unfinished under delay", true, 59 * time.Minute, 0},
		{"unfinished at delay", true, 60 * time.Minute, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Flush() })
			at := archiveFixture(t, st)
			for _, id := range []string{"p", "c", "g"} {
				s := st.sessions[id]
				s.UpdatedAt = at - tc.age.Milliseconds()
				if isEnded(s.Status) {
					s.EndedAt = s.UpdatedAt
				}
			}
			if !tc.unfinished {
				st.sessions["c"].Status = "done"
				st.sessions["c"].EndedAt = st.sessions["c"].UpdatedAt
			}
			if count := st.autoArchive(at, nil); count != tc.want {
				t.Fatalf("archived=%d want=%d", count, tc.want)
			}
			if count := st.autoArchive(at, nil); count != 0 {
				t.Fatalf("second sweep repeated archive: %d", count)
			}
		})
	}
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() })
	at := archiveFixture(t, st)
	st.sessions["g"].Archived = true
	if count := st.autoArchive(at, nil); count != 2 {
		t.Fatalf("wrong count excluding already archived child: %d", count)
	}
	g, _ := st.Session("g")
	if !g.Archived || len(g.Events) != 0 {
		t.Fatal("already archived child was changed")
	}
	if _, err := st.ArchiveTree("absent", true); err == nil {
		t.Fatal("missing root accepted")
	} else {
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatal(err)
		}
	}
}

func TestAutoArchiveRecoveredChildBecomesLogicalRoot(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() })
	at := archiveFixture(t, st)
	if _, err := st.ArchiveTree("p", true); err != nil {
		t.Fatal(err)
	}
	st.sessions["c"].Archived = false
	st.sessions["c"].Status = "done"
	st.sessions["c"].UpdatedAt = at - archiveEndedDelay.Milliseconds()
	st.sessions["c"].EndedAt = st.sessions["c"].UpdatedAt
	if count := st.autoArchive(at, nil); count != 1 {
		t.Fatalf("recovered child below archived parent must expire as logical root: %d", count)
	}
}

func TestArchiveUndoIDsPreservePriorArchivedDescendants(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Flush() })
	archiveFixture(t, st)
	st.sessions["g"].Archived = true
	result, err := st.ArchiveTree("p", true)
	if err != nil || len(result.Sessions) != 3 || len(result.ChangedIDs) != 2 {
		t.Fatalf("wrong changed set: %+v %v", result, err)
	}
	for _, id := range result.ChangedIDs {
		value := false
		if _, err := st.Patch(id, PatchInput{Archived: &value}); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := st.Session("g")
	if !g.Archived {
		t.Fatal("undo of changed IDs restored a previously archived descendant")
	}
	if _, err := st.ArchiveTree("p", false); err != nil {
		t.Fatal(err)
	}
	g, _ = st.Session("g")
	if g.Archived {
		t.Fatal("explicit restore must restore the complete tree")
	}
}

func TestLargeArchiveUsesOneBatchEvent(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%t", automatic), func(t *testing.T) {
			st, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Flush() })
			at := now()
			old := at - (2 * time.Hour).Milliseconds()
			for root := 0; root < 2; root++ {
				id := fmt.Sprintf("root%d", root)
				st.sessions[id] = &Session{ID: id, Status: "done", UpdatedAt: old, EndedAt: old}
				count := 80
				if root == 1 {
					count = 3
				}
				for i := 0; i < count; i++ {
					kid := fmt.Sprintf("%s-child%d", id, i)
					st.sessions[kid] = &Session{ID: kid, ParentID: id, Status: "running", UpdatedAt: old}
				}
			}
			ch, cancel := st.Subscribe()
			defer cancel()
			want := 81
			if automatic {
				want = 85
				if count := st.autoArchive(at, nil); count != want {
					t.Fatalf("automatic count %d != %d", count, want)
				}
			} else {
				result, err := st.ArchiveTree("root0", true)
				if err != nil || len(result.ChangedIDs) != want {
					t.Fatalf("manual count: %+v %v", result, err)
				}
			}
			select {
			case frame := <-ch:
				var batch []*Session
				if frame.Event != "sessions" || json.Unmarshal(frame.Data, &batch) != nil || len(batch) != want {
					t.Fatalf("batch lost sessions: event=%s want=%d got=%d", frame.Event, want, len(batch))
				}
				for _, s := range batch {
					if !s.Archived {
						t.Fatalf("unarchived batch member %s", s.ID)
					}
				}
			default:
				t.Fatal("missing batch")
			}
			select {
			case extra := <-ch:
				t.Fatalf("archive emitted more than one event: %s", extra.Event)
			default:
			}
		})
	}
}
