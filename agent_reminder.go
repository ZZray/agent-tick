package main

import (
	"fmt"
	"strings"
	"time"
)

type agentReminderState struct {
	Fingerprint string
	At          int64
}

const agentReminderDelay = 30 * time.Minute

// AgentReminder is pulled only by an agent's current request. The only mutation
// is an in-memory cooldown; it does not publish, mark read, save or change status.
func (st *Store) AgentReminder(id string, finishing bool, at int64) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.sessions[id]
	if s == nil {
		return ""
	}
	if finishing && s.Status != "done" {
		return ""
	}
	if !finishing && (s.Archived || (s.Status != "running" && s.Status != "planning")) {
		return ""
	}
	kind := "stale"
	if finishing {
		kind = "finish"
	}
	if st.agentReminders == nil {
		st.agentReminders = map[string]agentReminderState{}
	}
	var lines []string
	count := 0
	due := 0
	for index, it := range s.Items {
		last := it.UpdatedAt
		if last == 0 {
			last = it.StartedAt
		}
		unfinished := it.Status != "done" && it.Status != "skipped"
		if finishing {
			if !unfinished {
				continue
			}
		} else if it.Status != "doing" || last <= 0 || at-last < agentReminderDelay.Milliseconds() {
			continue
		}
		count++
		key := id + ":" + kind + ":" + it.ID
		fingerprint := fmt.Sprintf("%s:%d", it.Status, last)
		previous := st.agentReminders[key]
		if previous.Fingerprint == fingerprint && at-previous.At < agentReminderDelay.Milliseconds() {
			continue
		}
		due++
		st.agentReminders[key] = agentReminderState{Fingerprint: fingerprint, At: at}
		if len(lines) < 3 {
			age := "更新时间未知"
			if last > 0 {
				age = fmt.Sprintf("%d 分钟未实际更新", max(int64(0), (at-last)/time.Minute.Milliseconds()))
			}
			lines = append(lines, fmt.Sprintf("#%d (id=%s) [%s] %s · %s", index+1, it.ID, it.Status, clip(it.Text, 80), age))
		}
	}
	if due == 0 {
		return ""
	}
	heading := fmt.Sprintf("Tick 提醒：本会话有 %d 条 doing 长期未更新，是仍在执行、卡在依赖，还是忘记更新？请逐条核对并报告真实状态；仍在执行可补充实际进展或预计时长，待续用 tick_pause。", count)
	if finishing {
		heading = fmt.Sprintf("Tick 收尾核对：会话已按请求结束，但清单仍有 %d 条未结束项。请核对是遗漏更新、受阻还是留待下次；Tick 没有自动补 done，也不把它们视为通过。", count)
	}
	if count > len(lines) {
		lines = append(lines, fmt.Sprintf("另有 %d 条，请用 tick_show 查看。", count-len(lines)))
	}
	return heading + "\n" + strings.Join(lines, "\n")
}
