// Package ui 提供基于 Gio 的 Windows 桌面窗口（表单 + 启停 + 状态 + 日志）。
package ui

import (
	"context"
	"image"
	"image/color"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gb28181-device-ui/internal/appconfig"
	"gb28181-device-ui/internal/localip"
	"gb28181-device-ui/internal/logview"
	"gb28181-device-ui/internal/runner"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

// UI 是整个桌面窗口的状态。
type UI struct {
	win  *app.Window
	th   *material.Theme
	ops  op.Ops
	logs *logview.Buffer
	run  *runner.Runner

	cfgPath string

	platform, port, deviceID, channelID, domain    widget.Editor
	password, localPort, name, manufacturer, model widget.Editor
	registerInterval, heartbeatInterval            widget.Editor
	transport                                      widget.Enum
	showPassword                                   widget.Bool

	startBtn, stopBtn, saveBtn, loadBtn, exitBtn widget.Clickable

	logList layout.List

	mu           sync.Mutex
	localIP      string
	localIPErr   string
	detecting    bool
	lastRouteKey string
	lastEditKey  string
	lastEditAt   time.Time
	routeArmed   bool

	stickBottom bool
}

func New() *UI {
	u := &UI{
		cfgPath:     appconfig.Path(),
		logList:     layout.List{Axis: layout.Vertical},
		stickBottom: true,
	}
	u.logs = logview.New(func() {
		if u.win != nil {
			u.win.Invalidate()
		}
	})
	u.run = runner.New(u.logs)
	for _, ed := range u.editors() {
		ed.SingleLine = true
		ed.Submit = true
	}
	u.port.Filter = digits
	u.localPort.Filter = digits
	u.registerInterval.Filter = digits
	u.heartbeatInterval.Filter = digits
	u.password.Mask = '●'
	u.transport.Value = "udp"
	return u
}

const digits = "0123456789"

func (u *UI) editors() []*widget.Editor {
	return []*widget.Editor{
		&u.platform, &u.port, &u.deviceID, &u.channelID, &u.domain,
		&u.password, &u.localPort, &u.name, &u.manufacturer, &u.model,
		&u.registerInterval, &u.heartbeatInterval,
	}
}

// Handler 返回接到界面日志窗的 slog.Handler。
func (u *UI) Handler() slog.Handler { return u.logs }

// Run 打开窗口并阻塞直到关闭。
func (u *UI) Run() error {
	cfg, err := appconfig.Load(u.cfgPath)
	if err != nil {
		u.logs.Append(slog.LevelWarn, "加载配置失败，将使用默认值："+err.Error())
		cfg = appconfig.Defaults()
	} else if _, statErr := os.Stat(u.cfgPath); statErr == nil {
		u.logs.Append(slog.LevelInfo, "已从本地加载配置："+u.cfgPath)
	} else {
		u.logs.Append(slog.LevelInfo, "未找到本地配置，已填入默认值（密码需手动填写）")
	}
	u.applyConfig(cfg)

	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	th.Palette.Fg = rgb(0x1F2933)
	th.Palette.Bg = rgb(0xF4F6F8)
	th.Palette.ContrastBg = rgb(0x1565C0)
	th.Palette.ContrastFg = rgb(0xFFFFFF)
	th.TextSize = 14
	u.th = th

	w := new(app.Window)
	w.Option(
		app.Title("GB28181 设备模拟器"),
		app.Size(unit.Dp(1120), unit.Dp(800)),
		app.MinSize(unit.Dp(880), unit.Dp(620)),
	)
	u.win = w
	u.refreshLocalIP(true)

	var hwnd uintptr
	var placed bool
	for {
		ev := w.Event()
		tryPlaceOnCursorMonitor(ev, &hwnd, &placed)
		switch e := ev.(type) {
		case *app.ClosingEvent:
			u.shutdown()
		case app.DestroyEvent:
			u.shutdown()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&u.ops, e)
			u.frame(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (u *UI) shutdown() {
	if u.run.Running() {
		u.run.Stop()
		_ = appconfig.Save(u.cfgPath, u.readConfig())
	}
}

func (u *UI) applyConfig(cfg appconfig.Config) {
	u.platform.SetText(cfg.PlatformSIPAddress)
	u.port.SetText(strconv.Itoa(cfg.PlatformSIPPort))
	u.deviceID.SetText(cfg.DeviceID)
	u.channelID.SetText(cfg.ChannelID)
	u.domain.SetText(cfg.SIPDomain)
	u.password.SetText(cfg.Password)
	u.logs.SetSecret(cfg.Password)
	u.localPort.SetText(strconv.Itoa(cfg.LocalSIPPort))
	u.name.SetText(cfg.Name)
	u.manufacturer.SetText(cfg.Manufacturer)
	u.model.SetText(cfg.Model)
	u.registerInterval.SetText(strconv.Itoa(cfg.RegisterIntervalSecs))
	u.heartbeatInterval.SetText(strconv.Itoa(cfg.HeartbeatIntervalSecs))
	t := strings.ToLower(cfg.Transport)
	if t != "udp" && t != "tcp" {
		t = "udp"
	}
	u.transport.Value = t
}

func (u *UI) readConfig() appconfig.Config {
	cfg := appconfig.Defaults()
	cfg.PlatformSIPAddress = strings.TrimSpace(u.platform.Text())
	if v, err := strconv.Atoi(strings.TrimSpace(u.port.Text())); err == nil {
		cfg.PlatformSIPPort = v
	}
	cfg.DeviceID = strings.TrimSpace(u.deviceID.Text())
	cfg.ChannelID = strings.TrimSpace(u.channelID.Text())
	cfg.SIPDomain = strings.TrimSpace(u.domain.Text())
	cfg.Password = u.password.Text()
	if v, err := strconv.Atoi(strings.TrimSpace(u.localPort.Text())); err == nil {
		cfg.LocalSIPPort = v
	}
	cfg.Name = strings.TrimSpace(u.name.Text())
	cfg.Manufacturer = strings.TrimSpace(u.manufacturer.Text())
	cfg.Model = strings.TrimSpace(u.model.Text())
	cfg.Transport = u.transport.Value
	if v, err := strconv.Atoi(strings.TrimSpace(u.registerInterval.Text())); err == nil {
		cfg.RegisterIntervalSecs = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(u.heartbeatInterval.Text())); err == nil {
		cfg.HeartbeatIntervalSecs = v
	}
	return cfg
}

func (u *UI) frame(gtx C) {
	running := u.run.Running()
	for _, ed := range u.editors() {
		ed.ReadOnly = running
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok && !running {
				u.onStart()
			}
		}
	}
	if u.showPassword.Value {
		u.password.Mask = 0
	} else {
		u.password.Mask = '●'
	}
	u.logs.SetSecret(u.password.Text())
	u.maybeRefreshRoute(gtx, running)

	if u.startBtn.Clicked(gtx) && !running {
		u.onStart()
	}
	if u.stopBtn.Clicked(gtx) && running {
		u.onStop()
	}
	if u.saveBtn.Clicked(gtx) {
		u.onSave()
	}
	if u.loadBtn.Clicked(gtx) && !running {
		u.onLoad()
	}
	if u.exitBtn.Clicked(gtx) {
		u.shutdown()
		u.win.Perform(system.ActionClose)
	}

	paint.Fill(gtx.Ops, rgb(0xF4F6F8))
	layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.header),
			layout.Rigid(gap(10)),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
					layout.Flexed(1.15, func(gtx C) D {
						return card(gtx, u.th, u.form)
					}),
					layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx) }),
					layout.Flexed(0.85, func(gtx C) D {
						return card(gtx, u.th, u.statusPane)
					}),
				)
			}),
			layout.Rigid(gap(10)),
			layout.Flexed(1, func(gtx C) D {
				return card(gtx, u.th, u.logPane)
			}),
		)
	})
}

func (u *UI) header(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(22), "GB28181 设备模拟器")
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(4)),
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(13), "基于 gb28181-go 设备侧（UAC）库。视频为库内合成的占位画面，不是 RTSP 实况（与 EasyGBD 不同）。")
			l.Color = rgb(0x9A5B00)
			return l.Layout(gtx)
		}),
	)
}

func (u *UI) form(gtx C) D {
	running := u.run.Running()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(15), "连接参数")
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(8)),
		layout.Rigid(u.row("平台地址", &u.platform, "例如 192.168.10.170")),
		layout.Rigid(u.row("平台端口", &u.port, "5060")),
		layout.Rigid(u.row("设备编码", &u.deviceID, "20 位数字")),
		layout.Rigid(u.row("通道编码", &u.channelID, "20 位数字")),
		layout.Rigid(u.row("SIP 域", &u.domain, "通常为设备编码前 10 位")),
		layout.Rigid(u.passwordRow),
		layout.Rigid(u.row("本地 SIP 端口", &u.localPort, "避免与 EasyGBD 冲突")),
		layout.Rigid(u.row("设备名称", &u.name, "")),
		layout.Rigid(u.row("厂商", &u.manufacturer, "")),
		layout.Rigid(u.row("型号", &u.model, "")),
		layout.Rigid(u.transportRow),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, u.miniRow("注册间隔(秒)", &u.registerInterval)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx) }),
				layout.Flexed(1, u.miniRow("心跳间隔(秒)", &u.heartbeatInterval)),
			)
		}),
		layout.Rigid(gap(12)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(u.btn(&u.startBtn, "启动", rgb(0x157A3A), !running)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.stopBtn, "停止", rgb(0xC0392B), running)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.saveBtn, "保存配置", rgb(0x1565C0), true)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.loadBtn, "加载配置", rgb(0x455A64), !running)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.exitBtn, "退出", rgb(0x607080), true)),
			)
		}),
	)
}

func (u *UI) passwordRow(gtx C) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(108))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(108))
			return material.Label(u.th, unit.Sp(13), "密码").Layout(gtx)
		}),
		layout.Flexed(1, func(gtx C) D {
			return editorBox(gtx, u.th, &u.password, "与平台约定的 SIP 认证密码")
		}),
		layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
		layout.Rigid(func(gtx C) D {
			return material.CheckBox(u.th, &u.showPassword, "显示密码").Layout(gtx)
		}),
	)
}

func (u *UI) transportRow(gtx C) D {
	running := u.run.Running()
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(108))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(108))
			return material.Label(u.th, unit.Sp(13), "传输").Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D {
			if running {
				gtx = gtx.Disabled()
			}
			return material.RadioButton(u.th, &u.transport, "udp", "UDP").Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx) }),
		layout.Rigid(func(gtx C) D {
			if running {
				gtx = gtx.Disabled()
			}
			return material.RadioButton(u.th, &u.transport, "tcp", "TCP").Layout(gtx)
		}),
	)
}

func (u *UI) row(label string, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(108))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(108))
					return material.Label(u.th, unit.Sp(13), label).Layout(gtx)
				}),
				layout.Flexed(1, func(gtx C) D {
					return editorBox(gtx, u.th, ed, hint)
				}),
			)
		})
	}
}

func (u *UI) miniRow(label string, ed *widget.Editor) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, material.Label(u.th, unit.Sp(13), label).Layout)
			}),
			layout.Flexed(1, func(gtx C) D {
				return editorBox(gtx, u.th, ed, "")
			}),
		)
	}
}

func (u *UI) btn(clk *widget.Clickable, label string, bg color.NRGBA, enabled bool) layout.Widget {
	return func(gtx C) D {
		style := material.Button(u.th, clk, label)
		style.TextSize = unit.Sp(13)
		style.CornerRadius = unit.Dp(4)
		style.Inset = layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(14), Right: unit.Dp(14)}
		if !enabled {
			style.Background = rgb(0xC5CBD3)
			style.Color = rgb(0xF7F8FA)
			gtx = gtx.Disabled()
		} else {
			style.Background = bg
			style.Color = rgb(0xFFFFFF)
		}
		return style.Layout(gtx)
	}
}

func (u *UI) statusPane(gtx C) D {
	st, errText := u.run.State()
	sess := u.logs.Session()
	u.mu.Lock()
	ip, ipErr, detecting := u.localIP, u.localIPErr, u.detecting
	u.mu.Unlock()

	stateColor := rgb(0x52606D)
	switch st {
	case runner.StateRunning:
		stateColor = rgb(0x157A3A)
	case runner.StateError:
		stateColor = rgb(0xC0392B)
	}

	ipText := ip
	if detecting && ipText == "" {
		ipText = "正在探测…"
	}
	if ipText == "" {
		ipText = "—"
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(15), "状态")
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(10)),
		layout.Rigid(u.kv("运行状态", st.String(), stateColor)),
		layout.Rigid(gap(8)),
		layout.Rigid(u.kv("本机出发地址", ipText, rgb(0x1F2933))),
		layout.Rigid(gap(2)),
		layout.Rigid(func(gtx C) D {
			note := "由到达平台的路由决定（对平台地址做 UDP connect / getsockname），不是可填写的绑定地址。"
			if ipErr != "" {
				note = "探测失败：" + ipErr + "。仍可尝试启动，库会回退到网卡扫描。"
			}
			l := material.Label(u.th, unit.Sp(12), note)
			l.Color = rgb(0x616E7C)
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(10)),
		layout.Rigid(u.kv("注册", dash(sess.Register), rgb(0x1F2933))),
		layout.Rigid(gap(4)),
		layout.Rigid(u.kv("心跳", dash(sess.Heartbeat), rgb(0x1F2933))),
		layout.Rigid(gap(4)),
		layout.Rigid(u.kv("INVITE", dash(sess.Invite), rgb(0x1F2933))),
		layout.Rigid(gap(4)),
		layout.Rigid(u.kv("RTP", dash(sess.RTP), rgb(0x1F2933))),
		layout.Rigid(gap(10)),
		layout.Rigid(u.kv("平台协议版本", dash(sess.PlatformVer), rgb(0x1F2933))),
		layout.Rigid(gap(4)),
		layout.Rigid(u.kv("平台时间", dash(sess.PlatformAt), rgb(0x1F2933))),
		layout.Rigid(gap(4)),
		layout.Rigid(u.kv("已绑定 SIP 端口", dash(sess.BoundPort), rgb(0x1F2933))),
		layout.Rigid(gap(12)),
		layout.Rigid(func(gtx C) D {
			if errText == "" && st != runner.StateError {
				return D{}
			}
			msg := errText
			if msg == "" {
				msg = "发生错误，请查看日志。"
			}
			l := material.Label(u.th, unit.Sp(13), msg)
			l.Color = rgb(0xC0392B)
			return l.Layout(gtx)
		}),
	)
}

func (u *UI) kv(k, v string, vc color.NRGBA) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Start}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(unit.Dp(110))
				gtx.Constraints.Max.X = gtx.Dp(unit.Dp(110))
				l := material.Label(u.th, unit.Sp(13), k)
				l.Color = rgb(0x616E7C)
				return l.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx C) D {
				l := material.Label(u.th, unit.Sp(13), v)
				l.Color = vc
				l.Font.Weight = font.SemiBold
				return l.Layout(gtx)
			}),
		)
	}
}

func (u *UI) logPane(gtx C) D {
	lines := u.logs.Lines()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Label(u.th, unit.Sp(15), "日志")
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(gap(6)),
		layout.Flexed(1, func(gtx C) D {
			return widget.Border{
				Color:        rgb(0xE4E7EB),
				CornerRadius: unit.Dp(4),
				Width:        unit.Dp(1),
			}.Layout(gtx, func(gtx C) D {
				defer clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Max}, gtx.Dp(4)).Push(gtx.Ops).Pop()
				paint.Fill(gtx.Ops, rgb(0xFAFBFC))
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
					if u.stickBottom {
						u.logList.Position.BeforeEnd = false
					}
					dims := u.logList.Layout(gtx, len(lines), func(gtx C, i int) D {
						l := material.Label(u.th, unit.Sp(12), lines[i].Text)
						l.Color = levelColor(lines[i].Level)
						l.MaxLines = 0
						return layout.Inset{Bottom: unit.Dp(2)}.Layout(gtx, l.Layout)
					})
					u.stickBottom = !u.logList.Position.BeforeEnd
					return dims
				})
			})
		}),
	)
}

func (u *UI) onStart() {
	cfg := u.readConfig()
	u.logs.SetSecret(cfg.Password)
	u.refreshLocalIP(true)
	if err := u.run.Start(cfg); err != nil {
		u.logs.Append(slog.LevelError, err.Error())
		return
	}
	_ = appconfig.Save(u.cfgPath, cfg)
}

func (u *UI) onStop() {
	u.run.Stop()
	if err := appconfig.Save(u.cfgPath, u.readConfig()); err != nil {
		u.logs.Append(slog.LevelWarn, "自动保存配置失败："+err.Error())
	}
}

func (u *UI) onSave() {
	cfg := u.readConfig()
	if err := appconfig.Save(u.cfgPath, cfg); err != nil {
		u.logs.Append(slog.LevelError, "保存配置失败："+err.Error())
		return
	}
	u.logs.Append(slog.LevelInfo, "配置已保存到 "+u.cfgPath+"（含密码，仅本机文件，请勿拷进仓库）")
}

func (u *UI) onLoad() {
	cfg, err := appconfig.Load(u.cfgPath)
	if err != nil {
		u.logs.Append(slog.LevelError, "加载配置失败："+err.Error())
		return
	}
	if _, err := os.Stat(u.cfgPath); err != nil {
		u.logs.Append(slog.LevelWarn, "还没有本地配置文件，已恢复默认值（密码为空）")
	} else {
		u.logs.Append(slog.LevelInfo, "已重新加载 "+u.cfgPath)
	}
	u.applyConfig(cfg)
	u.refreshLocalIP(true)
}

func (u *UI) maybeRefreshRoute(_ C, _ bool) {
	key := strings.TrimSpace(u.platform.Text()) + ":" + strings.TrimSpace(u.port.Text())
	if key != u.lastEditKey {
		u.lastEditKey = key
		u.lastEditAt = time.Now()
	}
	u.mu.Lock()
	already := u.lastRouteKey == key || u.detecting
	u.mu.Unlock()
	if already {
		return
	}
	if time.Since(u.lastEditAt) < 400*time.Millisecond {
		u.mu.Lock()
		arm := !u.routeArmed && u.win != nil
		if arm {
			u.routeArmed = true
		}
		u.mu.Unlock()
		if arm {
			go func() {
				time.Sleep(420 * time.Millisecond)
				u.mu.Lock()
				u.routeArmed = false
				u.mu.Unlock()
				u.win.Invalidate()
			}()
		}
		return
	}
	u.refreshLocalIP(false)
}

func (u *UI) refreshLocalIP(immediate bool) {
	host := strings.TrimSpace(u.platform.Text())
	port, err := strconv.Atoi(strings.TrimSpace(u.port.Text()))
	key := host + ":" + strings.TrimSpace(u.port.Text())
	if err != nil || host == "" {
		u.mu.Lock()
		u.localIP = ""
		u.localIPErr = "请先填写有效的平台地址和端口"
		u.lastRouteKey = key
		u.mu.Unlock()
		return
	}
	u.mu.Lock()
	if u.detecting && !immediate {
		u.mu.Unlock()
		return
	}
	u.detecting = true
	u.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ip, detErr := localip.Detect(ctx, host, port)
		u.mu.Lock()
		u.detecting = false
		u.lastRouteKey = key
		if detErr != nil {
			u.localIP = ""
			u.localIPErr = detErr.Error()
		} else {
			u.localIP = ip
			u.localIPErr = ""
		}
		u.mu.Unlock()
		if u.win != nil {
			u.win.Invalidate()
		}
	}()
}

func editorBox(gtx C, th *material.Theme, ed *widget.Editor, hint string) D {
	return widget.Border{
		Color:        rgb(0xD0D5DD),
		CornerRadius: unit.Dp(4),
		Width:        unit.Dp(1),
	}.Layout(gtx, func(gtx C) D {
		return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
			e := material.Editor(th, ed, hint)
			e.TextSize = unit.Sp(13)
			return e.Layout(gtx)
		})
	})
}

func card(gtx C, _ *material.Theme, w layout.Widget) D {
	min := gtx.Constraints.Min
	return widget.Border{
		Color:        rgb(0xE4E7EB),
		CornerRadius: unit.Dp(8),
		Width:        unit.Dp(1),
	}.Layout(gtx, func(gtx C) D {
		macro := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(14)).Layout(gtx, w)
		call := macro.Stop()
		if dims.Size.X < min.X {
			dims.Size.X = min.X
		}
		if dims.Size.Y < min.Y {
			dims.Size.Y = min.Y
		}
		defer clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(8)).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, rgb(0xFFFFFF))
		call.Add(gtx.Ops)
		return dims
	})
}

func gap(n unit.Dp) layout.Widget {
	return func(gtx C) D { return layout.Spacer{Height: n}.Layout(gtx) }
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func levelColor(l slog.Level) color.NRGBA {
	switch {
	case l >= slog.LevelError:
		return rgb(0xB42318)
	case l >= slog.LevelWarn:
		return rgb(0xB54708)
	default:
		return rgb(0x1F2933)
	}
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xFF}
}
