package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
)

// ──────────────────────────── 设备 token 注册表 + 订阅状态（镜像 Python load/save_tokens / load/save_subscribe_status）────────────────────────────
// 全部 fileMu 守卫（register async/goroutine + 推送 goroutine 跨线程读写，全量 load/save 并发要锁防半写）。
// JSON 用 MarshalIndent 2 空格（= Python indent=2）；Go UTF-8 原生（= Python ensure_ascii=False）。
// 注：Go json.Marshal 对 map key 排序（字典序），Python 保插入序——push_tokens.json 是本地文件非线契约，
// key 顺序差异无功能影响（仅多设备时文件字节序不同）。语义等价即可。

// loadTokens — {device_id: push_token}。文件不存在/解析失败返回空 map（对齐 Python FileNotFoundError→{}）。
func loadTokens() map[string]string {
	fileMu.Lock()
	defer fileMu.Unlock()
	data, err := os.ReadFile(pushTokensFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[tokens] 读取失败：%v\n", err)
		}
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return map[string]string{}
	}
	if m == nil {
		return map[string]string{}
	}
	return m
}

// saveTokens — 写 push_tokens.json（indent=2）。fileMu 守卫。
func saveTokens(m map[string]string) {
	fileMu.Lock()
	defer fileMu.Unlock()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Printf("[tokens] 序列化失败：%v\n", err)
		return
	}
	if err := os.WriteFile(pushTokensFile, data, 0644); err != nil {
		log.Printf("[tokens] 写入失败：%v\n", err)
	}
}

// loadSubscribeStatus — {device_id: bool}。未记录的设备 sendToHuawei 视为订阅（默认 true，不破坏老设备/首装未上报）。
func loadSubscribeStatus() map[string]bool {
	fileMu.Lock()
	defer fileMu.Unlock()
	data, err := os.ReadFile(subscribeStatusFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[subscribe] 读取失败：%v\n", err)
		}
		return map[string]bool{}
	}
	var m map[string]bool
	if json.Unmarshal(data, &m) != nil {
		return map[string]bool{}
	}
	if m == nil {
		return map[string]bool{}
	}
	return m
}

// saveSubscribeStatus — 写 subscribe_status.json（indent=2）。fileMu 守卫。
func saveSubscribeStatus(m map[string]bool) {
	fileMu.Lock()
	defer fileMu.Unlock()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Printf("[subscribe] 序列化失败：%v\n", err)
		return
	}
	if err := os.WriteFile(subscribeStatusFile, data, 0644); err != nil {
		log.Printf("[subscribe] 写入失败：%v\n", err)
	}
}

// loadLastMsgID — 读 last_msg_id（高水位持久化，重启续传）。无文件/解析错 → 0。
func loadLastMsgID() int64 {
	fileMu.Lock()
	defer fileMu.Unlock()
	data, err := os.ReadFile(lastMsgIDFile)
	if err != nil {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// saveLastMsgID — 写 last_msg_id（forward 每推一条更新；重启不丢水位）。fileMu 守卫。
func saveLastMsgID(id int64) {
	fileMu.Lock()
	defer fileMu.Unlock()
	if err := os.WriteFile(lastMsgIDFile, []byte(strconv.FormatInt(id, 10)), 0644); err != nil {
		log.Printf("[Gotify] last_msg_id 写入失败：%v\n", err)
	}
}
