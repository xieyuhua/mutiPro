package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// NotifyEvent 是需要推送的事件类型。
type NotifyEvent string

const (
	EventCrash       NotifyEvent = "crash"        // 运行中异常退出
	EventStartFailed NotifyEvent = "startFailed"  // 启动后立即失败
	EventHung        NotifyEvent = "hung"         // 进程存活但无响应
	EventScheduled   NotifyEvent = "scheduled"    // 定时任务触发
)

func (e NotifyEvent) Label() string {
	switch e {
	case EventCrash:
		return "异常退出"
	case EventStartFailed:
		return "启动失败"
	case EventHung:
		return "进程无响应"
	case EventScheduled:
		return "定时任务"
	default:
		return string(e)
	}
}

// 群机器人平台。
const (
	RobotWeCom  = "wecom"  // 企业微信群机器人
	RobotDingTalk = "ding" // 钉钉群机器人
)

// 企业微信群机器人单条消息有 4096 字节上限，留出余量。
const wecomMaxBytes = 3800

// NotifyConfig 是机器人通知配置。全局一份，程序可覆盖 webhook。
type NotifyConfig struct {
	Enabled bool `json:"enabled"`

	// 平台：wecom 或 ding。两者 Webhook 格式不同，需要明确指定。
	Platform string `json:"platform"`

	// Webhook 完整的机器人地址（含 access_token）。
	Webhook string `json:"webhook"`
	// Secret 钉钉机器人的加签密钥；企业微信不使用。
	Secret string `json:"secret"`

	// 各事件是否推送。
	OnCrash       bool `json:"onCrash"`
	OnStartFailed bool `json:"onStartFailed"`
	OnHung        bool `json:"onHung"`
	OnScheduled   bool `json:"onScheduled"`

	// IncludeLogTail 是否在消息里附带日志尾部。
	IncludeLogTail bool `json:"includeLogTail"`
	// LogTailLines 附带多少行日志。
	LogTailLines int `json:"logTailLines"`
}

func defaultNotifyConfig() NotifyConfig {
	return NotifyConfig{
		Enabled:        false,
		Platform:       RobotWeCom,
		OnCrash:        true,
		OnStartFailed:  true,
		OnHung:         true,
		OnScheduled:    false, // 定时任务通常很频繁，默认不发
		IncludeLogTail: true,
		LogTailLines:   10,
	}
}

// wants 判断某事件是否启用推送。
// wants 判断某事件是否启用推送。
//
// program 用于解析实际生效的 Webhook：全局地址为空时，
// 配置了专属地址的程序仍然应该能发送通知。
func (c *NotifyConfig) wants(event NotifyEvent, program *ProgramConfig) bool {
	if !c.Enabled {
		return false
	}
	// 实际生效的地址为空时无法投递。
	if strings.TrimSpace(c.effectiveWebhook(program)) == "" {
		return false
	}
	switch event {
	case EventCrash:
		return c.OnCrash
	case EventStartFailed:
		return c.OnStartFailed
	case EventHung:
		return c.OnHung
	case EventScheduled:
		return c.OnScheduled
	}
	return false
}

// effectiveWebhook 返回该程序实际使用的 webhook。
// 程序单独配置了就用程序的，否则用全局默认。
func (c *NotifyConfig) effectiveWebhook(program *ProgramConfig) string {
	if program != nil {
		if custom := strings.TrimSpace(program.NotifyWebhook); custom != "" {
			return custom
		}
	}
	return strings.TrimSpace(c.Webhook)
}

// NotifyMessage 是一条待发送的通知。
type NotifyMessage struct {
	Event     NotifyEvent
	Program   string
	Detail    string
	LogTail   []string
	OccurredAt time.Time
	Hostname  string
}

// buildText 组装最终发送的纯文本内容。
func (m NotifyMessage) buildText(cfg *NotifyConfig) string {
	var sb strings.Builder
	sb.WriteString("【MultiPro】")
	sb.WriteString(m.Event.Label())
	sb.WriteString("\n程序：")
	sb.WriteString(m.Program)
	if m.Detail != "" {
		sb.WriteString("\n详情：")
		sb.WriteString(m.Detail)
	}
	sb.WriteString("\n时间：")
	sb.WriteString(m.OccurredAt.Format("2006-01-02 15:04:05"))
	if m.Hostname != "" {
		sb.WriteString("\n主机：")
		sb.WriteString(m.Hostname)
	}

	if cfg.IncludeLogTail && len(m.LogTail) > 0 {
		limit := cfg.LogTailLines
		if limit <= 0 {
			limit = 10
		}
		tail := m.LogTail
		if len(tail) > limit {
			tail = tail[len(tail)-limit:]
		}
		sb.WriteString("\n最近日志：\n")
		sb.WriteString(truncateTail(tail, cfg))
	}
	return sb.String()
}

// truncateTail 拼接日志尾部，并保证总长度不超过企业微信的限制。
// 从最新的一行往前加，保证最相关的日志优先保留。
func truncateTail(lines []string, cfg *NotifyConfig) string {
	const budget = wecomMaxBytes - 600 // 留出摘要部分的空间

	var selected []string
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		size := len(line) + 1
		if total+size > budget {
			break
		}
		selected = append(selected, line)
		total += size
	}
	// 上面的循环是倒序收集，反转回正序。
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return strings.Join(selected, "\n")
}

// Notifier 负责把消息投递到 Webhook。
//
// 发送在网络层可能阻塞，因此都带超时；失败只记录日志，绝不因为
// 通知失败影响被管理的程序。
type Notifier struct {
	mu     sync.Mutex
	client *http.Client
	// 上次发送失败的时间，用于避免持续刷错误。
	lastErrorAt time.Time
}

func newNotifier() *Notifier {
	return &Notifier{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send 发送一条通知。失败返回错误，由调用方决定如何记录。
func (n *Notifier) Send(cfg *NotifyConfig, program *ProgramConfig, msg NotifyMessage) error {
	webhook := cfg.effectiveWebhook(program)
	if webhook == "" {
		return fmt.Errorf("未配置 Webhook 地址")
	}

	text := msg.buildText(cfg)
	platform := strings.ToLower(strings.TrimSpace(cfg.Platform))
	if program != nil && strings.TrimSpace(program.NotifyPlatform) != "" {
		platform = strings.ToLower(strings.TrimSpace(program.NotifyPlatform))
	}

	switch platform {
	case RobotDingTalk:
		return n.sendDingTalk(webhook, cfg.Secret, text)
	default:
		return n.sendWeCom(webhook, text)
	}
}

// sendWeCom 发送企业微信群机器人消息。
func (n *Notifier) sendWeCom(webhook, text string) error {
	payload := map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": text},
	}
	return n.postJSON(webhook, payload, "errcode")
}

// sendDingTalk 发送钉钉群机器人消息。
//
// 钉钉要求用签名后的地址；未启用加签的机器人可以留空 Secret。
func (n *Notifier) sendDingTalk(webhook, secret, text string) error {
	target := webhook
	if strings.TrimSpace(secret) != "" {
		signed, err := signDingTalkURL(webhook, secret, time.Now())
		if err != nil {
			return err
		}
		target = signed
	}
	payload := map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": text},
	}
	return n.postJSON(target, payload, "errcode")
}

// signDingTalkURL 计算钉钉加签参数。
//
// 算法：timestamp + "\n" + secret 做 HMAC-SHA256，Base64 后再 URL 编码。
func signDingTalkURL(webhook, secret string, now time.Time) (string, error) {
	parsed, err := url.Parse(webhook)
	if err != nil {
		return "", err
	}
	timestamp := fmt.Sprintf("%d", now.UnixMilli())
	stringToSign := timestamp + "\n" + secret

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	signature := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	query := parsed.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", signature)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// postJSON 发送请求并检查平台返回的错误码。
func (n *Notifier) postJSON(target string, payload map[string]interface{}, codeField string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := n.client.Post(target, "application/json; charset=utf-8", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	// 企业微信与钉钉都用 errcode=0 表示成功。
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		// 返回体不是 JSON 时，HTTP 200 也算成功。
		return nil
	}
	if code, ok := result[codeField]; ok {
		if codeValue, ok := code.(float64); ok && codeValue != 0 {
			message, _ := result["errmsg"].(string)
			return fmt.Errorf("机器人返回错误 %v：%s", codeValue, message)
		}
	}
	return nil
}
