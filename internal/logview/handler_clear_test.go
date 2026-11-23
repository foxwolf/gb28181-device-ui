package logview

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClearEmptiesLinesAndDoesNotReplay(t *testing.T) {
	b := New(nil)
	b.Append(slog.LevelInfo, "first line")
	b.Append(slog.LevelWarn, "second line")
	if got := b.Dump(); !strings.Contains(got, "first line") || !strings.Contains(got, "second line") {
		t.Fatalf("pre-clear dump missing lines: %q", got)
	}
	b.Clear()
	if lines := b.Lines(); len(lines) != 0 {
		t.Fatalf("clear should empty buffer, got %d lines", len(lines))
	}
	if dump := b.Dump(); dump != "" {
		t.Fatalf("clear should empty dump, got %q", dump)
	}
	b.Append(slog.LevelInfo, "after-clear")
	lines := b.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 new line after clear, got %d", len(lines))
	}
	if strings.Contains(lines[0].Text, "first line") || strings.Contains(lines[0].Text, "second line") {
		t.Fatalf("old cache replayed after clear: %q", lines[0].Text)
	}
	if !strings.Contains(lines[0].Text, "after-clear") {
		t.Fatalf("new log missing after clear: %q", lines[0].Text)
	}
}

func TestClearDoesNotResetSession(t *testing.T) {
	b := New(nil)
	var s Session
	ApplySession(&s, "gb28181: sending initial REGISTER")
	ApplySession(&s, "gb28181: REGISTER successful")
	b.mu.Lock()
	b.session = s
	b.mu.Unlock()
	b.Append(slog.LevelInfo, "noise")
	b.Clear()
	got := b.Session()
	if got.Register != "成功" || got.Heartbeat != "等待心跳" {
		t.Fatalf("clear must not reset session, got %+v", got)
	}
}

func TestClearInvokesOnChange(t *testing.T) {
	var n atomic.Int32
	b := New(func() { n.Add(1) })
	b.Append(slog.LevelInfo, "x")
	before := n.Load()
	b.Clear()
	if n.Load() <= before {
		t.Fatal("Clear should notify onChange so the UI can refresh")
	}
}

func TestBufferDoesNotGrowPastMaxLines(t *testing.T) {
	b := New(nil)
	for i := 0; i < maxLines+25; i++ {
		b.Append(slog.LevelInfo, "line")
	}
	if got := len(b.Lines()); got != maxLines {
		t.Fatalf("buffer grew to %d, want cap %d", got, maxLines)
	}
}
