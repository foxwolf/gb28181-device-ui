package ui

import (
	"log/slog"
	"strings"
	"testing"

	"gb28181-device-ui/internal/logview"
)

func TestClearLogsEmptiesViewAndKeepsFollow(t *testing.T) {
	u := &UI{
		logs:        logview.New(nil),
		stickBottom: false,
		logStart:    logPos{line: 2, col: 1},
		logEnd:      logPos{line: 3, col: 4},
		logHits:     []lineHit{{idx: 2, y: 10, h: 16, w: 100}},
		logDragging: true,
	}
	u.logs.Append(slog.LevelInfo, "old-a")
	u.logs.Append(slog.LevelInfo, "old-b")
	u.clearLogs()
	if lines := u.logs.Lines(); len(lines) != 0 {
		t.Fatalf("expected empty log view, got %d", len(lines))
	}
	if u.logStart != (logPos{}) || u.logEnd != (logPos{}) {
		t.Fatalf("selection should reset, start=%v end=%v", u.logStart, u.logEnd)
	}
	if len(u.logHits) != 0 {
		t.Fatalf("hits should reset, got %d", len(u.logHits))
	}
	if u.logDragging {
		t.Fatal("dragging should stop after clear")
	}
	if !u.stickBottom {
		t.Fatal("after clear, follow-new-logs should be re-enabled")
	}
	u.logs.Append(slog.LevelInfo, "fresh")
	lines := u.logs.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0].Text, "fresh") {
		t.Fatalf("new logs should appear after clear: %+v", lines)
	}
	if strings.Contains(u.logs.Dump(), "old-a") {
		t.Fatal("cleared cache was replayed")
	}
}
