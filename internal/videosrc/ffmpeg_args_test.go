package videosrc

import (
	"strings"
	"testing"
)

func TestCaptureArgsPreviewOnly(t *testing.T) {
	args := captureArgs(`HD Camera`, Options{}, nil, 54321)
	joined := strings.Join(args, " ")
	if !containsSeq(args, "-vf", PreviewFilter) {
		t.Fatalf("preview-only should use -vf %s, got %v", PreviewFilter, args)
	}
	if !containsSeq(args, "-f", "rawvideo", "tcp://127.0.0.1:54321") {
		t.Fatalf("preview-only should write rawvideo to local tcp, got %v", args)
	}
	if contains(args, "pipe:1") || contains(args, "libx264") || contains(args, "h264_mf") || contains(args, "filter_complex") {
		t.Fatalf("preview-only must not encode H.264, got %v", args)
	}
	if !strings.Contains(joined, "video=") || !strings.Contains(joined, "HD Camera") {
		t.Fatalf("missing dshow camera input: %v", args)
	}
	if !contains(args, "dshow") {
		t.Fatalf("missing dshow: %v", args)
	}
}

func TestCaptureArgsDualOutput(t *testing.T) {
	enc := []string{"-c:v", "libx264", "-preset", "veryfast"}
	args := captureArgs("cam1", Options{Width: 1280, Height: 720, FPS: 25}, enc, 19001)
	if !containsSeq(args, "-filter_complex", "[0:v]split=2[enc][prev];[prev]"+PreviewFilter+"[rgba]") {
		t.Fatalf("dual output should split + pad to 640x360, got %v", args)
	}
	if !containsSeq(args, "-f", "h264", "pipe:1") {
		t.Fatalf("dual output should write Annex-B to stdout, got %v", args)
	}
	if !containsSeq(args, "-f", "rawvideo", "tcp://127.0.0.1:19001") {
		t.Fatalf("dual output should write preview to tcp, got %v", args)
	}
	if !containsSeq(args, "-c:v", "libx264") {
		t.Fatalf("dual output should keep encoder args, got %v", args)
	}
	if !containsSeq(args, "-s", "1280x720") || !containsSeq(args, "-r", "25") {
		t.Fatalf("encoder size/rate should apply to H.264 map, got %v", args)
	}
	// 预览路不应再被 -vf 单独缩放，尺寸由 filter_complex 固定。
	if containsSeq(args, "-vf", PreviewFilter) {
		t.Fatalf("dual output should not also set -vf, got %v", args)
	}
}

func TestCaptureArgsEncodeOnly(t *testing.T) {
	enc := []string{"-c:v", "h264_mf"}
	args := captureArgs("cam1", Options{FPS: 25}, enc, 0)
	if !containsSeq(args, "-f", "h264", "pipe:1") {
		t.Fatalf("encode-only should write stdout, got %v", args)
	}
	if contains(args, "rawvideo") || contains(args, "tcp://") || contains(args, "filter_complex") {
		t.Fatalf("encode-only should not open preview tcp, got %v", args)
	}
	if !containsSeq(args, "-c:v", "h264_mf") {
		t.Fatalf("missing encoder, got %v", args)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.Contains(a, want) {
			return true
		}
	}
	return false
}

func containsSeq(args []string, seq ...string) bool {
	if len(seq) == 0 || len(seq) > len(args) {
		return false
	}
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j := range seq {
			if args[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
