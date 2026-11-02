package main

import (
	"cmp"
	"log"
	"slices"
	"time"
)

const (
	archiveEndedDelay = 30 * time.Minute
	archiveQuietDelay = 60 * time.Minute
)

type ArchiveResult struct {
	Sessions   []*Session `json:"sessions"`
	ChangedIDs []string   `json:"changedIds"`
}

// sessionTree runs under st.mu. Archived descendants remain part of the tree so
// restoring a group is explicit and an archived intermediate cannot orphan it.
func (st *Store) sessionTree(id string) []*Session {
	root := st.sessions[id]
	if root == nil {
		return nil
	}
	children := make(map[string][]*Session)
	for _, s := range st.sessions {
		children[s.ParentID] = append(children[s.ParentID], s)
	}
	for _, list := range children {
		slices.SortFunc(list, func(a, b *Session) int {
			if order := cmp.Compare(a.CreatedAt, b.CreatedAt); order != 0 {
				return order
			}
			return cmp.Compare(a.ID, b.ID)
		})
	}
	list := []*Session{root}
	seen := map[string]bool{id: true}
	for i := 0; i < len(list); i++ {
		for _, child := range children[list[i].ID] {
			if !seen[child.ID] {
				seen[child.ID] = true
				list = append(list, child)
			}
		}
	}
	return list
}

// archiveTree changes the complete subtree under one store lock. Archive only
// changes visibility, never completion or the time of the last progress report.
func (st *Store) archiveTree(list []*Session, archived bool, automatic bool) (ArchiveResult, []*Session) {
	out := ArchiveResult{Sessions: make([]*Session, 0, len(list)), ChangedIDs: []string{}}
	var changed []*Session
	for _, s := range list {
		if s.Archived != archived {
			s.Archived = archived
			kind, text := "archive", "归档整组会话"
			if !archived {
				kind, text = "unarchive", "恢复整组会话"
			}
			if automatic {
				kind, text = "auto-archive", "按设置自动归档整组会话"
			}
			addEvent(s, kind, text)
			out.ChangedIDs = append(out.ChangedIDs, s.ID)
			snap := cloneSession(s)
			if n := len(snap.Events); n > pushEvents {
				snap.Events = snap.Events[n-pushEvents:]
			}
			changed = append(changed, snap)
		}
		out.Sessions = append(out.Sessions, cloneSession(s))
	}
	if len(changed) > 0 {
		st.markDirty()
	}
	return out, changed
}

func (st *Store) ArchiveTree(id string, archived bool) (ArchiveResult, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	list := st.sessionTree(id)
	if len(list) == 0 {
		return ArchiveResult{}, &apiError{404, "没有会话 " + id}
	}
	result, changed := st.archiveTree(list, archived, false)
	if len(changed) > 0 {
		st.publish("sessions", changed)
	}
	return result, nil
}

// AutoArchive is called only by the opted-in desktop timer. A missing transcript
// is unknown activity, not proof that an agent stopped; status is never changed.
func (st *Store) AutoArchive() int {
	activity := make(map[string]int64)
	for _, s := range st.Sessions(true) {
		if at := theTranscripts.lastActivity(s.Agent, s.ExternalID); at > 0 {
			activity[s.ID] = at
		}
	}
	return st.autoArchive(now(), activity)
}

func (st *Store) autoArchive(at int64, activity map[string]int64) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	count := 0
	var changed []*Session
	for _, root := range st.sessions {
		parent := st.sessions[root.ParentID]
		if root.Archived || !isEnded(root.Status) || root.EndedAt <= 0 || (parent != nil && !parent.Archived) {
			continue
		}
		list := st.sessionTree(root.ID)
		latest := max(root.UpdatedAt, root.EndedAt, activity[root.ID])
		unfinished, protected := false, false
		for _, s := range list {
			if s.Archived {
				continue
			}
			if isWaiting(s.Status) || s.Status == "paused" || (isEnded(s.Status) && s.EndedAt <= 0) {
				protected = true
				break
			}
			unfinished = unfinished || !isEnded(s.Status)
			latest = max(latest, s.UpdatedAt, s.EndedAt, activity[s.ID])
		}
		delay := archiveEndedDelay
		if unfinished {
			delay = archiveQuietDelay
		}
		if protected || at-latest < delay.Milliseconds() {
			continue
		}
		for _, s := range list {
			if !s.Archived {
				count++
			}
		}
		_, archived := st.archiveTree(list, true, true)
		changed = append(changed, archived...)
	}
	if count > 0 {
		st.publish("sessions", changed)
		log.Printf("[会话] 按设置自动归档 %d 个会话", count)
	}
	return count
}
