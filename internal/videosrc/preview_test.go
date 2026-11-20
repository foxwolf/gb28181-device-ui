package videosrc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPreviewFrameBytes(t *testing.T) {
	if PreviewFrameBytes() != 640*360*4 {
		t.Fatalf("unexpected frame size %d", PreviewFrameBytes())
	}
}

func TestReadRGBAFrames(t *testing.T) {
	a := patternedFrame(1)
	b := patternedFrame(2)
	var src bytes.Buffer
	src.Write(a.Pix)
	src.Write(b.Pix)
	src.Write([]byte{9, 8, 7}) // 不完整的尾帧，应被忽略而不崩溃

	var got []*image.RGBA
	if err := ReadRGBAFrames(context.Background(), &src, func(img *image.RGBA) {
		got = append(got, img)
	}); err != nil {
		t.Fatalf("ReadRGBAFrames: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d frames, want 2", len(got))
	}
	if !bytes.Equal(got[0].Pix, a.Pix) || !bytes.Equal(got[1].Pix, b.Pix) {
		t.Fatal("frame pixels mismatch")
	}
	if got[0].Rect != image.Rect(0, 0, PreviewWidth, PreviewHeight) {
		t.Fatalf("unexpected rect %v", got[0].Rect)
	}
	// 回调拿到的是拷贝，修改源缓冲不应影响已发出的帧
	src.Reset()
	if !bytes.Equal(got[0].Pix, a.Pix) {
		t.Fatal("emitted frame was not an independent copy")
	}
}

func TestReadRGBAFramesCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
		_ = pw.Close()
	}()
	if err := ReadRGBAFrames(ctx, pr, func(*image.RGBA) {}); err != nil {
		t.Fatalf("canceled read should be clean, got %v", err)
	}
}

func TestServePreviewTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	want := patternedFrame(7)
	var mu sync.Mutex
	var got *image.RGBA
	done := make(chan struct{})
	sink := &testPreviewSink{onFrame: func(img *image.RGBA) {
		mu.Lock()
		got = img
		mu.Unlock()
		close(done)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go servePreview(ctx, ln, sink, nil)

	conn, err := net.DialTimeout("tcp", previewURLHost(port), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write(want.Pix); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for preview frame")
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || !bytes.Equal(got.Pix, want.Pix) {
		t.Fatal("tcp preview frame mismatch")
	}
}

func previewURLHost(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func patternedFrame(seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, PreviewWidth, PreviewHeight))
	for y := 0; y < PreviewHeight; y++ {
		for x := 0; x < PreviewWidth; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x + seed),
				G: uint8(y + seed*3),
				B: uint8(x*y + seed),
				A: 255,
			})
		}
	}
	return img
}

type testPreviewSink struct {
	onFrame func(*image.RGBA)
	onErr   func(error)
}

func (s *testPreviewSink) WritePreview(img *image.RGBA) {
	if s.onFrame != nil {
		s.onFrame(img)
	}
}

func (s *testPreviewSink) PreviewError(err error) {
	if s.onErr != nil {
		s.onErr(err)
	}
}
