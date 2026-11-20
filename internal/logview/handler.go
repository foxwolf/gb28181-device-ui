// Package logview 把 log/slog 接到界面日志窗，并顺带从库日志推断注册/心跳/INVITE/RTP 状态。
package logview

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const maxLines = 2000

// Line 是一条带时间戳的日志。
type Line struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Text    string // 已格式化、可供直接显示
}

// Session 是从库日志与少量导出 API 拼出的会话状态。
type Session struct {
	Register    string
	Heartbeat   string
	Invite      string
	RTP         string
	PlatformVer string
	PlatformAt  string // 平台 Date 头的可读时间
	BoundPort   string
}

// Buffer 是线程安全的环形日志缓冲，并实现 slog.Handler。
type Buffer struct {
	mu       sync.Mutex
	lines    []Line
	session  Session
	secret   string
	onChange func()
	level    slog.Level
}

func New(onChange func()) *Buffer {
	return &Buffer{
		lines:    make([]Line, 0, 256),
		onChange: onChange,
		level:    slog.LevelInfo,
		session: Session{
			Register:  "—",
			Heartbeat: "—",
			Invite:    "—",
			RTP:       "—",
		},
	}
}

// SetSecret 设置需要从日志中抹去的 SIP 密码（可为空）。
func (b *Buffer) SetSecret(s string) {
	b.mu.Lock()
	b.secret = s
	b.mu.Unlock()
}

// ResetSession 在重新启动前清空会话状态（保留日志）。
func (b *Buffer) ResetSession() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.session = Session{
		Register:  "尚未注册",
		Heartbeat: "—",
		Invite:    "—",
		RTP:       "—",
	}
}

// SetPlatformInfo 写入库导出的平台协议版本与时钟。
func (b *Buffer) SetPlatformInfo(ver string, dateUnix int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := false
	if ver != "" && ver != b.session.PlatformVer {
		b.session.PlatformVer = ver
		changed = true
	}
	if dateUnix > 0 {
		text := time.Unix(dateUnix, 0).Local().Format("2006-01-02 15:04:05")
		if text != b.session.PlatformAt {
			b.session.PlatformAt = text
			changed = true
		}
	}
	if changed && b.onChange != nil {
		go b.onChange()
	}
}

// SetBoundPort 记录实际绑定的本地 SIP 端口。
func (b *Buffer) SetBoundPort(port int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := fmt.Sprintf("%d", port)
	if b.session.BoundPort == text {
		return
	}
	b.session.BoundPort = text
	if b.onChange != nil {
		go b.onChange()
	}
}

func (b *Buffer) Session() Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session
}

func (b *Buffer) Lines() []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Line, len(b.lines))
	copy(out, b.lines)
	return out
}

// Dump 返回当前窗口中的全部日志文本（已脱敏），便于复制到剪贴板。
func (b *Buffer) Dump() string {
	lines := b.Lines()
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line.Text)
	}
	return out.String()
}

func (b *Buffer) Enabled(_ context.Context, level slog.Level) bool {
	return level >= b.level
}

func (b *Buffer) Handle(_ context.Context, r slog.Record) error {
	msg := r.Message
	var attrs []string
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, formatAttr(a))
		return true
	})
	text := msg
	if len(attrs) > 0 {
		text = msg + "  " + strings.Join(attrs, " ")
	}

	b.mu.Lock()
	text = redact(text, b.secret)
	msg = redact(msg, b.secret)
	line := Line{
		Time:    r.Time,
		Level:   r.Level,
		Message: msg,
		Text:    fmt.Sprintf("%s  %-5s  %s", r.Time.Local().Format("2006-01-02 15:04:05"), levelName(r.Level), text),
	}
	b.lines = append(b.lines, line)
	if len(b.lines) > maxLines {
		b.lines = append([]Line(nil), b.lines[len(b.lines)-maxLines:]...)
	}
	applySession(&b.session, msg)
	onChange := b.onChange
	b.mu.Unlock()
	if onChange != nil {
		onChange()
	}
	return nil
}

func (b *Buffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	return b
}

func (b *Buffer) WithGroup(name string) slog.Handler {
	return b
}

func (b *Buffer) Append(level slog.Level, msg string) {
	_ = b.Handle(context.Background(), slog.NewRecord(time.Now(), level, msg, 0))
}

func formatAttr(a slog.Attr) string {
	key := strings.ToLower(a.Key)
	if key == "password" || key == "passwd" || key == "secret" || key == "authorization" {
		return a.Key + "=***"
	}
	return a.Key + "=" + a.Value.String()
}

func redact(s, secret string) string {
	if secret == "" || len(secret) < 1 {
		return s
	}
	if !strings.Contains(s, secret) {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// ApplySession 根据一条库日志更新会话状态（导出给测试）。
func ApplySession(s *Session, msg string) {
	applySession(s, msg)
}

func applySession(s *Session, msg string) {
	switch {
	case strings.Contains(msg, "sending initial REGISTER"):
		s.Register = "正在注册"
	case strings.Contains(msg, "received 401, authenticating"):
		s.Register = "收到 401，正在认证"
	case strings.Contains(msg, "REGISTER successful"):
		s.Register = "成功"
		if s.Heartbeat == "—" || s.Heartbeat == "" {
			s.Heartbeat = "等待心跳"
		}
	case strings.Contains(msg, "initial REGISTER failed"):
		s.Register = "失败（仅监听，将重试）"
	case strings.Contains(msg, "re-register failed"):
		s.Register = "刷新失败"
	case strings.Contains(msg, "re-registration recovered"):
		s.Register = "已恢复"
	case strings.Contains(msg, "re-registration triggered"):
		s.Register = "正在重新注册"
	case strings.Contains(msg, "de-registered"):
		s.Register = "已注销"
	case strings.Contains(msg, "sending de-registration"):
		s.Register = "正在注销"
	}

	switch {
	case strings.Contains(msg, "keepalive send failed"):
		s.Heartbeat = "发送失败"
	case strings.Contains(msg, "too many keepalive failures"):
		s.Heartbeat = "连续失败，正在重注册"
	case strings.Contains(msg, "platform silent"):
		s.Heartbeat = "平台静默，正在重注册"
	}

	switch {
	case strings.Contains(msg, "received INVITE"):
		s.Invite = "已收到"
	case strings.Contains(msg, "retransmitted INVITE"):
		s.Invite = "重传（回放 200 OK）"
	case strings.Contains(msg, "received BYE"):
		s.Invite = "已结束（BYE）"
	case strings.Contains(msg, "failed to parse INVITE") || strings.Contains(msg, "rejecting INVITE") || strings.Contains(msg, "INVITE requested"):
		s.Invite = "失败"
	}

	switch {
	case strings.Contains(msg, "media goroutine started"):
		s.RTP = "推流中（合成画面）"
	case strings.Contains(msg, "media goroutine stopped"):
		s.RTP = "已结束"
	case strings.Contains(msg, "failed to send RTP"):
		s.RTP = "发送失败"
	case strings.Contains(msg, "bound media UDP"):
		s.RTP = "媒体端口已绑定"
	case strings.Contains(msg, "failed to bind media"):
		s.RTP = "媒体端口绑定失败"
	}
}
