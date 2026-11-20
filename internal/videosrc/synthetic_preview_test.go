package videosrc

import (
	"bytes"
	"context"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"
)

func TestRenderSyntheticPreviewAnimates(t *testing.T) {
	a := RenderSyntheticPreview(0)
	b := RenderSyntheticPreview(20)
	if a.Bounds() != image.Rect(0, 0, PreviewWidth, PreviewHeight) {
		t.Fatalf("unexpected size %v", a.Bounds())
	}
	if bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("synthetic preview frames should change over time")
	}
	// 像素是独立缓冲，再次渲染不应改动已返回的帧。
	_ = RenderSyntheticPreview(0)
	if !bytes.Equal(a.Pix, RenderSyntheticPreview(0).Pix) {
		t.Fatal("same frame index should be deterministic")
	}
}

func TestSyntheticPreviewSession(t *testing.T) {
	var mu sync.Mutex
	var frames []*image.RGBA
	sink := &testPreviewSink{onFrame: func(img *image.RGBA) {
		mu.Lock()
		frames = append(frames, img)
		mu.Unlock()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sess, err := (Synthetic{}).Preview(ctx, Device{}, sink)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(frames)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	StopWait(sess, time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(frames) < 2 {
		t.Fatalf("want at least 2 preview frames, got %d", len(frames))
	}
	if bytes.Equal(frames[0].Pix, frames[1].Pix) {
		t.Fatal("preview frames should not be identical")
	}
}

func TestSyntheticOpenEmitsPreview(t *testing.T) {
	var mu sync.Mutex
	var n int
	sink := &testPreviewSink{onFrame: func(*image.RGBA) {
		mu.Lock()
		n++
		mu.Unlock()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sess, err := (Synthetic{}).Open(ctx, Device{}, Options{Preview: sink}, discardAU{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := n
		mu.Unlock()
		if got >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	StopWait(sess, time.Second)
	mu.Lock()
	defer mu.Unlock()
	if n < 2 {
		t.Fatalf("Open with Preview should emit preview frames, got %d", n)
	}
}

func TestStopWaitNil(t *testing.T) {
	StopWait(nil, 10*time.Millisecond)
}

type discardAU struct{}

func (discardAU) Write(device.AccessUnit) {}
