package videosrc

import (
	"context"
	"fmt"
	"image"
	"io"
	"net"
	"time"
)

// 预览画面为固定尺寸的原始 RGBA，便于按帧切分，也避免在进程内解码 H.264。
const (
	PreviewWidth  = 640
	PreviewHeight = 360
	PreviewFPS    = 10
)

// PreviewFilter 是 ffmpeg 预览路上的滤镜：约 10 fps、等比缩小后居中垫到 640x360、输出 rgba。
const PreviewFilter = "fps=10,scale=640:360:force_original_aspect_ratio=decrease,pad=640:360:(ow-iw)/2:(oh-ih)/2,format=rgba"

// PreviewFrameBytes 是一帧 rawvideo rgba 的字节数。
func PreviewFrameBytes() int { return PreviewWidth * PreviewHeight * 4 }

// PreviewSink 接收预览帧。实现方不得长时间阻塞；UI 侧应尽快拷贝或替换最新帧。
type PreviewSink interface {
	WritePreview(img *image.RGBA)
}

// PreviewErrorSink 是可选接口，用于把预览失败以中文错误交给界面，而不中断推流。
type PreviewErrorSink interface {
	PreviewError(err error)
}

// Previewable 是可在未启动 SIP 时单独开启预览的后端。
type Previewable interface {
	Preview(ctx context.Context, dev Device, dest PreviewSink) (Session, error)
}

func reportPreviewError(dest PreviewSink, err error) {
	if dest == nil || err == nil {
		return
	}
	if s, ok := dest.(PreviewErrorSink); ok {
		s.PreviewError(err)
	}
}

// ReadRGBAFrame 从 r 读取恰好一帧 640x360 RGBA。buf 可复用；返回的图像像素是独立拷贝，可交给界面绘制。
func ReadRGBAFrame(r io.Reader, buf []byte) (*image.RGBA, error) {
	need := PreviewFrameBytes()
	if len(buf) < need {
		buf = make([]byte, need)
	} else {
		buf = buf[:need]
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	pix := make([]byte, need)
	copy(pix, buf)
	return &image.RGBA{
		Pix:    pix,
		Stride: PreviewWidth * 4,
		Rect:   image.Rect(0, 0, PreviewWidth, PreviewHeight),
	}, nil
}

// ReadRGBAFrames 持续读取完整预览帧并回调。流正常结束（EOF 或最后一帧不完整）时返回 nil。
func ReadRGBAFrames(ctx context.Context, r io.Reader, emit func(*image.RGBA)) error {
	if emit == nil {
		return fmt.Errorf("预览回调不能为空")
	}
	buf := make([]byte, PreviewFrameBytes())
	for {
		if ctx != nil && ctx.Err() != nil {
			return nil
		}
		img, err := ReadRGBAFrame(r, buf)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			if ctx != nil && ctx.Err() != nil {
				return nil
			}
			return err
		}
		emit(img)
	}
}

func listenPreview() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("无法在本机监听预览端口：%w", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		_ = ln.Close()
		return nil, 0, fmt.Errorf("无法在本机监听预览端口")
	}
	return ln, addr.Port, nil
}

func servePreview(ctx context.Context, ln net.Listener, dest PreviewSink, onFrame func()) {
	if ln == nil || dest == nil {
		return
	}
	defer func() { _ = ln.Close() }()

	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(8 * time.Second))
	}
	conn, err := ln.Accept()
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		reportPreviewError(dest, fmt.Errorf("预览失败：未能连接到本机预览端口：%v", err))
		return
	}
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Time{})
	}
	_ = ln.Close()
	defer func() { _ = conn.Close() }()

	err = ReadRGBAFrames(ctx, deadlineReader{conn: conn, idle: 5 * time.Second}, func(img *image.RGBA) {
		if onFrame != nil {
			onFrame()
		}
		dest.WritePreview(img)
	})
	if err != nil && (ctx == nil || ctx.Err() == nil) {
		reportPreviewError(dest, fmt.Errorf("预览失败：读取画面时中断：%v", err))
	}
}

// deadlineReader 给预览连接加上读超时，避免 ffmpeg 挂起时永久阻塞。
type deadlineReader struct {
	conn net.Conn
	idle time.Duration
}

func (r deadlineReader) Read(p []byte) (int, error) {
	if r.idle > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.idle))
	}
	return r.conn.Read(p)
}

// StopWait 关闭采集会话并等待子进程退出，避免留下孤儿 ffmpeg。
func StopWait(s Session, d time.Duration) {
	if s == nil {
		return
	}
	_ = s.Close()
	done := make(chan struct{})
	go func() {
		_ = s.Wait()
		close(done)
	}()
	if d <= 0 {
		d = 4 * time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func previewURL(port int) string {
	return fmt.Sprintf("tcp://127.0.0.1:%d", port)
}
