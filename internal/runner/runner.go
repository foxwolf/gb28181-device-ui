// Package runner 封装 gb28181-go 设备侧（UAC）的启动、停止和合成视频循环。
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"

	"gb28181-device-ui/internal/appconfig"
	"gb28181-device-ui/internal/logview"
)

var (
	ErrAlreadyRunning = errors.New("设备已在运行")
	ErrNotRunning     = errors.New("设备未启动")
	ErrEmptyPassword  = errors.New("请填写密码（与平台约定的 SIP 认证密码）")
)

// State 是界面展示用的运行状态。
type State int

const (
	StateIdle State = iota
	StateRunning
	StateError
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "未启动"
	case StateRunning:
		return "运行中"
	case StateError:
		return "出错"
	default:
		return "未知"
	}
}

// Runner 管理单个 device.Server 实例，禁止并行启动。
type Runner struct {
	mu     sync.Mutex
	state  State
	err    string
	cancel context.CancelFunc
	srv    *device.Server
	done   chan struct{}
	logs   *logview.Buffer
}

func New(logs *logview.Buffer) *Runner {
	return &Runner{state: StateIdle, logs: logs}
}

func (r *Runner) State() (State, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.err
}

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == StateRunning
}

// Server 返回当前实例（可能为 nil）。调用方只应读取导出的状态方法。
func (r *Runner) Server() *device.Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.srv
}

// Start 根据界面配置启动设备。密码为空或已在运行时立即返回错误。
// 端口占用等绑定错误会在 Start 返回之后通过状态变为「出错」体现。
func (r *Runner) Start(cfg appconfig.Config) error {
	if strings.TrimSpace(cfg.Password) == "" {
		return ErrEmptyPassword
	}
	devCfg, info, err := toDevice(cfg)
	if err != nil {
		return err
	}
	if err := devCfg.Validate(); err != nil {
		return formatValidate(err)
	}

	r.mu.Lock()
	if r.state == StateRunning {
		r.mu.Unlock()
		return ErrAlreadyRunning
	}
	// 若上次的 goroutine 尚未退出，先等它结束。
	donePrev := r.done
	r.mu.Unlock()
	if donePrev != nil {
		select {
		case <-donePrev:
		case <-time.After(3 * time.Second):
			return fmt.Errorf("上一轮设备尚未完全停止，请稍后重试")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	hub := device.NewFrameHub()
	srv := device.New(devCfg, info, hub)
	done := make(chan struct{})

	r.mu.Lock()
	r.state = StateRunning
	r.err = ""
	r.cancel = cancel
	r.srv = srv
	r.done = done
	r.mu.Unlock()

	if r.logs != nil {
		r.logs.ResetSession()
		r.logs.SetSecret(cfg.Password)
	}
	slog.Info("界面：正在启动设备",
		"platform", fmt.Sprintf("%s:%d", devCfg.PlatformSIPAddress, devCfg.PlatformSIPPort),
		"device_id", devCfg.DeviceID,
		"channel_id", devCfg.ChannelID,
		"local_sip_port", devCfg.LocalSIPPort,
		"transport", devCfg.Transport,
	)
	slog.Info("界面：视频为库内合成的占位 H.264 帧（每秒一帧，每 5 帧一个 IDR），不是 RTSP 实况")

	go r.loop(ctx, cancel, srv, hub, done)
	return nil
}

func (r *Runner) loop(ctx context.Context, cancel context.CancelFunc, srv *device.Server, hub *device.FrameHub, done chan struct{}) {
	defer close(done)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// 绑定成功后尽快把实际端口和平台信息刷到状态栏。
	go r.pollServer(ctx, srv)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			srv.Stop()
			r.finish(nil)
			return
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				slog.Error("界面：设备服务退出", "error", err)
				cancel()
				srv.Stop()
				r.finish(err)
				return
			}
			srv.Stop()
			r.finish(nil)
			return
		case <-ticker.C:
			hub.Write(syntheticAU(i))
		}
	}
}

func (r *Runner) pollServer(ctx context.Context, srv *device.Server) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.logs == nil {
				continue
			}
			if p, err := srv.SIPPort(); err == nil && p > 0 {
				r.logs.SetBoundPort(p)
			}
			r.logs.SetPlatformInfo(srv.PlatformProtocolVersion(), srv.PlatformDateUnix())
		}
	}
}

func (r *Runner) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = nil
	r.srv = nil
	if err != nil {
		r.state = StateError
		r.err = formatStartErr(err)
	} else if r.state == StateRunning {
		r.state = StateIdle
		r.err = ""
	}
}

// Stop 取消上下文、调用 Server.Stop，并结束合成循环。
// 若曾经注册成功，会先尝试 Deregister（最多约 2.5 秒），失败不影响 Stop。
func (r *Runner) Stop() {
	r.mu.Lock()
	if r.state != StateRunning {
		r.mu.Unlock()
		return
	}
	srv := r.srv
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()

	slog.Info("界面：正在停止设备")
	if srv != nil {
		dctx, dcancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		if err := srv.Deregister(dctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("界面：注销（Deregister）未确认，将继续停止", "error", err)
		}
		dcancel()
	}
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		srv.Stop()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			slog.Warn("界面：等待设备退出超时")
		}
	}
	r.mu.Lock()
	if r.state == StateRunning {
		r.state = StateIdle
		r.err = ""
	}
	r.cancel = nil
	r.srv = nil
	r.mu.Unlock()
	slog.Info("界面：设备已停止")
}

func toDevice(cfg appconfig.Config) (device.Config, device.DeviceInfo, error) {
	manu := strings.TrimSpace(cfg.Manufacturer)
	if manu == "" {
		manu = "Unknown"
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "Unknown"
	}
	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if transport == "" {
		transport = "udp"
	}
	return device.Config{
		Enabled:               true,
		PlatformSIPAddress:    strings.TrimSpace(cfg.PlatformSIPAddress),
		PlatformSIPPort:       cfg.PlatformSIPPort,
		DeviceID:              strings.TrimSpace(cfg.DeviceID),
		ChannelID:             strings.TrimSpace(cfg.ChannelID),
		SIPDomain:             strings.TrimSpace(cfg.SIPDomain),
		Password:              cfg.Password,
		LocalSIPPort:          cfg.LocalSIPPort,
		RegisterIntervalSecs:  cfg.RegisterIntervalSecs,
		HeartbeatIntervalSecs: cfg.HeartbeatIntervalSecs,
		HeartbeatTimeoutCount: 3,
		Transport:             transport,
	}, device.DeviceInfo{
		Name:         strings.TrimSpace(cfg.Name),
		Manufacturer: manu,
		Model:        model,
	}, nil
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

func formatValidate(err error) error {
	s := err.Error()
	switch {
	case strings.Contains(s, "PlatformSIPAddress"):
		return fmt.Errorf("请填写平台地址")
	case strings.Contains(s, "PlatformSIPPort"):
		return fmt.Errorf("平台端口无效（1–65535）")
	case strings.Contains(s, "DeviceID"):
		return fmt.Errorf("设备编码必须是 20 位数字")
	case strings.Contains(s, "ChannelID"):
		return fmt.Errorf("通道编码必须是 20 位数字")
	case strings.Contains(s, "SIPDomain"):
		return fmt.Errorf("请填写 SIP 域")
	case strings.Contains(s, "RegisterIntervalSecs"):
		return fmt.Errorf("注册间隔必须大于 0 秒")
	case strings.Contains(s, "HeartbeatIntervalSecs"):
		return fmt.Errorf("心跳间隔必须大于 0 秒")
	case strings.Contains(s, "Transport"):
		return fmt.Errorf("传输方式无效，请选择 udp 或 tcp")
	default:
		return fmt.Errorf("配置无效：%s", s)
	}
}

func formatStartErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if strings.Contains(s, "binding SIP UDP") || strings.Contains(s, "address already in use") ||
		strings.Contains(s, "Only one usage of each socket address") || strings.Contains(s, "bind:") {
		return "启动失败：本地 SIP 端口已被占用，请更换端口后重试。"
	}
	return "启动失败：" + s
}
