package logview

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRedactsPassword(t *testing.T) {
	b := New(nil)
	b.SetSecret("super-secret")
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "auth with super-secret failed", 0)
	rec.AddAttrs(slog.String("password", "super-secret"))
	if err := b.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	lines := b.Lines()
	if len(lines) != 1 {
		t.Fatalf("got %d lines", len(lines))
	}
	if strings.Contains(lines[0].Text, "super-secret") {
		t.Fatalf("password leaked in log: %s", lines[0].Text)
	}
	if !strings.Contains(lines[0].Text, "***") {
		t.Fatalf("expected redaction, got %s", lines[0].Text)
	}
}

func TestSessionFromLogs(t *testing.T) {
	var s Session
	ApplySession(&s, "gb28181: sending initial REGISTER")
	if s.Register != "正在注册" {
		t.Fatalf("register = %q", s.Register)
	}
	ApplySession(&s, "gb28181: REGISTER successful")
	if s.Register != "成功" || s.Heartbeat != "等待心跳" {
		t.Fatalf("after success: %+v", s)
	}
	ApplySession(&s, "gb28181: received INVITE")
	ApplySession(&s, "gb28181: media goroutine started")
	if s.Invite != "已收到" || s.RTP != "推流中（合成画面）" {
		t.Fatalf("media: %+v", s)
	}
	ApplySession(&s, "gb28181: received BYE")
	ApplySession(&s, "gb28181: media goroutine stopped")
	if s.Invite != "已结束（BYE）" || s.RTP != "已结束" {
		t.Fatalf("bye: %+v", s)
	}
}
