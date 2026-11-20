package videosrc

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"sync/atomic"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"
)

const (
	BackendSynthetic  = "synthetic"
	syntheticDeviceID = "placeholder"
)

func init() {
	Register(Synthetic{})
}

// Synthetic 写入库内合成的占位 H.264（约 1 fps，每 5 帧一个 IDR），
// 同时可生成简单的动画 RGBA 预览（不必经过 FFmpeg）。
type Synthetic struct{}

func (Synthetic) ID() string       { return BackendSynthetic }
func (Synthetic) Label() string    { return "合成占位画面" }
func (Synthetic) Available() error { return nil }

func (Synthetic) Devices(context.Context) ([]Device, error) {
	return []Device{{ID: syntheticDeviceID, Name: "合成占位 H.264（每秒一帧）"}}, nil
}

func (Synthetic) Open(ctx context.Context, _ Device, opts Options, dest Sink) (Session, error) {
	if dest == nil {
		return nil, errNilSink
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &basicSession{cancel: cancel, done: make(chan error, 1)}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var err error
		defer func() { s.finish(err) }()
		stopPreview := startSyntheticPreview(ctx, opts.Preview)
		defer stopPreview()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				err = nil
				return
			case <-ticker.C:
				dest.Write(syntheticAU(i))
			}
		}
	}()
	return s, nil
}

// Preview 仅输出动画预览，不产生 H.264，也不启动 SIP。
func (Synthetic) Preview(ctx context.Context, _ Device, dest PreviewSink) (Session, error) {
	if dest == nil {
		return nil, errNilPreview
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &basicSession{cancel: cancel, done: make(chan error, 1)}
	go func() {
		defer s.finish(nil)
		stopPreview := startSyntheticPreview(ctx, dest)
		defer stopPreview()
		<-ctx.Done()
	}()
	return s, nil
}

func startSyntheticPreview(ctx context.Context, dest PreviewSink) (stop func()) {
	if dest == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second / PreviewFPS)
		defer tick.Stop()
		for i := 0; ; i++ {
			dest.WritePreview(RenderSyntheticPreview(i))
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { <-done }
}

func syntheticAU(i int) device.AccessUnit {
	au := device.AccessUnit{
		Timestamp: time.Now(),
		KeyFrame:  i%5 == 0,
	}
	if au.KeyFrame {
		au.NALUs = []device.NALU{
			{Type: 7, IsSPS: true, Data: []byte{0x67, 0x42}},
			{Type: 8, IsPPS: true, Data: []byte{0x68, 0xEB}},
			{Type: 5, IsIDR: true, Data: []byte{0x65, 0x01}},
		}
	} else {
		au.NALUs = []device.NALU{{Type: 1, Data: []byte{0x41, 0x02}}}
	}
	return au
}

// RenderSyntheticPreview 画一帧固定尺寸的占位动画（移动色条 + 弹跳方块）。
func RenderSyntheticPreview(frame int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, PreviewWidth, PreviewHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 16, G: 22, B: 32, A: 255}}, image.Point{}, draw.Src)

	// 顶部与底部细边线，标明这是占位画面而不是黑屏无信号。
	bar := color.RGBA{R: 40, G: 80, B: 110, A: 255}
	draw.Draw(img, image.Rect(0, 0, PreviewWidth, 6), &image.Uniform{C: bar}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, PreviewHeight-6, PreviewWidth, PreviewHeight), &image.Uniform{C: bar}, image.Point{}, draw.Src)

	// 水平移动的色带。
	bandW := 80
	x := (frame * 8) % (PreviewWidth + bandW)
	x0 := x - bandW
	if x0 < 0 {
		x0 = 0
	}
	x1 := x
	if x1 > PreviewWidth {
		x1 = PreviewWidth
	}
	if x1 > x0 {
		hue := uint8((frame * 12) % 180)
		col := color.RGBA{R: 40 + hue/2, G: 160, B: 220 - hue/3, A: 255}
		draw.Draw(img, image.Rect(x0, 80, x1, 160), &image.Uniform{C: col}, image.Point{}, draw.Src)
	}

	// 左右来回弹跳的方块。
	box := 48
	span := PreviewWidth - box
	if span < 1 {
		span = 1
	}
	period := span * 2
	pos := frame * 6 % period
	if pos > span {
		pos = period - pos
	}
	by := 220
	draw.Draw(img, image.Rect(pos, by, pos+box, by+box), &image.Uniform{C: color.RGBA{R: 230, G: 180, B: 50, A: 255}}, image.Point{}, draw.Src)
	return img
}

// basicSession 用于不需要杀子进程的后端（如 synthetic）。
type basicSession struct {
	cancel context.CancelFunc
	done   chan error
	closed atomic.Bool
	once   atomic.Bool
}

func (s *basicSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *basicSession) Wait() error {
	err, ok := <-s.done
	if !ok {
		return nil
	}
	return err
}

func (s *basicSession) finish(err error) {
	if s.once.Swap(true) {
		return
	}
	s.done <- err
	close(s.done)
}
