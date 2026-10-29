package main

// 会话、清单、通知的数据模型和状态机。所有修改走 Store.mutate：加锁、校验、改、落盘、推送；失败整体回滚。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	maxAgent, maxID, maxTitle, maxText = 40, 200, 200, 300
	maxNote, maxSummary, maxPath       = 2000, 4000, 500
	maxItems, maxEvents, maxEventText  = 100, 300, 300
	maxNotices                         = 300
)

var (
	sessionStatus = []string{"planning", "running", "waiting", "blocked", "done", "failed", "cancelled"}
	itemStatus    = []string{"todo", "doing", "done", "skipped", "blocked"}
	noticeKinds   = []string{"info", "success", "warning"} // agent 主动发；waiting / blocked / done / failed 随状态自动生成
	statusText    = map[string]string{"planning": "回到写清单", "running": "继续执行", "waiting": "等你", "blocked": "受阻", "done": "完成", "failed": "失败", "cancelled": "取消"}
	itemText      = map[string]string{"todo": "重开", "doing": "开始", "done": "勾选", "skipped": "跳过", "blocked": "卡住"}
	noticeDefault = map[string]string{"waiting": "等你回复", "blocked": "卡住了", "done": "做完了", "failed": "没做成"}
)

func isWaiting(s string) bool { return s == "waiting" || s == "blocked" }
func isEnded(s string) bool   { return s == "done" || s == "failed" || s == "cancelled" }

// 等待和结束时，进行中条目的计时暂停：条目耗时只算 agent 干活的时间
func isPaused(s string) bool { return isWaiting(s) || isEnded(s) }

type Item struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Note      string `json:"note"`
	SpentMs   int64  `json:"spentMs"`
	StartedAt int64  `json:"startedAt"`
	DoneAt    int64  `json:"doneAt"`
}

type Event struct {
	At   int64  `json:"at"`
	Type string `json:"type"`
	Text string `json:"text"`
}

type Session struct {
	ID         string  `json:"id"`
	Key        string  `json:"key"`
	Agent      string  `json:"agent"`
	ExternalID string  `json:"externalId"`
	ParentID   string  `json:"parentId"` // 子 agent 的会话：挂在哪个主会话下（只支持一层）
	Title      string  `json:"title"`
	Goal       string  `json:"goal"`
	Workspace  string  `json:"workspace"`
	Link       string  `json:"link"`
	Status     string  `json:"status"`
	StatusNote string  `json:"statusNote"`
	Summary    string  `json:"summary"`
	Archived   bool    `json:"archived"`
	CreatedAt  int64   `json:"createdAt"`
	UpdatedAt  int64   `json:"updatedAt"`
	EndedAt    int64   `json:"endedAt"`
	WaitMs     int64   `json:"waitMs"`
	WaitSince  int64   `json:"waitSince"`
	IdleMs     int64   `json:"idleMs"`
	NextItem   int     `json:"nextItem"`
	Items      []*Item `json:"items"`
	Events     []Event `json:"events"`
}

type Notice struct {
	ID        string `json:"id"`
	At        int64  `json:"at"`
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Read      bool   `json:"read"`
}

// apiError 带 HTTP 状态码，消息直接给 agent 或用户看
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }
func bad(format string, a ...any) error {
	return &apiError{400, fmt.Sprintf(format, a...)}
}

func now() int64 { return time.Now().UnixMilli() }

func newID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func fmtMs(ms int64) string {
	s := max(ms/1000, 0)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm %02ds", m, s%60)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}

// ---------- 输入：只在边界校验一次 ----------

// field 接收可缺省的字符串：nil 表示没给，"" 表示清空
func field(p *string, name string, limit int) (*string, error) {
	if p == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*p)
	if utf8.RuneCountInString(v) > limit {
		return nil, bad("%s 超过 %d 个字符", name, limit)
	}
	return &v, nil
}

func required(p *string, name string, limit int) (string, error) {
	v, err := field(p, name, limit)
	if err != nil {
		return "", err
	}
	if v == nil || *v == "" {
		return "", bad("缺少 %s", name)
	}
	return *v, nil
}

func oneOf(p *string, name string, allowed []string) (string, error) {
	if p == nil {
		return "", nil
	}
	if !slices.Contains(allowed, *p) {
		return "", bad("%s 只能是 %s", name, strings.Join(allowed, " / "))
	}
	return *p, nil
}

// 回到对话的链接会被系统默认程序打开，只放行网页和常见 agent 的协议
var linkSchemes = []string{"http", "https", "codex", "claude", "cursor", "vscode", "vscode-insiders", "kiro", "windsurf", "zed"}

func linkAllowed(v string) bool {
	scheme, rest, ok := strings.Cut(v, "://")
	return ok && rest != "" && slices.Contains(linkSchemes, strings.ToLower(scheme))
}

func linkField(p *string) (*string, error) {
	v, err := field(p, "link", maxPath)
	if err != nil || v == nil || *v == "" {
		return v, err
	}
	if !linkAllowed(*v) {
		return nil, bad("link 只支持 %s 这些协议，例如 https://… 或 codex://threads/<id>", strings.Join(linkSchemes, " / "))
	}
	return v, nil
}

type itemInput struct {
	Text   string
	Status string
	Note   *string
}

// items 的每一项可以是字符串，或 {text, status, note}
func parseItems(raw []json.RawMessage) ([]itemInput, error) {
	if len(raw) > maxItems {
		return nil, bad("items 最多 %d 条", maxItems)
	}
	out := make([]itemInput, 0, len(raw))
	for i, r := range raw {
		name := fmt.Sprintf("items[%d]", i)
		var s string
		if json.Unmarshal(r, &s) == nil {
			t, err := required(&s, name, maxText)
			if err != nil {
				return nil, err
			}
			out = append(out, itemInput{Text: t})
			continue
		}
		var o struct {
			Text, Status, Note *string
		}
		if err := json.Unmarshal(r, &o); err != nil {
			return nil, bad("%s 必须是字符串或 {text, status, note}", name)
		}
		t, err := required(o.Text, name+".text", maxText)
		if err != nil {
			return nil, err
		}
		st, err := oneOf(o.Status, name+".status", itemStatus)
		if err != nil {
			return nil, err
		}
		note, err := field(o.Note, name+".note", maxNote)
		if err != nil {
			return nil, err
		}
		out = append(out, itemInput{Text: t, Status: st, Note: note})
	}
	return out, nil
}

// ---------- Store ----------

type Store struct {
	mu       sync.Mutex
	file     string
	sessions map[string]*Session
	notices  []*Notice
	pending  []*Notice // 本次修改中新建或改动的通知，落盘后推送

	subMu    sync.Mutex
	subs     map[chan Frame]struct{}
	onNotice atomic.Pointer[func(*Notice)]
}

// Frame 是推给 SSE 订阅者的一条消息
type Frame struct {
	Event string
	Data  []byte
}

type diskData struct {
	Version  int        `json:"version"`
	Sessions []*Session `json:"sessions"`
	Notices  []*Notice  `json:"notices"`
}

func OpenStore(dir string) (*Store, error) {
	st := &Store{file: filepath.Join(dir, "sessions.json"), sessions: map[string]*Session{}, subs: map[chan Frame]struct{}{}}
	b, err := os.ReadFile(st.file)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	var d diskData
	if err := json.Unmarshal(b, &d); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", st.file, now())
		if rerr := os.Rename(st.file, backup); rerr != nil {
			return nil, fmt.Errorf("数据文件无法解析（%v），也没能移走：%w", err, rerr)
		}
		log.Printf("数据文件无法解析，已移到 %s，从空数据开始：%v", backup, err)
		return st, nil
	}
	for _, s := range d.Sessions {
		st.sessions[s.ID] = s
	}
	st.notices = d.Notices
	return st, nil
}

func (st *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(st.file), 0o755); err != nil {
		return err
	}
	list := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		list = append(list, s)
	}
	b, err := json.Marshal(diskData{Version: 2, Sessions: list, Notices: st.notices})
	if err != nil {
		return err
	}
	tmp := st.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	// Windows 上杀毒或索引偶尔短暂占用目标文件，重试几次
	for attempt := 1; ; attempt++ {
		err = os.Rename(tmp, st.file)
		if err == nil || attempt >= 4 {
			return err
		}
		time.Sleep(time.Duration(50*attempt) * time.Millisecond)
	}
}

func (st *Store) Subscribe() (chan Frame, func()) {
	ch := make(chan Frame, 64)
	st.subMu.Lock()
	st.subs[ch] = struct{}{}
	st.subMu.Unlock()
	return ch, func() {
		st.subMu.Lock()
		delete(st.subs, ch)
		st.subMu.Unlock()
	}
}

func (st *Store) publish(event string, v any) {
	b, _ := json.Marshal(v)
	st.subMu.Lock()
	defer st.subMu.Unlock()
	for ch := range st.subs {
		select {
		case ch <- Frame{event, b}:
		default: // 订阅者太慢就丢这一帧，重连时会整体刷新
		}
	}
}

// change 在锁内执行 fn；成功则落盘并推送，失败把会话和通知恢复原样
func (st *Store) change(sessionID string, fn func(s *Session) error) (*Session, error) {
	st.mu.Lock()
	var s *Session
	var backup *Session
	if sessionID != "" {
		s = st.sessions[sessionID]
		if s == nil {
			st.mu.Unlock()
			return nil, &apiError{404, "没有会话 " + sessionID}
		}
		backup = cloneSession(s)
	}
	noticeBackup := cloneNotices(st.notices)
	err := fn(s)
	if err == nil {
		if s != nil {
			s.UpdatedAt = now()
		}
		if err = st.save(); err != nil {
			err = &apiError{500, "数据没能写入磁盘，本次修改已撤回：" + err.Error()}
		}
	}
	if err != nil {
		if backup != nil {
			st.sessions[sessionID] = backup
		}
		st.notices = noticeBackup
		st.pending = nil
		st.mu.Unlock()
		return nil, err
	}
	var snap *Session
	if s != nil {
		snap = cloneSession(s)
	}
	pending := cloneNotices(st.pending)
	st.pending = nil
	// 在锁内推送，保证订阅者收到的顺序和修改顺序一致（publish 不阻塞）
	if snap != nil {
		st.publish("session", snap)
	}
	for _, n := range pending {
		st.publish("notice", n)
	}
	st.mu.Unlock()

	if hook := st.onNotice.Load(); hook != nil {
		for _, n := range pending {
			if !n.Read {
				(*hook)(n)
			}
		}
	}
	return snap, nil
}

// SetOnNotice 设置新通知的回调（桌面端弹系统通知），在锁外调用
func (st *Store) SetOnNotice(fn func(*Notice)) { st.onNotice.Store(&fn) }

func cloneSession(s *Session) *Session {
	c := *s
	c.Items = make([]*Item, len(s.Items))
	for i, it := range s.Items {
		x := *it
		c.Items[i] = &x
	}
	c.Events = slices.Clone(s.Events)
	return &c
}

func cloneNotices(ns []*Notice) []*Notice {
	out := make([]*Notice, len(ns))
	for i, n := range ns {
		x := *n
		out[i] = &x
	}
	return out
}

// ---------- 读 ----------

func (st *Store) Sessions(includeArchived bool) []*Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := []*Session{}
	for _, s := range st.sessions {
		if includeArchived || !s.Archived {
			out = append(out, cloneSession(s))
		}
	}
	slices.SortFunc(out, func(a, b *Session) int { return int(b.UpdatedAt - a.UpdatedAt) })
	return out
}

func (st *Store) Session(id string) (*Session, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.sessions[id]
	if s == nil {
		return nil, &apiError{404, "没有会话 " + id}
	}
	return cloneSession(s), nil
}

func (st *Store) Notices() []*Notice {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := cloneNotices(st.notices)
	slices.Reverse(out)
	return out
}

// ---------- 状态与计时 ----------

func addEvent(s *Session, typ, text string) {
	s.Events = append(s.Events, Event{At: now(), Type: typ, Text: clip(text, maxEventText)})
	if len(s.Events) > maxEvents {
		s.Events = slices.Clone(s.Events[len(s.Events)-maxEvents:])
	}
}

func setItemStatus(s *Session, it *Item, status string) bool {
	if it.Status == status {
		return false
	}
	t := now()
	if it.StartedAt != 0 {
		it.SpentMs += t - it.StartedAt
		it.StartedAt = 0
	}
	if status == "doing" && !isPaused(s.Status) {
		it.StartedAt = t
	}
	it.DoneAt = 0
	if status == "done" || status == "skipped" {
		it.DoneAt = t
	}
	it.Status = status
	return true
}

func (st *Store) setSessionStatus(s *Session, status string) bool {
	prev := s.Status
	if prev == status {
		return false
	}
	t := now()
	if isWaiting(prev) && s.WaitSince != 0 {
		s.WaitMs += t - s.WaitSince
		s.WaitSince = 0
	}
	if isWaiting(status) {
		s.WaitSince = t
	}
	// 不再等你了：之前那条“等你 / 受阻”通知自动算已读
	if isWaiting(prev) {
		st.markRead(func(n *Notice) bool { return n.SessionID == s.ID && isWaiting(n.Kind) })
	}
	// 结束后又被重新打开：中间空着的时间不算总耗时，也不算干活
	if isEnded(prev) && !isEnded(status) && s.EndedAt != 0 {
		s.IdleMs += t - s.EndedAt
	}
	pause, resume := !isPaused(prev) && isPaused(status), isPaused(prev) && !isPaused(status)
	for _, it := range s.Items {
		if it.Status != "doing" {
			continue
		}
		if pause && it.StartedAt != 0 {
			it.SpentMs += t - it.StartedAt
			it.StartedAt = 0
		}
		if resume {
			it.StartedAt = t
		}
	}
	s.EndedAt = 0
	if isEnded(status) {
		s.EndedAt = t
	}
	s.Status = status
	return true
}

func (st *Store) wake(s *Session) {
	st.setSessionStatus(s, "running")
	s.StatusNote = ""
	addEvent(s, "running", statusText["running"])
}

// 清单里有条目动过，写清单阶段就算开工了；有条目在做而会话暂停着，说明 agent 已回到工作
func (st *Store) autoRun(s *Session) {
	if s.Status == "planning" && slices.ContainsFunc(s.Items, func(it *Item) bool { return it.Status != "todo" }) {
		st.setSessionStatus(s, "running")
	} else if isPaused(s.Status) && slices.ContainsFunc(s.Items, func(it *Item) bool { return it.Status == "doing" }) {
		st.wake(s)
	}
}

func newItem(s *Session, x itemInput) *Item {
	it := &Item{ID: "i" + strconv.Itoa(s.NextItem), Text: x.Text, Status: "todo"}
	s.NextItem++
	if x.Note != nil {
		it.Note = *x.Note
	}
	if x.Status != "" {
		setItemStatus(s, it, x.Status)
	}
	return it
}

// ---------- 通知 ----------

func (st *Store) pushNotice(s *Session, agent, kind, title, text string) *Notice {
	n := &Notice{ID: newID("n_", 5), At: now(), Agent: agent, Kind: kind, Title: title, Text: clip(text, maxNote)}
	if s != nil {
		n.SessionID, n.Agent, n.Title = s.ID, s.Agent, s.Title
	}
	st.notices = append(st.notices, n)
	if len(st.notices) > maxNotices {
		st.notices = slices.Clone(st.notices[len(st.notices)-maxNotices:])
	}
	st.pending = append(st.pending, n)
	return n
}

func (st *Store) markRead(match func(*Notice) bool) int {
	count := 0
	for _, n := range st.notices {
		if !n.Read && match(n) {
			n.Read = true
			st.pending = append(st.pending, n)
			count++
		}
	}
	return count
}

// ---------- 业务操作（输入已在 API 层解码，这里做校验和修改） ----------

type StartInput struct {
	Agent, Title, ExternalID, Goal, Workspace, Link *string
	ParentID                                        *string `json:"parentId"` // 子 agent 的会话挂在主会话下
	Items                                           []json.RawMessage
}

// Start 登记会话。同 agent + externalId 拿回原会话（attached=true），只追加文字不同的新条目
func (st *Store) Start(in StartInput) (s *Session, attached bool, err error) {
	agent, err := required(in.Agent, "agent", maxAgent)
	if err != nil {
		return nil, false, err
	}
	agent = strings.ToLower(agent)
	title, err := required(in.Title, "title", maxTitle)
	if err != nil {
		return nil, false, err
	}
	ext, err := field(in.ExternalID, "externalId", maxID)
	if err != nil {
		return nil, false, err
	}
	goal, err := field(in.Goal, "goal", maxNote)
	if err != nil {
		return nil, false, err
	}
	ws, err := field(in.Workspace, "workspace", maxPath)
	if err != nil {
		return nil, false, err
	}
	link, err := linkField(in.Link)
	if err != nil {
		return nil, false, err
	}
	items, err := parseItems(in.Items)
	if err != nil {
		return nil, false, err
	}
	parent, err := field(in.ParentID, "parentId", maxID)
	if err != nil {
		return nil, false, err
	}
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	key := ""
	if val(ext) != "" {
		key = agent + ":" + val(ext)
	}

	st.mu.Lock()
	if p := val(parent); p != "" {
		ps := st.sessions[p]
		if ps == nil {
			st.mu.Unlock()
			return nil, false, bad("没有父会话 %s", p)
		}
		if ps.ParentID != "" {
			st.mu.Unlock()
			return nil, false, bad("父会话 %s 本身是子任务，只支持一层", p)
		}
	}
	var existing string
	for _, x := range st.sessions {
		if key != "" && x.Key == key {
			existing = x.ID
			break
		}
	}
	var id string
	if existing == "" {
		for id = newID("s_", 3); st.sessions[id] != nil; id = newID("s_", 3) {
		}
		t := now()
		st.sessions[id] = &Session{ID: id, Key: key, Agent: agent, ExternalID: val(ext), Title: title, Goal: val(goal), ParentID: val(parent),
			Workspace: val(ws), Link: val(link), Status: "planning", CreatedAt: t, UpdatedAt: t, NextItem: 1, Items: []*Item{}, Events: []Event{}}
	}
	st.mu.Unlock()

	if existing != "" {
		s, err = st.change(existing, func(s *Session) error {
			known := map[string]bool{}
			for _, it := range s.Items {
				known[it.Text] = true
			}
			fresh := slices.DeleteFunc(slices.Clone(items), func(x itemInput) bool { return known[x.Text] })
			if len(fresh)+len(s.Items) > maxItems {
				return bad("清单最多 %d 条", maxItems)
			}
			if len(fresh) > 0 {
				first := len(s.Items) == 0
				if isEnded(s.Status) {
					st.wake(s) // 同一对话来了新任务
				}
				for _, x := range fresh {
					s.Items = append(s.Items, newItem(s, x))
				}
				if first {
					addEvent(s, "plan", fmt.Sprintf("写好清单：%d 项", len(fresh)))
				} else {
					addEvent(s, "plan", fmt.Sprintf("追加 %d 项", len(fresh)))
				}
				st.autoRun(s)
			}
			if val(link) != "" {
				s.Link = val(link)
			}
			if val(ws) != "" {
				s.Workspace = val(ws)
			}
			if val(goal) != "" && s.Goal == "" {
				s.Goal = val(goal)
			}
			s.Archived = false
			addEvent(s, "attach", "重新关联到这个会话")
			return nil
		})
		return s, true, err
	}

	s, err = st.change(id, func(s *Session) error {
		addEvent(s, "created", "登记会话")
		if len(items) > 0 {
			for _, x := range items {
				s.Items = append(s.Items, newItem(s, x))
			}
			addEvent(s, "plan", fmt.Sprintf("写好清单：%d 项", len(items)))
			st.autoRun(s)
		}
		return nil
	})
	if err != nil {
		st.mu.Lock()
		delete(st.sessions, id)
		st.mu.Unlock()
	}
	return s, false, err
}

type PatchInput struct {
	Title, Goal, Workspace, Link, Status, Note, Summary *string
	Archived                                            *bool
}

func (st *Store) Patch(id string, in PatchInput) (*Session, error) {
	title, err := field(in.Title, "title", maxTitle)
	if err != nil {
		return nil, err
	}
	if title != nil && *title == "" {
		return nil, bad("title 不能为空")
	}
	goal, err := field(in.Goal, "goal", maxNote)
	if err != nil {
		return nil, err
	}
	ws, err := field(in.Workspace, "workspace", maxPath)
	if err != nil {
		return nil, err
	}
	link, err := linkField(in.Link)
	if err != nil {
		return nil, err
	}
	status, err := oneOf(in.Status, "status", sessionStatus)
	if err != nil {
		return nil, err
	}
	note, err := field(in.Note, "note", maxNote)
	if err != nil {
		return nil, err
	}
	summary, err := field(in.Summary, "summary", maxSummary)
	if err != nil {
		return nil, err
	}
	return st.change(id, func(s *Session) error {
		for _, f := range []struct {
			src *string
			dst *string
		}{{title, &s.Title}, {goal, &s.Goal}, {ws, &s.Workspace}, {link, &s.Link}, {summary, &s.Summary}} {
			if f.src != nil {
				*f.dst = *f.src
			}
		}
		if in.Archived != nil {
			s.Archived = *in.Archived
		}
		if status == "" {
			if note != nil {
				s.StatusNote = *note
			}
			return nil
		}
		changed := st.setSessionStatus(s, status)
		next := s.StatusNote
		switch {
		case status == "running":
			next = ""
		case note != nil:
			next = *note
		case changed:
			next = ""
		}
		if changed || next != s.StatusNote {
			extra := next
			if extra == "" && isEnded(status) {
				extra = s.Summary
			}
			if extra != "" {
				addEvent(s, status, statusText[status]+"："+extra)
			} else {
				addEvent(s, status, statusText[status])
			}
		}
		renewed := !changed && isWaiting(status) && next != s.StatusNote
		s.StatusNote = next
		// 已经在等你、又问了新问题：旧卡片算已读，换成新的
		if renewed {
			st.markRead(func(n *Notice) bool { return n.SessionID == s.ID && isWaiting(n.Kind) })
		}
		// 需要用户知道的状态变化，生成一条通知
		if def, ok := noticeDefault[status]; ok && (changed || renewed) {
			text := s.Summary
			if isWaiting(status) {
				text = next
			}
			if text == "" {
				text = def
			}
			st.pushNotice(s, "", status, "", text)
		}
		return nil
	})
}

// ReplacePlan 重发完整清单：文字相同的条目保留原状态和耗时
func (st *Store) ReplacePlan(id string, raw []json.RawMessage) (*Session, error) {
	if raw == nil {
		return nil, bad("缺少 items")
	}
	inputs, err := parseItems(raw)
	if err != nil {
		return nil, err
	}
	return st.change(id, func(s *Session) error {
		pool := slices.Clone(s.Items)
		next := make([]*Item, 0, len(inputs))
		added := 0
		for _, x := range inputs {
			k := slices.IndexFunc(pool, func(it *Item) bool { return it.Text == x.Text })
			if k < 0 {
				next = append(next, newItem(s, x))
				added++
				continue
			}
			it := pool[k]
			pool = slices.Delete(pool, k, k+1)
			if x.Status != "" {
				setItemStatus(s, it, x.Status)
			}
			if x.Note != nil {
				it.Note = *x.Note
			}
			next = append(next, it)
		}
		first := len(s.Items) == 0
		s.Items = next
		if first {
			addEvent(s, "plan", fmt.Sprintf("写好清单：%d 项", len(next)))
		} else {
			addEvent(s, "plan", fmt.Sprintf("调整清单：新增 %d，移除 %d，共 %d 项", added, len(pool), len(next)))
		}
		st.autoRun(s)
		return nil
	})
}

func (st *Store) AppendItems(id string, raw []json.RawMessage, text *string) (*Session, error) {
	var inputs []itemInput
	if raw != nil {
		var err error
		if inputs, err = parseItems(raw); err != nil {
			return nil, err
		}
	} else {
		t, err := required(text, "text", maxText)
		if err != nil {
			return nil, err
		}
		inputs = []itemInput{{Text: t}}
	}
	return st.change(id, func(s *Session) error {
		if len(s.Items)+len(inputs) > maxItems {
			return bad("清单最多 %d 条", maxItems)
		}
		if isEnded(s.Status) {
			st.wake(s)
		}
		texts := make([]string, len(inputs))
		for i, x := range inputs {
			s.Items = append(s.Items, newItem(s, x))
			texts[i] = x.Text
		}
		addEvent(s, "plan", fmt.Sprintf("追加 %d 项：%s", len(inputs), strings.Join(texts, "；")))
		st.autoRun(s)
		return nil
	})
}

type ItemPatch struct {
	Status, Note, Text *string
}

func (st *Store) PatchItem(id, ref string, in ItemPatch) (*Session, error) {
	status, err := oneOf(in.Status, "status", itemStatus)
	if err != nil {
		return nil, err
	}
	note, err := field(in.Note, "note", maxNote)
	if err != nil {
		return nil, err
	}
	text, err := field(in.Text, "text", maxText)
	if err != nil {
		return nil, err
	}
	if text != nil && *text == "" {
		return nil, bad("text 不能为空")
	}
	return st.change(id, func(s *Session) error {
		var it *Item
		if n, err := strconv.Atoi(ref); err == nil {
			if n >= 1 && n <= len(s.Items) {
				it = s.Items[n-1]
			}
		} else if k := slices.IndexFunc(s.Items, func(x *Item) bool { return x.ID == ref }); k >= 0 {
			it = s.Items[k]
		}
		if it == nil {
			return &apiError{404, fmt.Sprintf("没有条目 %s（清单共 %d 项）", ref, len(s.Items))}
		}
		// agent 开始做某条，说明它已经回到工作状态
		if status == "doing" && isPaused(s.Status) {
			st.wake(s)
		} else if status != "" && status != "todo" && s.Status == "planning" {
			st.setSessionStatus(s, "running")
		}
		if text != nil {
			it.Text = *text
		}
		if note != nil {
			it.Note = *note
		}
		if status != "" && setItemStatus(s, it, status) {
			n := slices.Index(s.Items, it) + 1
			msg := fmt.Sprintf("%s #%d %s", itemText[status], n, it.Text)
			if status == "done" && it.SpentMs > 0 {
				msg += "（" + fmtMs(it.SpentMs) + "）"
			}
			if (status == "skipped" || status == "blocked") && it.Note != "" {
				msg += "：" + it.Note
			}
			addEvent(s, "item-"+status, msg)
		}
		return nil
	})
}

// Delete 永久删除会话：连同它的子任务和相关通知。不可撤销（想留着就归档）
func (st *Store) Delete(id string) ([]string, error) {
	st.mu.Lock()
	s := st.sessions[id]
	if s == nil {
		st.mu.Unlock()
		return nil, &apiError{404, "没有会话 " + id}
	}
	ids := []string{id}
	for _, x := range st.sessions {
		if x.ParentID == id {
			ids = append(ids, x.ID)
		}
	}
	backup := map[string]*Session{}
	for _, x := range ids {
		backup[x] = st.sessions[x]
		delete(st.sessions, x)
	}
	noticeBackup := st.notices
	gone := map[string]bool{}
	for _, x := range ids {
		gone[x] = true
	}
	st.notices = slices.DeleteFunc(slices.Clone(st.notices), func(n *Notice) bool { return gone[n.SessionID] })
	if err := st.save(); err != nil {
		for k, v := range backup {
			st.sessions[k] = v
		}
		st.notices = noticeBackup
		st.mu.Unlock()
		return nil, &apiError{500, "数据没能写入磁盘，删除已撤回：" + err.Error()}
	}
	st.mu.Unlock()
	st.publish("deleted", map[string][]string{"ids": ids})
	return ids, nil
}

// DeleteItem 删掉清单里的一条（agent 写错了、用户不想要了）
func (st *Store) DeleteItem(id, ref string) (*Session, error) {
	return st.change(id, func(s *Session) error {
		k := -1
		if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(s.Items) {
			k = n - 1
		} else {
			k = slices.IndexFunc(s.Items, func(x *Item) bool { return x.ID == ref })
		}
		if k < 0 {
			return &apiError{404, fmt.Sprintf("没有条目 %s（清单共 %d 项）", ref, len(s.Items))}
		}
		addEvent(s, "plan", fmt.Sprintf("删掉 #%d %s", k+1, s.Items[k].Text))
		s.Items = slices.Delete(s.Items, k, k+1)
		return nil
	})
}

func (st *Store) Log(id string, text *string) (*Session, error) {
	t, err := required(text, "text", maxNote)
	if err != nil {
		return nil, err
	}
	return st.change(id, func(s *Session) error {
		addEvent(s, "log", t)
		return nil
	})
}

type NotifyInput struct {
	SessionID, Agent, Kind, Title, Text *string
}

// Notify 是 agent 主动发的通知：挂在会话上，或只带 agent 名
func (st *Store) Notify(in NotifyInput) (*Notice, error) {
	text, err := required(in.Text, "text", maxNote)
	if err != nil {
		return nil, err
	}
	kind := "info"
	if in.Kind != nil {
		if kind, err = oneOf(in.Kind, "kind", noticeKinds); err != nil {
			return nil, err
		}
	}
	sid, err := field(in.SessionID, "sessionId", maxID)
	if err != nil {
		return nil, err
	}
	// 返回副本：原对象之后可能在锁内被标为已读
	var n Notice
	if sid != nil && *sid != "" {
		_, err = st.change(*sid, func(s *Session) error {
			n = *st.pushNotice(s, "", kind, "", text)
			addEvent(s, "notice", "通知："+text)
			return nil
		})
		return &n, err
	}
	agent, err := required(in.Agent, "agent", maxAgent)
	if err != nil {
		return nil, err
	}
	title, err := field(in.Title, "title", maxTitle)
	if err != nil {
		return nil, err
	}
	_, err = st.change("", func(*Session) error {
		t := ""
		if title != nil {
			t = *title
		}
		n = *st.pushNotice(nil, strings.ToLower(agent), kind, t, text)
		return nil
	})
	return &n, err
}

type ReadInput struct {
	IDs       []string
	All       bool
	SessionID *string
}

func (st *Store) ReadNotices(in ReadInput) (int, error) {
	if len(in.IDs) > maxNotices {
		return 0, bad("ids 最多 %d 个", maxNotices)
	}
	if in.IDs == nil && !in.All && in.SessionID == nil {
		return 0, bad("给出 ids、sessionId 或 all: true")
	}
	count := 0
	_, err := st.change("", func(*Session) error {
		count = st.markRead(func(n *Notice) bool {
			return in.All || slices.Contains(in.IDs, n.ID) || (in.SessionID != nil && n.SessionID == *in.SessionID)
		})
		return nil
	})
	return count, err
}
