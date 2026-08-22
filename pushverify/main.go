// CP1 验证脚本：本地签 service account JWT，直推华为 Push Kit（v3 鸿蒙 / v2 安卓）。
// 独立小程序，不动桥、不动云函数。签名逻辑照抄 CloudFuction/netlify/functions/push.js（Go 复刻）。
//
// 用法：
//   v3 自测（确认本地签名链路对，用现有鸿蒙 token）：
//     go run . -private <private.json> -mode v3 -token <鸿蒙token> -title 测试 -body hi
//   v2 验证（等安卓 token，看错误码判断 JWT 接不接受 / payload 缺啥）：
//     go run . -private <private.json> -mode v2 -token <安卓token> -title 测试 -body hi
//
// v3：照 push.js，准确。v2：按调研 §2 框架写最小 payload（message.android.notification + token），
//     字段/鉴权待用错误码迭代。v2 是否接受鸿蒙那套 service account JWT，正是本脚本要验证的核心。
package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// serviceAccount — private.json 字段（照 push.js 的 sa.*）。
type serviceAccount struct {
	PrivateKey string `json:"private_key"` // RSA PEM（PKCS8/PKCS1）
	KeyID      string `json:"key_id"`      // JWT header.kid
	SubAccount string `json:"sub_account"` // JWT payload.iss
	ProjectID  string `json:"project_id"`  // push API URL 路径用
}

const tokenURI = "https://oauth-login.cloud.huawei.com/oauth2/v3/token" // JWT aud（照 push.js）

func main() {
	privatePath := flag.String("private", "", "private.json 路径（service account）")
	mode := flag.String("mode", "v3", "v3（鸿蒙）| v2（安卓）")
	token := flag.String("token", "", "push token")
	title := flag.String("title", "Hotify 验证", "通知标题")
	body := flag.String("body", "本地签 JWT 推送测试", "通知正文")
	project := flag.String("project", "", "覆盖 projectId（留空用 private.json 的）")
	flag.Parse()

	if *privatePath == "" || *token == "" {
		log.Fatal("必须指定 -private <path> -token <token>")
	}
	raw, err := os.ReadFile(*privatePath)
	if err != nil {
		log.Fatalf("读 private.json: %v", err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		log.Fatalf("解析 private.json: %v", err)
	}
	pid := *project
	if pid == "" {
		pid = sa.ProjectID
	}
	if pid == "" {
		log.Fatal("projectId 为空（private.json 无 project_id，用 -project 指定）")
	}

	jwt, err := signJWT(&sa)
	if err != nil {
		log.Fatalf("签 JWT: %v", err)
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + jwt,
	}
	var (
		url     string
		reqBody []byte
	)
	switch *mode {
	case "v3":
		url = fmt.Sprintf("https://push-api.cloud.huawei.com/v3/%s/messages:send", pid)
		headers["push-type"] = "0"
		obj := map[string]any{
			"target": map[string]any{"token": []string{*token}},
			"payload": map[string]any{
				"notification": map[string]any{
					"category":    "SUBSCRIPTION",
					"title":       *title,
					"body":        *body,
					"clickAction": map[string]any{"actionType": 0},
				},
				"data": "{}",
			},
			"pushOptions": map[string]any{"testMessage": false},
		}
		reqBody, _ = json.Marshal(obj)
	case "v2":
		// 安卓格式（调研 §2 框架）：message.android.notification + token，无 push-type。
		// ⚠️ 先最小 title/body/token，字段/鉴权待错误码迭代。
		url = fmt.Sprintf("https://push-api.cloud.huawei.com/v2/%s/messages:send", pid)
		obj := map[string]any{
			"message": map[string]any{
				"android": map[string]any{
					"notification": map[string]any{"title": *title, "body": *body},
				},
				"token": []string{*token},
			},
		}
		reqBody, _ = json.Marshal(obj)
	default:
		log.Fatalf("未知 mode %q（v3 或 v2）", *mode)
	}

	tokPreview := *token
	if len(tokPreview) > 12 {
		tokPreview = tokPreview[:12] + "..."
	}
	fmt.Printf("→ POST %s  (mode=%s token=%s)\n", url, *mode, tokPreview)

	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		log.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		log.Fatalf("HTTP: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	fmt.Printf("← HTTP %d\n%s\n", resp.StatusCode, string(rb))
}

// signJWT — PS256 签 JWT（照 push.js signJwt）。header{kid,typ,alg} + payload{iss,aud,iat,exp}，无 sub。
func signJWT(sa *serviceAccount) (string, error) {
	now := time.Now().Unix()
	header := map[string]any{"kid": sa.KeyID, "typ": "JWT", "alg": "PS256"}
	payload := map[string]any{"iss": sa.SubAccount, "aud": tokenURI, "iat": now, "exp": now + 3600}
	enc := func(o any) string {
		b, _ := json.Marshal(o)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(header) + "." + enc(payload)
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(signingInput))
	// PSS saltLength = digest 长度（对应 Node RSA_PSS_SALTLEN_DIGEST）。
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAPrivateKey — private.json 的 private_key 是 PEM 字符串（PKCS8 优先，回退 PKCS1）。
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("private_key 不是 PEM 格式")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, fmt.Errorf("私钥非 RSA（PKCS8）")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("私钥解析失败（PKCS1/PKCS8 均失败）")
}
