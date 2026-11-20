package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gb28181-device-ui/internal/videosrc"
)

// previewHub 保存最新一帧预览，供 Gio 在窗体线程用 paint.ImageOp 绘制。
type previewHub struct {
	invalidate func()

	mu     sync.Mutex
	img    *image.RGBA
	gen    int
	err    string
	status string
}

func (h *previewHub) WritePreview(img *image.RGBA) {
	if h == nil || img == nil {
		return
	}
	h.mu.Lock()
	h.img = img
	h.gen++
	h.err = ""
	h.status = ""
	h.mu.Unlock()
	h.ping()
}

func (h *previewHub) PreviewError(err error) {
	if h == nil || err == nil {
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return
	}
	if !strings.HasPrefix(msg, "预览失败") {
		msg = "预览失败：" + msg
	}
	h.mu.Lock()
	h.err = msg
	h.mu.Unlock()
	h.ping()
}

func (h *previewHub) SetStatus(s string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.status = s
	h.err = ""
	h.mu.Unlock()
	h.ping()
}

func (h *previewHub) Clear(status string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.img = nil
	h.gen++
	h.err = ""
	h.status = status
	h.mu.Unlock()
	h.ping()
}

func (h *previewHub) snapshot() (img *image.RGBA, gen int, err, status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.img, h.gen, h.err, h.status
}

func (h *previewHub) ping() {
	if h != nil && h.invalidate != nil {
		h.invalidate()
	}
}

func (u *UI) initPreview() {
	u.preview = &previewHub{invalidate: func() {
		if u.win != nil {
			u.win.Invalidate()
		}
	}}
	u.preview.status = "暂无画面"
}

// desiredPreviewKey 返回当前应跑的独立预览标识。启动 SIP 期间由采集进程兼顾预览，这里返回空。
func (u *UI) desiredPreviewKey(running bool) string {
	if running {
		return ""
	}
	switch u.backend.Value {
	case videosrc.BackendSynthetic:
		return videosrc.BackendSynthetic
	case videosrc.BackendFFmpeg:
		id := strings.TrimSpace(u.selectedCam.ID)
		if id == "" {
			id = strings.TrimSpace(u.selectedCam.Name)
		}
		if id == "" {
			return ""
		}
		return videosrc.BackendFFmpeg + "\x00" + id
	default:
		return ""
	}
}

func (u *UI) syncPreview(running bool) {
	want := u.desiredPreviewKey(running)
	u.previewMu.Lock()
	cur, starting := u.previewKey, u.previewStarting
	u.previewMu.Unlock()
	if running {
		if cur != "" || starting {
			u.stopStandalonePreview(false)
		}
		return
	}
	if want == cur && (cur != "" || starting) {
		return
	}
	if want == cur && cur == "" && !starting {
		// 没有预览任务时仍更新无信号提示。
		if u.backend.Value == videosrc.BackendFFmpeg && strings.TrimSpace(u.selectedCam.ID) == "" && strings.TrimSpace(u.selectedCam.Name) == "" {
			if img, _, err, status := u.preview.snapshot(); img == nil && err == "" && status != "请先选择摄像头" {
				u.preview.Clear("请先选择摄像头")
			}
		}
		return
	}
	changedSource := want != cur && cur != ""
	u.stopStandalonePreview(changedSource)
	if want == "" {
		status := "暂无画面"
		if u.backend.Value == videosrc.BackendFFmpeg {
			status = "请先选择摄像头"
		}
		u.preview.Clear(status)
		return
	}
	u.startStandalonePreview(want, changedSource)
}

func (u *UI) startStandalonePreview(key string, clearFrame bool) {
	backend, dev, err := u.previewTarget(key)
	if err != nil {
		u.preview.Clear("")
		u.preview.PreviewError(err)
		return
	}
	p, ok := backend.(videosrc.Previewable)
	if !ok {
		u.preview.Clear("该视频源不支持本地预览")
		return
	}
	msg := "正在连接摄像头…"
	if key == videosrc.BackendSynthetic {
		msg = "正在生成预览…"
	}
	if clearFrame {
		u.preview.Clear(msg)
	} else {
		u.preview.SetStatus(msg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.previewMu.Lock()
	u.previewKey = key
	u.previewStarting = true
	u.previewCancel = cancel
	u.previewMu.Unlock()

	go func() {
		sess, openErr := p.Preview(ctx, dev, u.preview)
		u.previewMu.Lock()
		still := u.previewKey == key && u.previewCancel != nil
		u.previewStarting = false
		if !still {
			u.previewMu.Unlock()
			if sess != nil {
				videosrc.StopWait(sess, 4*time.Second)
			}
			return
		}
		if openErr != nil {
			// 保留 previewKey，避免每帧重试；用户刷新或改选后再启动。
			u.previewCancel = nil
			u.previewSess = nil
			u.previewMu.Unlock()
			u.preview.PreviewError(openErr)
			return
		}
		u.previewSess = sess
		u.previewMu.Unlock()
		if err := sess.Wait(); err != nil {
			u.previewMu.Lock()
			current := u.previewKey == key
			u.previewMu.Unlock()
			if current {
				u.preview.PreviewError(err)
			}
		}
	}()
}

func (u *UI) stopStandalonePreview(clearFrame bool) {
	u.previewMu.Lock()
	sess := u.previewSess
	cancel := u.previewCancel
	u.previewSess = nil
	u.previewCancel = nil
	u.previewKey = ""
	u.previewStarting = false
	u.previewMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sess != nil {
		videosrc.StopWait(sess, 4*time.Second)
	}
	if clearFrame {
		u.preview.Clear("暂无画面")
	}
}

// restartPreview 强制结束当前独立预览，使下一帧的 syncPreview 按新选择重新启动。
func (u *UI) restartPreview() {
	u.stopStandalonePreview(true)
}

func (u *UI) previewTarget(key string) (videosrc.Backend, videosrc.Device, error) {
	if key == videosrc.BackendSynthetic {
		b, ok := videosrc.Lookup(videosrc.BackendSynthetic)
		if !ok {
			return nil, videosrc.Device{}, fmt.Errorf("未登记合成视频源")
		}
		return b, videosrc.Device{ID: "placeholder", Name: "合成占位画面"}, nil
	}
	if strings.HasPrefix(key, videosrc.BackendFFmpeg+"\x00") {
		b, ok := videosrc.Lookup(videosrc.BackendFFmpeg)
		if !ok {
			return nil, videosrc.Device{}, fmt.Errorf("未登记 FFmpeg 视频源")
		}
		if err := b.Available(); err != nil {
			return nil, videosrc.Device{}, err
		}
		return b, u.selectedCam, nil
	}
	return nil, videosrc.Device{}, fmt.Errorf("未知的视频源")
}

func (u *UI) previewPane(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(15), "本地画面")
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(8)),
		layout.Rigid(u.previewBox),
	)
}

func (u *UI) previewBox(gtx C) D {
	w := gtx.Constraints.Max.X
	if w < 1 {
		w = 1
	}
	h := w * videosrc.PreviewHeight / videosrc.PreviewWidth
	maxH := gtx.Dp(unit.Dp(180))
	if h > maxH {
		h = maxH
		w = h * videosrc.PreviewWidth / videosrc.PreviewHeight
	}
	minH := gtx.Dp(unit.Dp(120))
	if h < minH && gtx.Constraints.Max.Y >= minH {
		// 保持 16:9，不强制撑到 minH，以免裁切比例。
	}
	size := image.Pt(gtx.Constraints.Max.X, h)
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size

	defer clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(4)).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 0xFF})

	img, gen, errText, status := u.preview.snapshot()
	if img != nil {
		if gen != u.previewDrawn {
			u.previewOp = paint.NewImageOp(img)
			u.previewOp.Filter = paint.FilterLinear
			u.previewDrawn = gen
		}
		drawPreviewFitted(gtx, u.previewOp, size)
	}

	msg := status
	if errText != "" {
		msg = errText
	} else if img == nil && msg == "" {
		msg = "暂无画面"
	} else if img != nil {
		msg = ""
	}
	if msg != "" {
		layout.Center.Layout(gtx, func(gtx C) D {
			l := material.Label(u.th, unit.Sp(13), msg)
			l.Color = color.NRGBA{R: 0xDD, G: 0xE2, B: 0xE8, A: 0xFF}
			l.Alignment = text.Middle
			l.MaxLines = 4
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, l.Layout)
		})
	}
	return D{Size: size}
}

func drawPreviewFitted(gtx C, imgOp paint.ImageOp, box image.Point) {
	isz := imgOp.Size()
	if isz.X < 1 || isz.Y < 1 || box.X < 1 || box.Y < 1 {
		return
	}
	sx := float32(box.X) / float32(isz.X)
	sy := float32(box.Y) / float32(isz.Y)
	scale := sx
	if sy < sx {
		scale = sy
	}
	dw := float32(isz.X) * scale
	dh := float32(isz.Y) * scale
	ox := (float32(box.X) - dw) / 2
	oy := (float32(box.Y) - dh) / 2
	defer op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale)).Offset(f32.Pt(ox, oy))).Push(gtx.Ops).Pop()
	imgOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}
