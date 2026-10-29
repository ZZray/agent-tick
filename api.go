package main

// HTTP 层：REST、SSE、内嵌前端、协议文档。只接受本机访问。

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

//go:embed web/index.html AGENT_PROTOCOL.md
var assets embed.FS

const maxBody = 256 << 10

type Server struct {
	store   *Store
	port    int
	desktop bool // 有托盘和原生悬浮窗时，网页不再自己弹系统通知
	mcp     *mcpSessions
	// showFloat 让桌面端显示悬浮窗（第二次启动 tick.exe 时调用）；没有桌面端时为 nil
	showFloat func()
	// restart 让桌面端在空闲时重启到新版本（tick update 换好文件后调用）；没有桌面端时为 nil
	restart func()
	// showPanel 打开本地完整面板窗口，hash 定位到会话（"#s_xxx"）
	showPanel func(hash string)
}

func (srv *Server) origins() []string {
	return []string{fmt.Sprintf("http://127.0.0.1:%d", srv.port), fmt.Sprintf("http://localhost:%d", srv.port)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, map[string]string{"error": ae.Msg})
		return
	}
	log.Printf("内部错误：%v", err)
	writeJSON(w, 500, map[string]string{"error": "服务内部错误：" + err.Error()})
}

// decode 读取 JSON 对象请求体；空体当作 {}
func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return bad("读请求体失败：%v", err)
	}
	if len(b) > maxBody {
		return &apiError{413, "请求体超过 256KB"}
	}
	b = []byte(strings.TrimPrefix(string(b), "\uFEFF"))
	if len(strings.TrimSpace(string(b))) == 0 {
		b = []byte("{}")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		return bad("请求体必须是 JSON 对象")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return bad("请求体不是合法 JSON：%v", err)
	}
	return nil
}

// guard：Host 必须是本机地址（防 DNS rebinding）；写请求再校验 Origin 和 JSON 类型（防网页跨站提交）
func (srv *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := srv.origins()
		if !slices.Contains(origins, "http://"+r.Host) {
			writeJSON(w, 403, map[string]string{"error": "Host 不在允许范围"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if o := r.Header.Get("Origin"); o != "" && !slices.Contains(origins, o) {
				writeJSON(w, 403, map[string]string{"error": "Origin 不在允许范围"})
				return
			}
			if r.Method != http.MethodDelete && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				writeJSON(w, 415, map[string]string{"error": "请求体须为 application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (srv *Server) Handler() http.Handler {
	st := srv.store
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(r *http.Request) (int, any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			status, v, err := fn(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, status, v)
		})
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /protocol", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("AGENT_PROTOCOL.md")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(b), "{{BASE}}", srv.origins()[0])))
	})
	mux.HandleFunc("GET /api/stream", srv.stream)
	mux.Handle("/mcp", http.HandlerFunc(srv.serveMCP))

	handle("GET /api/health", func(*http.Request) (int, any, error) {
		exe, _ := os.Executable()
		return 200, map[string]any{"ok": true, "name": "tick", "version": version, "desktop": srv.desktop, "mcp": srv.origins()[0] + "/mcp", "exe": exe}, nil
	})
	handle("GET /api/sessions", func(r *http.Request) (int, any, error) {
		return 200, st.Sessions(r.URL.Query().Get("archived") == "1"), nil
	})
	handle("POST /api/sessions", func(r *http.Request) (int, any, error) {
		var in StartInput
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, attached, err := st.Start(in)
		if err != nil {
			return 0, nil, err
		}
		status := 201
		if attached {
			status = 200
		}
		return status, struct {
			*Session
			Attached bool `json:"attached"`
		}{s, attached}, nil
	})
	handle("GET /api/sessions/{id}", func(r *http.Request) (int, any, error) {
		s, err := st.Session(r.PathValue("id"))
		return 200, s, err
	})
	handle("PATCH /api/sessions/{id}", func(r *http.Request) (int, any, error) {
		var in PatchInput
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, err := st.Patch(r.PathValue("id"), in)
		return 200, s, err
	})
	handle("PUT /api/sessions/{id}/items", func(r *http.Request) (int, any, error) {
		var in struct{ Items []json.RawMessage }
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, err := st.ReplacePlan(r.PathValue("id"), in.Items)
		return 200, s, err
	})
	handle("POST /api/sessions/{id}/items", func(r *http.Request) (int, any, error) {
		var in struct {
			Items []json.RawMessage
			Text  *string
		}
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, err := st.AppendItems(r.PathValue("id"), in.Items, in.Text)
		return 200, s, err
	})
	handle("PATCH /api/sessions/{id}/items/{ref}", func(r *http.Request) (int, any, error) {
		var in ItemPatch
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, err := st.PatchItem(r.PathValue("id"), r.PathValue("ref"), in)
		return 200, s, err
	})
	handle("DELETE /api/sessions/{id}", func(r *http.Request) (int, any, error) {
		ids, err := st.Delete(r.PathValue("id"))
		return 200, map[string][]string{"deleted": ids}, err
	})
	handle("DELETE /api/sessions/{id}/items/{ref}", func(r *http.Request) (int, any, error) {
		s, err := st.DeleteItem(r.PathValue("id"), r.PathValue("ref"))
		return 200, s, err
	})
	handle("POST /api/sessions/{id}/log", func(r *http.Request) (int, any, error) {
		var in struct{ Text *string }
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		s, err := st.Log(r.PathValue("id"), in.Text)
		return 200, s, err
	})
	handle("POST /api/desktop/panel", func(r *http.Request) (int, any, error) {
		var in struct{ Session *string }
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		if srv.showPanel == nil {
			return 0, nil, &apiError{404, "没有桌面端，在浏览器打开 " + srv.origins()[0]}
		}
		hash := ""
		if in.Session != nil && *in.Session != "" {
			hash = "#" + *in.Session
		}
		srv.showPanel(hash)
		return 200, map[string]bool{"ok": true}, nil
	})
	handle("POST /api/desktop/restart", func(*http.Request) (int, any, error) {
		if srv.restart == nil {
			return 0, nil, &apiError{404, "没有桌面端，手动重启服务即可"}
		}
		go srv.restart()
		return 200, map[string]bool{"ok": true}, nil
	})
	handle("POST /api/desktop/show", func(*http.Request) (int, any, error) {
		if srv.showFloat == nil {
			return 0, nil, &apiError{404, "没有桌面悬浮窗（服务以 -headless 启动）"}
		}
		srv.showFloat()
		return 200, map[string]bool{"ok": true}, nil
	})
	handle("GET /api/notices", func(*http.Request) (int, any, error) {
		return 200, st.Notices(), nil
	})
	handle("POST /api/notices", func(r *http.Request) (int, any, error) {
		var in NotifyInput
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		n, err := st.Notify(in)
		return 201, n, err
	})
	handle("POST /api/notices/read", func(r *http.Request) (int, any, error) {
		var in ReadInput
		if err := decode(r, &in); err != nil {
			return 0, nil, err
		}
		n, err := st.ReadNotices(in)
		return 200, map[string]int{"updated": n}, err
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "没有这个接口：" + r.Method + " " + r.URL.Path})
	})
	return srv.guard(mux)
}

func (srv *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "不支持推送"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := srv.store.Subscribe()
	defer cancel()
	fmt.Fprintf(w, "retry: 3000\nevent: hello\ndata: {\"version\":%q}\n\n", version)
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case f := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f.Event, f.Data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
