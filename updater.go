package main

// 自动更新：从 GitHub Releases 取最新版，校验 SHA256 后替换 tick.exe，空闲时重启。
// 更新源在编译时写入（-X main.updateRepo=owner/name），环境变量 TICK_UPDATE_REPO 可以临时覆盖；为空就不检查。
// 发布约定（见 .github/workflows/release.yml）：tag 为 vX.Y.Z，资产里有 tick-<os>-<arch>.exe 和 SHA256SUMS。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	version    = "0.4.0" // 发布时由 -X main.version=… 覆盖
	updateRepo = ""      // 发布时由 -X main.updateRepo=owner/name 写入
)

func updateSource() string {
	if r := os.Getenv("TICK_UPDATE_REPO"); r != "" {
		return r
	}
	return updateRepo
}

func assetName() string {
	name := fmt.Sprintf("tick-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// newer 比较 vX.Y.Z；带 -dev 之类后缀的本地版本视为更旧
func newer(remote, local string) bool {
	parse := func(v string) [4]int {
		v = strings.TrimPrefix(v, "v")
		pre := 1
		if i := strings.IndexAny(v, "-+"); i >= 0 {
			v, pre = v[:i], 0
		}
		var out [4]int
		for i, p := range strings.SplitN(v, ".", 3) {
			out[i], _ = strconv.Atoi(p)
		}
		out[3] = pre
		return out
	}
	r, l := parse(remote), parse(local)
	for i := range r {
		if r[i] != l[i] {
			return r[i] > l[i]
		}
	}
	return false
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type release struct {
	Tag    string  `json:"tag_name"`
	Body   string  `json:"body"`
	Assets []asset `json:"assets"`
}

var (
	httpc     = &http.Client{Timeout: 60 * time.Second}
	githubAPI = "https://api.github.com"
	githubWeb = "https://github.com" // 测试里换成本地服务
)

var errNoRelease = errors.New("这个仓库还没有发布版本")

func latestRelease(repo string) (*release, error) {
	req, _ := http.NewRequest("GET", githubAPI+"/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tick/"+version)
	res, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
	case 404:
		return nil, errNoRelease
	case 403, 429:
		// 未登录的 API 每个出口 IP 每小时 60 次，走代理或公司网络时很容易用完。
		// 这时改用网页上的“最新版本”跳转拿 tag，资产地址按发布约定拼出来，不占 API 额度
		return latestViaRedirect(repo)
	default:
		return nil, fmt.Errorf("GitHub 返回 HTTP %d", res.StatusCode)
	}
	var r release
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// latestViaRedirect：github.com/<repo>/releases/latest 会跳到 /releases/tag/<tag>；没有发布时跳到 /releases
func latestViaRedirect(repo string) (*release, error) {
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("HEAD", githubWeb+"/"+repo+"/releases/latest", nil)
	req.Header.Set("User-Agent", "tick/"+version)
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode/100 != 3 || loc == "" {
		return nil, fmt.Errorf("GitHub 限流，网页也没拿到最新版本（HTTP %d）", res.StatusCode)
	}
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if !ok || tag == "" || strings.Contains(tag, "/") {
		return nil, errNoRelease
	}
	base := githubWeb + "/" + repo + "/releases/download/" + tag + "/"
	return &release{Tag: tag, Assets: []asset{{assetName(), base + assetName()}, {"SHA256SUMS", base + "SHA256SUMS"}}}, nil
}

func download(url string, w io.Writer, limit int64) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "tick/"+version)
	res, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("下载 HTTP %d", res.StatusCode)
	}
	_, err = io.Copy(w, io.LimitReader(res.Body, limit))
	return err
}

// checkUpdate 返回新版本号；没有新版返回空串
func checkUpdate() (string, *release, error) {
	repo := updateSource()
	if repo == "" {
		return "", nil, errors.New("这个版本没有配置更新源（自己编译的版本不自动更新）")
	}
	r, err := latestRelease(repo)
	if err != nil {
		return "", nil, err
	}
	if !newer(r.Tag, version) {
		return "", r, nil
	}
	return r.Tag, r, nil
}

// applyUpdate 下载、校验、替换当前程序文件。正在运行的 exe 不能覆盖，但能改名：旧的挪成 .old
func applyUpdate(r *release) error {
	var exeURL, sumURL string
	for _, a := range r.Assets {
		switch a.Name {
		case assetName():
			exeURL = a.URL
		case "SHA256SUMS":
			sumURL = a.URL
		}
	}
	if exeURL == "" || sumURL == "" {
		return fmt.Errorf("发布 %s 里缺少 %s 或 SHA256SUMS", r.Tag, assetName())
	}
	var sums strings.Builder
	if err := download(sumURL, &sums, 64<<10); err != nil {
		return fmt.Errorf("下载校验文件失败：%w", err)
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(sums.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == assetName() {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return errors.New("SHA256SUMS 里没有这个平台的校验值")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	err = download(exeURL, io.MultiWriter(f, h), 200<<20)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("下载失败：%w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return fmt.Errorf("校验不通过（期望 %s，实际 %s），没有替换", want[:12], got[:12])
	}
	_ = os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("挪开旧程序失败：%w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(exe+".old", exe) // 放回去
		return fmt.Errorf("换上新程序失败：%w", err)
	}
	return nil
}

// cleanupOld 启动时删掉上次更新留下的 .old（那时旧进程已经退出）
func cleanupOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
		_ = os.Remove(exe + ".new")
	}
}

func runUpdateCLI(cmd string) int {
	if cmd == "version" {
		src := updateSource()
		if src == "" {
			src = "未配置"
		}
		fmt.Printf("Tick %s（%s/%s，更新源：%s）\n", version, runtime.GOOS, runtime.GOARCH, src)
		return 0
	}
	fmt.Printf("当前版本 %s，正在检查…\n", version)
	tag, r, err := checkUpdate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "检查更新失败：", err)
		return 1
	}
	if tag == "" {
		fmt.Printf("已经是最新版（最新发布 %s）\n", r.Tag)
		return 0
	}
	fmt.Printf("发现新版本 %s，下载中…\n", tag)
	if err := applyUpdate(r); err != nil {
		fmt.Fprintln(os.Stderr, "更新失败：", err)
		return 1
	}
	exe, _ := os.Executable()
	fmt.Printf("已更新到 %s：%s\n", tag, exe)
	// 正在运行的 Tick 让它自己重启到新版
	port := defaultPort()
	if pingTick(port) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/desktop/restart", port), strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if res, err := (&http.Client{Timeout: 3 * time.Second}).Do(req); err == nil {
			res.Body.Close()
			fmt.Println("正在运行的 Tick 会在空闲时重启到新版本")
		}
	}
	return 0
}

// relaunch 启动新版本的自己，旧进程随后退出；新进程会等端口空出来（TICK_RESTART=1）
func relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := execCommand(filepath.Clean(exe))
	cmd.Env = append(os.Environ(), "TICK_RESTART=1")
	return cmd.Start()
}
