package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ──────────────────────────── 0-config 自动探测（镜像 Python _probe/_parse_gotify_config / _autodetect_local_gotify / _fetch_cf_urls_from_txt）────────────────────────────

// cloud_function_urls.txt fetch 源（ghproxy.com 优先国内加速 → 直连 fallback）
var cfTxtSources = []string{
	"https://ghproxy.com/https://raw.githubusercontent.com/sakura-lolipop/hotify-bridge/main/cloud_function_urls.txt",
	"https://raw.githubusercontent.com/sakura-lolipop/hotify-bridge/main/cloud_function_urls.txt",
}
const cfTxtCache = "cloud_function_urls.cache.txt"

// cfYamlOverride — bridge_config.yaml 显式填了 cloud_function_urls（手动 override）。
// true → 不走 txt/cache 自动管理（启动跳过 initCfURLs、后台不刷新），尊重部署者手填。
// initConfig 在 applyParsedConfig 后判定（cfg.CloudFunctionURLs 非空 = yaml 填了）。
var cfYamlOverride bool

// cfRefreshInterval — 后台 fetch cloud_function_urls.txt 的间隔（cache-first 启动之上的运行时刷新）。
// 1h：云函数 URL 变动改 txt，桥常驻最多 1h 跟上、免重启；网络挂则保留当前不动。
const cfRefreshInterval = 1 * time.Hour

// autodetectDoneFor — 已【成功】探到同机 Gotify 的 gotify_url（去重）；失败不标记→下次再探。
var autodetectDoneFor atomic.Value // 存 string

type sectionEntry struct {
	name   string
	indent int
}

// gotifyConfigInfo — 从 Gotify config.yml 解析出的关键字段。
type gotifyConfigInfo struct {
	Port       int
	SSLEnabled bool
	CertFile   string
	KeyFile    string
	LEEnabled  bool
}

// parseGotifyConfig — 极简解析 Gotify config.yml（只取 server.port + server.ssl.{enabled,certfile,keyfile,letsencrypt.enabled}）。
// 不用 yaml.v3（桥零额外依赖）——按缩进跟踪 section 路径，提取目标字段。镜像 Python _parse_gotify_config。
func parseGotifyConfig(path string) (gotifyConfigInfo, error) {
	var info gotifyConfigInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	var section []sectionEntry
	for _, line := range strings.Split(string(data), "\n") {
		stripped := strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(stripped)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(stripped) - len(strings.TrimLeft(stripped, " \t"))
		content := strings.TrimLeft(stripped, " \t")
		for len(section) > 0 && indent <= section[len(section)-1].indent {
			section = section[:len(section)-1]
		}
		if strings.HasSuffix(content, ":") {
			section = append(section, sectionEntry{name: strings.TrimSpace(strings.TrimSuffix(content, ":")), indent: indent})
			continue
		}
		if strings.Contains(content, ":") {
			k, v, _ := strings.Cut(content, ":")
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			v = strings.Trim(v, "\"") // 去 "（Python strip('"')）
			v = strings.Trim(v, "'")  // 去 '（Python strip("'")）
			pathStr := sectionPath(section) + "." + k
			switch pathStr {
			case "server.port":
				if p, err := strconv.Atoi(v); err == nil {
					info.Port = p
				}
			case "server.ssl.enabled":
				info.SSLEnabled = strings.ToLower(v) == "true"
			case "server.ssl.certfile":
				info.CertFile = v
			case "server.ssl.keyfile":
				info.KeyFile = v
			case "server.ssl.letsencrypt.enabled":
				info.LEEnabled = strings.ToLower(v) == "true"
			}
		}
	}
	return info, nil
}

func sectionPath(section []sectionEntry) string {
	parts := make([]string, len(section))
	for i, s := range section {
		parts[i] = s.name
	}
	return strings.Join(parts, ".")
}

// probeGotifyConfig — 启动时探同机 Gotify config.yml → 读 port(hint) + 证书 → 自动加载（/register HTTPS）。
// **不设 gotify_url_local**——端口可能不准（Gotify 可能 302 重定向 80→443）→ 交给 autodetectLocalGotify。
// 证书：config ssl.certfile/keyfile → 否则扫 <config_dir>/certs/。5 种情况显式打印。手动 override 优先。
func probeGotifyConfig() {
	cfgMu.RLock()
	cfgPath := cfg.GotifyConfigPath
	cfgMu.RUnlock()
	var candidates []string
	if cfgPath != "" {
		candidates = append(candidates, cfgPath)
	}
	candidates = append(candidates, "../gotify/config.yml", "./gotify/config.yml", "../config.yml")
	found := ""
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			found = c
			break
		}
	}
	if found == "" {
		log.Print("[配置] ⚠ 未找到 Gotify config.yml（尝试了 ../gotify/config.yml 等）。/register 退 HTTP(LAN)。需 HTTPS:bridge_config 配 gotify_config_path 指向 Gotify config，或手填 tls_cert_file/tls_key_file。")
		return
	}
	info, err := parseGotifyConfig(found)
	if err != nil {
		log.Printf("[配置] ⚠ Gotify config 读取失败（%s）:%v。/register 退 HTTP(LAN)。", found, err)
		return
	}
	cfgMu.Lock()
	cfg.GotifyConfigPort = info.Port
	cfg.GotifyConfigSSL = info.SSLEnabled
	cfgMu.Unlock()

	certfile, keyfile := info.CertFile, info.KeyFile
	if certfile == "" || keyfile == "" {
		abs, _ := filepath.Abs(found)
		certsDir := filepath.Join(filepath.Dir(abs), "certs")
		if fi, err := os.Stat(certsDir); err == nil && fi.IsDir() {
			c, k := scanCerts(certsDir)
			if c != "" {
				certfile = c
			}
			if k != "" {
				keyfile = k
			}
		}
	}
	switch {
	case certfile != "" && keyfile != "" && fileExists(certfile) && fileExists(keyfile):
		cfgMu.Lock()
		if cfg.TLSCertFile == "" {
			cfg.TLSCertFile = certfile
		}
		if cfg.TLSKeyFile == "" {
			cfg.TLSKeyFile = keyfile
		}
		cfgMu.Unlock()
		log.Printf("[配置] ✓ 自动加载证书: cert=%s → /register HTTPS（Gotify 端口由探测定）", certfile)
	case info.LEEnabled:
		log.Print("[配置] ⚠ Gotify 用 Let's Encrypt（证书内部管理，私钥不可读）。/register 退 HTTP(LAN)。App 需在家庭 WiFi 注册一次。需公网 HTTPS:手填证书（acme.sh/certbot）或 Caddy 反代。")
	case certfile != "" || keyfile != "":
		log.Printf("[配置] ⚠ 证书文件不存在: cert=%s key=%s。检查路径。/register 退 HTTP(LAN)。", certfile, keyfile)
	default:
		log.Print("[配置] 未找到证书（Gotify config 无 ssl + certs/ 无证书文件）。/register 退 HTTP(LAN)。需 HTTPS:手填 tls_cert_file/tls_key_file。")
	}
}

// scanCerts — 扫 certs/ 目录：*.cer/*.pem/*.crt → cert，*.key → key（各取第一个）。
func scanCerts(dir string) (cert, key string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		switch {
		case strings.HasSuffix(n, ".cer") || strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".crt"):
			if cert == "" {
				cert = filepath.Join(dir, n)
			}
		case strings.HasSuffix(n, ".key"):
			if key == "" {
				key = filepath.Join(dir, n)
			}
		}
	}
	return
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// autodetectLocalGotify — 探同机 Gotify：试 443(HTTPS) → 80(HTTP) → config 端口 → gotify_url 端口。
// 首个 /version 应答 → cfg.GotifyURLLocal。**不信任 config 端口**（Gotify 可能 302 重定向 80→443）。
// 私网 IP 跳 TLS 验证。探成功才标记 done，失败下次 App 上报/启动再探。
func autodetectLocalGotify() {
	cfgMu.RLock()
	local := cfg.GotifyURLLocal
	gu := cfg.GotifyURL
	cfgMu.RUnlock()
	var doneFor string
	if v := autodetectDoneFor.Load(); v != nil {
		doneFor, _ = v.(string)
	}
	if local != "" || doneFor == gu {
		return
	}

	type candidate struct {
		scheme string
		port   int
	}
	var cands []candidate
	cands = append(cands, candidate{"https", 443}, candidate{"http", 80})

	cfgMu.RLock()
	cfgPort := cfg.GotifyConfigPort
	cfgSSL := cfg.GotifyConfigSSL
	cfgMu.RUnlock()
	if cfgPort != 0 && cfgPort != 443 && cfgPort != 80 {
		sch := "http"
		if cfgSSL {
			sch = "https"
		}
		cands = append(cands, candidate{sch, cfgPort})
	}
	if gu != "" {
		if u, err := url.Parse(gu); err == nil {
			if pStr := u.Port(); pStr != "" {
				if p, err := strconv.Atoi(pStr); err == nil && p != 0 && p != 443 && p != 80 && p != cfgPort {
					sch := u.Scheme
					if sch == "" {
						sch = "https"
					}
					cands = append(cands, candidate{sch, p}) // gotify_url 端口也探 127.0.0.1（同机，域名也试——免 hairpin）
				}
			}
		}
	}

	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // localhost 跳过证书校验
	}
	for _, c := range cands {
		localURL := fmt.Sprintf("%s://127.0.0.1:%d", c.scheme, c.port)
		resp, err := client.Get(localURL + "/version")
		if err != nil {
			continue
		}
		var ver struct {
			Version string `json:"version"`
		}
		err2 := json.NewDecoder(resp.Body).Decode(&ver)
		resp.Body.Close()
		if err == nil && resp.StatusCode == 200 && err2 == nil && ver.Version != "" {
			cfgMu.Lock()
			cfg.GotifyURLLocal = localURL
			cfgMu.Unlock()
			mark := gu
			if mark == "" {
				mark = localURL
			}
			autodetectDoneFor.Store(mark)
			log.Printf("[Gotify] 🔍 探到同机 Gotify（%s → %s），自动走 localhost。", localURL, ver.Version)
			return
		}
	}
	// 都没探到：不标记 done，下次 App 上报/启动再探
}

// initCfURLs — 启动配 cloud_function_urls（cache-first；仅 yaml 未 override 时调）。
// 优先级：热启动有 cache → 秒起用 cache（不等网络，后台 refreshCfURLs 取最新）
//        > 冷启动无 cache → 同步 fetch 建缓存（首次必等网络；失败则 cfg 空，后台重试补）。
// yaml override（cfYamlOverride=true）由 initConfig 拦截，不进此函数。
func initCfURLs() {
	// ① 热启动：有 cache 直接用（秒起，不等网络；后台刷新最新）
	if data, err := os.ReadFile(cfTxtCache); err == nil {
		if parsed := parseCfTxt(string(data)); len(parsed) > 0 {
			cfgMu.Lock()
			cfg.CloudFunctionURLs = parsed
			cfgMu.Unlock()
			log.Printf("[配置] ✓ 热启动用 cache（%d 个 URL），后台刷新最新", len(parsed))
			return
		}
	}
	// ② 冷启动：无 cache → 同步 fetch 建缓存（首次必等网络；fetchCfURLsFromTxt 成功写 cache，失败 log 报错、cfg 留空）
	fetchCfURLsFromTxt()
}

// fetchCfURLsFromTxt — cloud_function_urls 空时 → fetch cloud_function_urls.txt（GitHub raw，ghproxy.com 优先国内加速）
// → 按行解析 URL。拉到 → 缓存本地（全挂时用缓存）。已配（bridge_config override）→ 跳过。
// 冷启动用（initCfURLs 无 cache 时调）；后台刷新用 refreshCfURLs（只更新有变化的）。
func fetchCfURLsFromTxt() {
	cfgMu.RLock()
	urls := cfg.CloudFunctionURLs
	cfgMu.RUnlock()
	if len(urls) > 0 {
		return
	}

	client := &http.Client{Timeout: 8 * time.Second}
	for _, src := range cfTxtSources {
		resp, err := client.Get(src)
		if err != nil {
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			continue
		}
		content, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		parsed := parseCfTxt(string(content))
		if len(parsed) > 0 {
			cfgMu.Lock()
			cfg.CloudFunctionURLs = parsed
			cfgMu.Unlock()
			_ = os.WriteFile(cfTxtCache, content, 0644)
			tag := "直连"
			if strings.Contains(src, "ghproxy.com") {
				tag = "ghproxy"
			}
			log.Printf("[配置] ✓ fetch cloud_function_urls.txt（%s）→ %v", tag, parsed)
			return
		}
	}
	// 全挂 → 用缓存
	if data, err := os.ReadFile(cfTxtCache); err == nil {
		parsed := parseCfTxt(string(data))
		if len(parsed) > 0 {
			cfgMu.Lock()
			cfg.CloudFunctionURLs = parsed
			cfgMu.Unlock()
			log.Printf("[配置] ⚠ fetch .txt 全挂,用缓存（%d 个 URL）", len(parsed))
			return
		}
	}
	log.Print("[配置] ⚠ fetch cloud_function_urls.txt 失败（ghproxy + 直连都挂,无缓存）。请手填 cloud_function_urls")
}

// parseCfTxt — 一行一个 URL，跳过空行 + # 注释。
func parseCfTxt(content string) []string {
	var urls []string
	for _, ln := range strings.Split(content, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		urls = append(urls, ln)
	}
	return urls
}

// refreshCfURLs — 单次后台刷新：fetch txt → 与当前 cfg 比 → 变了才更新 cfg + cache（加锁）。
// fetch 失败 / 内容相同 → 不动 cfg（保留当前，避免无谓写 + 抖动）。
func refreshCfURLs() {
	client := &http.Client{Timeout: 8 * time.Second}
	for _, src := range cfTxtSources {
		resp, err := client.Get(src)
		if err != nil {
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			continue
		}
		content, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		parsed := parseCfTxt(string(content))
		if len(parsed) == 0 {
			continue
		}
		cfgMu.RLock()
		cur := cfg.CloudFunctionURLs
		cfgMu.RUnlock()
		if cfListsEqual(cur, parsed) {
			return // 无变化，不动
		}
		cfgMu.Lock()
		cfg.CloudFunctionURLs = parsed
		cfgMu.Unlock()
		_ = os.WriteFile(cfTxtCache, content, 0644)
		tag := "直连"
		if strings.Contains(src, "ghproxy.com") {
			tag = "ghproxy"
		}
		log.Printf("[配置] ↻ 后台刷新 cloud_function_urls（%s）→ %v", tag, parsed)
		return
	}
	log.Print("[配置] ↻ 后台刷新 fetch 全挂，保留当前 cloud_function_urls")
}

// refreshCfURLsPeriodically — 后台 goroutine：定期 fetch txt 刷新（cache-first 之上的运行时跟上）。
// 仅 yaml 未 override 时由 main 启动（cfYamlOverride=false）。立即刷一次 + 每 cfRefreshInterval；随 ctx.Done 退出。
// 热更新对 push 安全：sendToHuawei 入口 RLock 拷出 slice header 再遍历，refresh 替换 cfg.CloudFunctionURLs
//   整体（新 slice），旧 slice 完整、本轮推送跑完不受影响（下一轮 sendToHuawei 才读新值）。
func refreshCfURLsPeriodically(ctx context.Context) {
	refreshCfURLs() // 启动后立即刷一次（冷启动 cache 起来的，这里取最新）
	ticker := time.NewTicker(cfRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			refreshCfURLs()
		case <-ctx.Done():
			return
		}
	}
}

// cfListsEqual — 顺序敏感比较（txt 行序即 fallback 顺序，顺序变了也算更新）。
func cfListsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
