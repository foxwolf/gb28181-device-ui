// Package videosrc 提供可插拔的视频采集后端。
//
// 当前实现：
//   - synthetic：库内合成的占位 H.264（默认，无需外部程序）
//   - ffmpeg：以外部进程调用 ffmpeg，在 Windows 上经 DirectShow 采集本机摄像头
//
// 未来若以进程内方式接入（例如 pion/mediadevices 或 Media Foundation），
// 应实现 Backend 接口并以 "native" 为 ID 调用 Register，无需改动界面或 SIP 启动逻辑。
package videosrc

import (
	"context"
	"sync"

	"github.com/mickeyzzc/gb28181-go/device"
)

// Backend 是可插拔的采集实现。"ffmpeg" 是第一个真实后端；
// "synthetic" 保留当前占位画面；未来的 "native"（Media Foundation / pion）
// 应实现同一接口，从而不必改 UI 或 runner。
type Backend interface {
	ID() string    // 稳定标识： "ffmpeg"、"synthetic"，以及日后的 "native"
	Label() string // 界面用的中文名称
	// Available 在本后端可用时返回 nil（例如已找到 ffmpeg）。
	Available() error
	// Devices 列出摄像头。synthetic 可返回单个占位设备或空列表。
	Devices(ctx context.Context) ([]Device, error)
	// Open 开始采集，向 dest 写入完整的 H.264 AccessUnit，直到 ctx 取消。
	// 返回的 Session 必须 Close()；Wait() 在停止后返回运行错误。
	Open(ctx context.Context, dev Device, opts Options, dest Sink) (Session, error)
}

// Device 表示一路可选择的采集设备。
type Device struct {
	ID   string // 稳定标识：有 DirectShow Alternative name 时优先用它
	Name string // 下拉框里的友好名称
}

// Options 是采集/编码参数。0 表示使用编码器或设备默认值。
// 优先 25fps、yuv420p、baseline、无 B 帧、较密的 IDR。
// Preview 非空时，在编码的同时输出固定尺寸的 RGBA 预览帧（FFmpeg 走第二路，不在本进程解码）。
type Options struct {
	Width, Height, FPS int
	Preview            PreviewSink
}

// Sink 接收编码后的访问单元。语义与 FrameHub.Write 相同：
// 不得阻塞生产者（hub 在队列满时会丢帧）。
type Sink interface {
	Write(au device.AccessUnit)
}

// Session 表示一次正在进行的采集。
type Session interface {
	Wait() error
	Close() error
}

var (
	regMu    sync.RWMutex
	byID     = make(map[string]Backend)
	regOrder []string
)

// Register 登记一个后端。同一 ID 再次登记会覆盖实现，但保留原顺序。
func Register(b Backend) {
	if b == nil {
		return
	}
	id := b.ID()
	regMu.Lock()
	defer regMu.Unlock()
	if _, ok := byID[id]; !ok {
		regOrder = append(regOrder, id)
	}
	byID[id] = b
}

// Lookup 按 ID 查找已登记的后端。
func Lookup(id string) (Backend, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	b, ok := byID[id]
	return b, ok
}

// ListBackends 按登记顺序返回后端。
func ListBackends() []Backend {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Backend, 0, len(regOrder))
	for _, id := range regOrder {
		if b, ok := byID[id]; ok {
			out = append(out, b)
		}
	}
	return out
}
