package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ──────────────────────────── 常量（对齐 Python）────────────────────────────
// 文件名与 Python 同名、同 CWD 读写（部署目录里和 Python 桥互换无感）。
const (
	bridgeConfigFile     = "bridge_config.yaml"
	pushTokensFile       = "push_tokens.json"
	subscribeStatusFile  = "subscribe_status.json"
	registerPortDefault  = 8080 // register_port 留空 → 此值（公开发布默认 8080，避开 Gotify 常占的 80/443；自用/测试在 yaml 显式设 register_port 覆盖）
)

// ──────────────────────────── Config（对应 Python _cfg）────────────────────────────
// 一个 struct 装两类：静态项（部署者填，启动读）+ 动态项（gotify_*，App 运行时首注上报、写回锁定）。
// register_port 故意不在 cfgDefaults（读 ad-hoc，留空→默认 8080，且 saveBridgeConfig 不碰它）。
type Config struct {
	// gotify（首注锁定动态项）
	GotifyURL      string // normalize 后（纯端口→http://127.0.0.1:port）
	GotifyToken    string // client token（读消息/订阅流；机密）
	GotifyURLLocal string // 同机覆盖；空 → 用 GotifyURL
	// 静态项（部署者填，启动读）
	TLSCertFile      string // 留空→CP4 自动探 Gotify config ssl.certfile
	TLSKeyFile       string // 同上（ssl.keyfile）
	GotifyConfigPath string // 留空→自动探 ../gotify/config.yml
	RegisterPort     string // 留空→registerPortDefault
	SubscribeLabel   string // "true"/"false"（字符串，真值集 {true,1,yes,on}）
	// 推送服务
	CloudFunctionURLs  []string // yaml 填=override；空→cache-first（热启动用 cache/冷启动 fetch txt）+ 后台每 h 刷新
	CloudFunctionToken string   // AUTH_TOKEN；留空=服务侧没开鉴权（不发 Authorization 头）
	// 运行时探测（不入 yaml；CP4 填）
	GotifyConfigPort int
	GotifyConfigSSL  bool
}

// cfgDefaults — 对应 Python _CFG_DEFAULTS。register_port 不在此（读 ad-hoc）。
func cfgDefaults() Config {
	return Config{
		SubscribeLabel:      "true",
		CloudFunctionURLs:   nil,
		CloudFunctionToken: "hotifypushkit",
	}
}

var (
	cfg    Config        // 运行时配置；initConfig 填，processRegister 改动态项，keepSubscribed 读
	cfgMu  sync.RWMutex  // register 写 / subscriber 读
	fileMu sync.Mutex    // 4 文件 I/O 守卫（对应 Python _file_lock）：tokens/subscribe_status/bridge_config 并发 load/save 防半写
)

// ──────────────────────────── 宽松解析器（镜像 Python load_bridge_config）────────────────────────────
// 每行 `key: value`，value = 第一个冒号后整段（去外层可选引号 + 尾部 ` #` 注释）。
// 反斜杠/冒号/冒号后无空格/引号不配对——全容错（专治 YAML \U 转义、冒号必空格那些坑）。
// 返回 map[string]any（值 string 或 []string）。
func loadBridgeConfig(path string) map[string]any {
	out := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("[配置] ❌ 读取 %s 出错：%v\n", path, err)
		}
		return out
	}
	def := cfgDefaults()
	for _, line := range strings.Split(string(data), "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") || !strings.Contains(line, ":") {
			continue
		}
		key, val, _ := strings.Cut(line, ":") // 首冒号分割（对齐 Python partition(":")）
		key = strings.TrimSpace(key)
		val = strings.SplitN(val, " #", 2)[0] // 去尾部行内注释（空格+#）
		val = strings.TrimSpace(val)
		// 去外层匹配引号（" 或 '，必须首尾同字符且长度≥2）
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[0] == val[len(val)-1] {
			val = val[1 : len(val)-1]
		}
		switch {
		case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
			// list 字面量 → json.Unmarshal；失败保留原字符串（对齐 Python json.loads 失败 pass）
			var arr []string
			if json.Unmarshal([]byte(val), &arr) == nil {
				out[key] = arr
			} else {
				out[key] = val
			}
		case isListDefaultKey(key, def) && val != "":
			// 默认是 list 但值是普通字符串 → 宽松归一化（剥 brackets/引号/空白 → 单元素 list）
			// 处理 malformed 如 `"url"]`（缺 [）；多 URL 须用 ["a","b"]（走上面的 json 路径）
			trimmed := strings.Trim(val, "[]\"' \t")
			if trimmed != "" {
				out[key] = []string{trimmed}
			} else {
				out[key] = []string{}
			}
		default:
			out[key] = val
		}
	}
	return out
}

// isListDefaultKey — 该 key 在 defaults 里是 list 类型（仅 cloud_function_urls）。
func isListDefaultKey(key string, def Config) bool {
	return key == "cloud_function_urls"
}

// ──────────────────────────── 点更新写入器（镜像 Python save_bridge_config）────────────────────────────
// App 上报后持久化动态项：只替换 gotify_url / gotify_token 两行的值，其余（静态项 + # 注释）原样保留。
// 不整文件重写——避免丢注释、避免"动"踩"静"。fileMu 守卫。
func saveBridgeConfig() {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfgMu.RLock()
	gurl, gtok := cfg.GotifyURL, cfg.GotifyToken
	cfgMu.RUnlock()

	// 读全行（保留行尾 \n，对齐 Python readlines）
	content := ""
	if data, err := os.ReadFile(bridgeConfigFile); err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		fmt.Printf("[配置] save 读取失败：%v\n", err)
		return
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n" // 确保末尾有换行（SplitAfter 处理一致）
	}
	lines := strings.SplitAfter(content, "\n") // 保留 \n 在每行尾
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // 去掉末尾空元素（SplitAfter 的副作用）
	}

	out, wroteURL, wroteTok := []string{}, false, false
	for _, line := range lines {
		key := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(key, "gotify_url:"):
			out = append(out, fmt.Sprintf("gotify_url: %s\n", gurl))
			wroteURL = true
		case strings.HasPrefix(key, "gotify_token:"):
			out = append(out, fmt.Sprintf("gotify_token: %s\n", gtok))
			wroteTok = true
		default:
			out = append(out, line) // 原样（含 \n）
		}
	}
	if !wroteURL { // 文件里没这行（罕见）→ 追加
		out = append(out, fmt.Sprintf("gotify_url: %s\n", gurl))
	}
	if !wroteTok {
		out = append(out, fmt.Sprintf("gotify_token: %s\n", gtok))
	}
	if err := os.WriteFile(bridgeConfigFile, []byte(strings.Join(out, "")), 0644); err != nil {
		fmt.Printf("[配置] save 写入失败：%v\n", err)
	}
}

// ──────────────────────────── 智能地址（镜像 Python normalize_gotify_addr）────────────────────────────
// 纯端口→http://127.0.0.1:端口（同机，最快、免 TLS）；没带协议补 http://；完整地址原样。
func normalizeGotifyAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if isAllDigit(raw) { // 纯端口 → 同机走 127.0.0.1
		return "http://127.0.0.1:" + raw
	}
	if !strings.Contains(raw, "://") { // 没带协议 → 补 http://
		return "http://" + raw
	}
	return raw
}

func isAllDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ──────────────────────────── 首次 seed（镜像 Python _seed_config_file）────────────────────────────
// bridge_config.yaml 不存在 → 生成带注释的一份（部署者直接改）。注释内嵌于此，无 example 模板文件。
func seedConfigFile() {
	d := cfgDefaults()
	cfURLs := "[]" // 空列表渲染为 []（对齐 Python str([])）
	content := fmt.Sprintf(`# bridge_config.yaml — Hotify 桥配置（首次自动生成，按需修改）
# 格式宽松：每行 `+"`键: 值`"+`，值 = 冒号后整段（反斜杠/冒号原样，引号可选）。# 开头是注释。
# 必填：gotify_token + cloud_function_urls。其余有默认值或自动探测。详见 BRIDGE.md / repourl.md

# Gotify 地址（App 视角）。完整地址 https://你的域名:端口（远程/域名）；或只填端口→同机明文。App 上报会覆盖。
gotify_url: %s

# Gotify client token（读消息 / 订阅流；机密，别提交 git）
gotify_token: %s

# 桥连 Gotify 的本地地址，覆盖上面的 gotify_url。同机 TLS Gotify 填 https://127.0.0.1:端口（自动跳过证书校验、免 hairpin）；留空→用 gotify_url
gotify_url_local: %s

# /register 监听端口。留空→默认 8080（启动 ⚠️ 提醒）；自用/测试填 25238 等 override
register_port:

# Gotify config.yml 路径（留空→自动探 ../gotify/config.yml 等同机路径）。桥启动读它自动加载证书 + Gotify 端口（0 配置）。
gotify_config_path: %s

# TLS 证书/私钥【文件路径】。留空→自动从 Gotify config 读 ssl.certfile/keyfile（0 配置）；手填=override（如 LE 或非同机）。
tls_cert_file: %s
tls_key_file: %s

# 订阅类字样标注开关。true（默认）= 转发时给标题加"订阅:"前缀（如"订阅:短信验证码"）；false = 不加。
subscribe_label: %s

# 推送服务入口（桥不直连 Push Kit，HTTP POST 推送服务；private 锁在服务里）。
# 留空 = 自动管理：cache-first 启动（热启动用本地 cache 秒起 / 冷启动 fetch cloud_function_urls.txt）+ 后台每 h 刷新。
#   云函数变动只改仓库 cloud_function_urls.txt，桥常驻最多 1h 跟上、免重启。
# 填了 = 手动 override（不走自动管理，改 URL 改这里；JSON 数组可多个 fallback）。
cloud_function_urls: %s

# 推送服务 AUTH_TOKEN（防爬虫，非防推送）。默认 hotifypushkit（managed）；自托管填你服务侧配的；留空=服务侧没开鉴权。
cloud_function_token: %s
`, d.GotifyURL, d.GotifyToken, d.GotifyURLLocal, d.GotifyConfigPath, d.TLSCertFile, d.TLSKeyFile, d.SubscribeLabel, cfURLs, d.CloudFunctionToken)
	if err := os.WriteFile(bridgeConfigFile, []byte(content), 0644); err != nil {
		fmt.Printf("[配置] 生成 %s 失败：%v\n", bridgeConfigFile, err)
		return
	}
	fmt.Printf("[配置] 未找到 %s，已生成一份（带注释 + 默认值）。\n", bridgeConfigFile)
	fmt.Printf("[配置] ✏️ 请编辑 %s：必填 gotify_token + cloud_function_urls，存盘后重启。\n", bridgeConfigFile)
}

// ──────────────────────────── initConfig（镜像 Python init_config，CP0 仅配置加载）────────────────────────────
// 启动：bridge_config.yaml 不存在则 seed；读配置覆盖默认；normalize gotify 项。
// CP0：仅配置加载。probe/autodetect/cf-txt-fetch 在 CP4 接入（此处留 TODO）。
func initConfig() {
	if _, err := os.Stat(bridgeConfigFile); os.IsNotExist(err) {
		seedConfigFile()
	}
	cfg = cfgDefaults()
	p := loadBridgeConfig(bridgeConfigFile)
	applyParsedConfig(p)
	// gotify 动态项：文件 > env 兜底，统一过 normalize（端口→127.0.0.1）
	gu := getString(p, "gotify_url")
	if gu == "" {
		gu = os.Getenv("GOTIFY_HTTP_URL")
	}
	cfg.GotifyURL = normalizeGotifyAddr(gu)
	gt := getString(p, "gotify_token")
	if gt == "" {
		gt = os.Getenv("GOTIFY_CLIENT_TOKEN")
	}
	cfg.GotifyToken = gt
	cfg.GotifyURLLocal = normalizeGotifyAddr(getString(p, "gotify_url_local"))
	// 0-config 自动探测（CP4）
	probeGotifyConfig()      // 探 Gotify config.yml → 自动证书 + 端口 hint
	autodetectLocalGotify()  // 留空则探同机 Gotify（/version probe）
	// cloud_function_urls：yaml 填了 = 手动 override（不动）；留空 = txt/cache 自动管理（cache-first + 后台刷新）
	cfYamlOverride = len(cfg.CloudFunctionURLs) > 0
	if !cfYamlOverride {
		initCfURLs()
	}
}

// applyParsedConfig — 用解析出的 map 覆盖 cfg 静态项（对应 Python _cfg.update(p) 的静态部分）。
// gotify_* 不在此设（initConfig 单独 normalize + env 兜底）。
func applyParsedConfig(p map[string]any) {
	if v, ok := p["tls_cert_file"].(string); ok {
		cfg.TLSCertFile = v
	}
	if v, ok := p["tls_key_file"].(string); ok {
		cfg.TLSKeyFile = v
	}
	if v, ok := p["gotify_config_path"].(string); ok {
		cfg.GotifyConfigPath = v
	}
	if v, ok := p["register_port"].(string); ok {
		cfg.RegisterPort = v
	}
	if v, ok := p["subscribe_label"].(string); ok {
		cfg.SubscribeLabel = v
	}
	if v, ok := p["cloud_function_token"].(string); ok {
		cfg.CloudFunctionToken = v
	}
	if v, ok := p["cloud_function_urls"].([]string); ok {
		cfg.CloudFunctionURLs = v
	}
}

// getString — map 取 string，不存在/类型不符返回 ""。
func getString(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// mask — token 脱敏日志（前 12 字符 + ...，对齐 Python push_token[:12]...）。
func mask(s string) string {
	if len(s) <= 12 {
		return strings.Repeat("*", len(s))
	}
	return s[:12] + "..."
}
