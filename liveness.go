package main

// 对话活跃度：agent 很久没报进度时，看它自己的对话记录文件最近有没有写入，
// 区分“还在干（跑一条很长的命令、只是没报）”和“真的停了 / 窗口关了”。
//   - Codex：~/.codex/sessions/年/月/日/rollout-…-<线程ID>.jsonl，线程 ID 就是登记时的 externalId
//   - Claude Code：~/.claude/projects/<项目>/<会话ID>.jsonl
// 只读文件的修改时间，不读内容。externalId 必须是 UUID 才查（不会拼出别的路径）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type transcripts struct {
	mu    sync.Mutex
	paths map[string]string    // agent|ext → 找到的文件
	miss  map[string]time.Time // 没找到的，过一会儿再找（新对话的文件可能还没建）
}

var theTranscripts = &transcripts{paths: map[string]string{}, miss: map[string]time.Time{}}

// lastActivity 对话记录文件最后写入的时间（毫秒）；不支持的 agent、找不到文件返回 0
func (t *transcripts) lastActivity(agent, ext string) int64 {
	if !uuidRe.MatchString(ext) || (agent != "codex" && agent != "claude") {
		return 0
	}
	key := agent + "|" + ext
	t.mu.Lock()
	p, ok := t.paths[key]
	missAt, missed := t.miss[key]
	t.mu.Unlock()
	if !ok {
		if missed && time.Since(missAt) < 5*time.Minute {
			return 0
		}
		p = findTranscript(agent, ext)
		t.mu.Lock()
		if p == "" {
			t.miss[key] = time.Now()
		} else {
			t.paths[key] = p
			delete(t.miss, key)
		}
		t.mu.Unlock()
		if p == "" {
			return 0
		}
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.mu.Lock()
		delete(t.paths, key) // 文件被挪走了（Codex 归档对话），下次重新找
		t.mu.Unlock()
		return 0
	}
	return fi.ModTime().UnixMilli()
}

func findTranscript(agent, ext string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var pattern string
	switch agent {
	case "codex":
		pattern = filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+ext+".jsonl")
	case "claude":
		pattern = filepath.Join(home, ".claude", "projects", "*", ext+".jsonl")
	}
	m, _ := filepath.Glob(pattern)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// Activity 还没结束的会话里，能查到对话记录的那些：会话 ID → 对话最后动静的时间
func (st *Store) Activity() map[string]int64 {
	type ref struct{ id, agent, ext string }
	st.mu.Lock()
	var refs []ref
	for _, s := range st.sessions {
		if !isEnded(s.Status) && !s.Archived && s.ExternalID != "" {
			refs = append(refs, ref{s.ID, s.Agent, s.ExternalID})
		}
	}
	st.mu.Unlock()
	out := map[string]int64{}
	for _, r := range refs { // 在锁外查文件
		if at := theTranscripts.lastActivity(r.agent, r.ext); at > 0 {
			out[r.id] = at
		}
	}
	return out
}
