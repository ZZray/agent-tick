package main

// 独立的远端 MCP 入口：鉴权后只转发 /mcp，本机会话和桌面 API 仍留在环回地址。

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func gatewayToken(file string) (string, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return "", err
		}
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		b = []byte(hex.EncodeToString(b))
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(b)
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	} else if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("令牌文件需要 32–256 个非空白 ASCII 字符")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return "", errors.New("令牌文件只能包含可打印 ASCII 字符")
		}
	}
	return token, nil
}

func gatewayHandler(port int, token string) http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", fmt.Sprint(port))}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host // 本机 guard 的 Host 契约保持不变
			r.Out.Header.Del("Authorization")
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "本机 Tick MCP 不可用，请检查 Tick 是否正在运行"})
		},
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="Tick"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "需要有效的 Tick 接入令牌"})
			return
		}
		if r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "此入口仅供 Agent 连接，不接受网页 Origin"})
			return
		}
		if r.URL.Path != "/mcp" || r.URL.RawPath != "" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func runGateway(args []string) int {
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:7358", "绑定的具体本机 IP:端口")
	tokenFile := flags.String("token-file", "", "私有令牌文件；不存在时自动创建，不打印令牌")
	port := flags.Int("port", defaultPort(), "转发到本机 Tick 端口")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	ip, _, err := net.SplitHostPort(*listen)
	addr := net.ParseIP(ip)
	if err != nil || addr == nil || addr.IsUnspecified() || *tokenFile == "" || *port < 1 || *port > 65535 || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "用法：tick gateway --listen <具体本机IP:7358> --token-file <私有令牌文件> [--port 7357]")
		return 1
	}
	token, err := gatewayToken(*tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取接入令牌失败：", err)
		return 1
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动 Tick 接入失败：", err)
		return 1
	}
	fmt.Printf("Tick Agent 接入：http://%s/mcp；令牌文件：%s\n", *listen, *tokenFile)
	srv := &http.Server{Handler: gatewayHandler(*port, token), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "Tick 接入已退出：", err)
		return 1
	}
	return 0
}
