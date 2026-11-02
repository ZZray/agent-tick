package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private", "gateway.token")
	token, err := gatewayToken(file)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(token); err != nil || len(raw) != 32 {
		t.Fatalf("generated token must contain 32 random bytes: length=%d err=%v", len(token), err)
	}
	if again, err := gatewayToken(file); err != nil || again != token {
		t.Fatalf("existing token was not preserved: %v", err)
	}
	for _, invalid := range []string{"", "short", strings.Repeat("a", 31) + " " + "b", strings.Repeat("a", 32) + "\nb", strings.Repeat("a", 257), strings.Repeat("a", 32) + "中"} {
		if err := os.WriteFile(file, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := gatewayToken(file); err == nil {
			t.Fatalf("invalid token accepted: length=%d", len(invalid))
		}
	}
}

func gatewayRequest(t *testing.T, base, method, path, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, b
}

func TestGatewayAccessBoundaries(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	gateway := httptest.NewServer(gatewayHandler(port, strings.Repeat("t", 64)))
	defer gateway.Close()
	for _, tc := range []struct {
		name, method, path, auth, origin string
		want                             int
	}{
		{"missing token", "POST", "/mcp", "", "", 401},
		{"wrong token", "POST", "/mcp", "Bearer wrong", "", 401},
		{"browser origin", "POST", "/mcp", "Bearer " + strings.Repeat("t", 64), "http://example.com", 403},
		{"sessions", "POST", "/api/sessions", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"desktop", "POST", "/api/desktop/restart", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"skills", "POST", "/api/skills", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"webpage", "GET", "/", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"query", "POST", "/mcp?token=secret", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"encoded path", "POST", "/%6dcp", "Bearer " + strings.Repeat("t", 64), "", 404},
		{"MCP GET", "GET", "/mcp", "Bearer " + strings.Repeat("t", 64), "", 405},
		{"MCP PUT", "PUT", "/mcp", "Bearer " + strings.Repeat("t", 64), "", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, headers, _ := gatewayRequest(t, gateway.URL, tc.method, tc.path, "{}", map[string]string{"Authorization": tc.auth, "Origin": tc.origin, "Content-Type": "application/json"})
			if status != tc.want {
				t.Fatalf("got HTTP %d, want %d", status, tc.want)
			}
			if tc.want == 401 && headers.Get("WWW-Authenticate") == "" {
				t.Fatal("unauthorized response has no Bearer challenge")
			}
		})
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("rejected requests reached upstream %d times", got)
	}
}

func TestGatewayForwardsMCPHeaders(t *testing.T) {
	var upstreamHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != upstreamHost || r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "" || r.Header.Get("Mcp-Session-Id") != "m_existing" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Errorf("upstream contract changed: host=%q path=%q authPresent=%v session=%q protocol=%q", r.Host, r.URL.Path, r.Header.Get("Authorization") != "", r.Header.Get("Mcp-Session-Id"), r.Header.Get("MCP-Protocol-Version"))
		}
		w.Header().Set("Mcp-Session-Id", "m_returned")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	upstreamHost = fmt.Sprintf("127.0.0.1:%d", port)
	gateway := httptest.NewServer(gatewayHandler(port, strings.Repeat("t", 64)))
	defer gateway.Close()
	for _, method := range []string{"POST", "DELETE"} {
		status, headers, _ := gatewayRequest(t, gateway.URL, method, "/mcp", "{}", map[string]string{"Authorization": "Bearer " + strings.Repeat("t", 64), "Mcp-Session-Id": "m_existing", "MCP-Protocol-Version": "2025-06-18", "Content-Type": "application/json"})
		if status != 202 || headers.Get("Mcp-Session-Id") != "m_returned" {
			t.Fatalf("MCP response was not preserved: status=%d session=%q", status, headers.Get("Mcp-Session-Id"))
		}
	}
}

func TestGatewayUnavailableUpstream(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	gateway := httptest.NewServer(gatewayHandler(port, strings.Repeat("t", 64)))
	defer gateway.Close()
	status, _, body := gatewayRequest(t, gateway.URL, "POST", "/mcp", "{}", map[string]string{"Authorization": "Bearer " + strings.Repeat("t", 64), "Content-Type": "application/json"})
	if status != 502 || !strings.Contains(string(body), "Tick MCP") {
		t.Fatalf("expected actionable 502: status=%d body=%s", status, body)
	}
}

func TestGatewayRealMCPFlow(t *testing.T) {
	h := newHarness(t, t.TempDir())
	gateway := httptest.NewServer(gatewayHandler(h.srv.port, strings.Repeat("t", 64)))
	defer gateway.Close()
	session := ""
	rpc := func(method string, params any) (int, map[string]any) {
		t.Helper()
		b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		status, headers, raw := gatewayRequest(t, gateway.URL, "POST", "/mcp", string(b), map[string]string{"Authorization": "Bearer " + strings.Repeat("t", 64), "Content-Type": "application/json", "Mcp-Session-Id": session, "MCP-Protocol-Version": "2025-06-18"})
		if got := headers.Get("Mcp-Session-Id"); got != "" {
			session = got
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("invalid RPC body: %s (%v)", raw, err)
		}
		return status, result
	}
	status, init := rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]string{"name": "codex-remote"}})
	if status != 200 || session == "" || init["error"] != nil {
		t.Fatalf("initialize failed: %d %v", status, init)
	}
	tool := func(name string, args map[string]any) string {
		t.Helper()
		status, response := rpc("tools/call", map[string]any{"name": name, "arguments": args})
		if status != 200 || response["error"] != nil {
			t.Fatalf("%s failed: %d %v", name, status, response)
		}
		result := response["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatalf("%s returned tool error: %v", name, result)
		}
		return result["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	text := tool("tick_start", map[string]any{"title": "远端网关测试", "items": []string{"确认接入"}, "conversation_id": "gateway-test-conversation"})
	match := regexp.MustCompile(`session_id: (s_\w+)`).FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatalf("no created session ID: %s", text)
	}
	id := match[1]
	tool("tick_item", map[string]any{"session_id": id, "n": 1, "status": "doing"})
	tool("tick_item", map[string]any{"session_id": id, "n": 1, "status": "done", "note": "网关上报成功"})
	tool("tick_finish", map[string]any{"session_id": id, "summary": "远端完成"})
	s := h.ok("GET", "/api/sessions/"+id, nil)
	if s["agent"] != "codex" || s["status"] != "done" || s["summary"] != "远端完成" || items(s)[0]["status"] != "done" {
		t.Fatalf("local Store does not show remote results: %v", s)
	}
	if err := h.srv.store.Flush(); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, h.dir)
	if persisted := h2.ok("GET", "/api/sessions/"+id, nil); persisted["status"] != "done" {
		t.Fatalf("remote results not persisted: %v", persisted)
	}
	status, _, _ = gatewayRequest(t, gateway.URL, "DELETE", "/mcp", "", map[string]string{"Authorization": "Bearer " + strings.Repeat("t", 64), "Mcp-Session-Id": session})
	if status != 204 {
		t.Fatalf("DELETE failed: %d", status)
	}
	if status, _ := rpc("tools/list", nil); status != 404 {
		t.Fatalf("deleted MCP session still usable: %d", status)
	}
}
