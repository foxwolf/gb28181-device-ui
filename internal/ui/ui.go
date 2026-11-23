// Package ui 提供基于 Gio 的 Windows 桌面窗口（表单 + 启停 + 状态 + 日志）。
package ui

import (
	"context"
	"image"
	"image/color"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
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
	"gb28181-device-ui/internal/videosrc"
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
	backend                                        widget.Enum
	refreshCamBtn, camBtn                          widget.Clickable
	camOpen                                        bool
	camClicks                                      []widget.Clickable
	lastBackend                                    string
	cameras                                        []videosrc.Device
	camErr                                         string
	camScanning                                    bool
	camPick                                        *videosrc.Device
	selectedCam                                    videosrc.Device

	preview         *previewHub
	previewMu       sync.Mutex
	previewSess     videosrc.Session
	previewCancel   context.CancelFunc
	previewKey      string
	previewStarting bool
	previewOp       paint.ImageOp
	previewDrawn    int
	previewExpanded bool
	previewPtr      struct{}
	previewPressed  bool
	previewPressPt  image.Point
	previewClickAt  time.Time
	previewClickPt  image.Point

	startBtn, stopBtn, saveBtn, loadBtn, exitBtn, copyLogsBtn, clearLogsBtn widget.Clickable

	formList      layout.List
	statusList    layout.List
	logList       widget.List
	logPtr        struct{}
	logStart      logPos
	logEnd        logPos
	logDragging   bool
	logFocused    bool
	logHits       []lineHit
	logWidth      int
	logPressLocal image.Point
	logCopiedAt   time.Time
	hwnd          uintptr
	winPtr        struct{}
	winPress      image.Point
	haveWinPress  bool
	logOrigin     image.Point
	haveLogOrigin bool
	frameSize     image.Point
	clientSize    image.Point

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
		formList:    layout.List{Axis: layout.Vertical},
		statusList:  layout.List{Axis: layout.Vertical},
		logList:     widget.List{List: layout.List{Axis: layout.Vertical}},
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
	u.backend.Value = videosrc.BackendSynthetic
	u.initPreview()
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
		app.Size(unit.Dp(1000), unit.Dp(780)),
		app.MinSize(unit.Dp(720), unit.Dp(560)),
	)
	u.win = w
	u.refreshLocalIP(true)

	var placed bool
	for {
		ev := w.Event()
		tryPlaceOnCursorMonitor(ev, &u.hwnd, &placed)
		switch e := ev.(type) {
		case *app.ClosingEvent:
			u.shutdown()
		case app.DestroyEvent:
			u.shutdown()
			return e.Err
		case app.FrameEvent:
			u.frameSize = e.Size
			gtx := app.NewContext(&u.ops, e)
			u.frame(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (u *UI) shutdown() {
	u.stopStandalonePreview(true)
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
	vb := strings.ToLower(strings.TrimSpace(cfg.VideoBackend))
	if vb == "" {
		vb = videosrc.BackendSynthetic
	}
	u.backend.Value = vb
	u.selectedCam = videosrc.Device{ID: cfg.CameraID, Name: cfg.CameraName}
	if vb == videosrc.BackendFFmpeg {
		u.refreshCameras()
	}
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
	cfg.VideoBackend = u.backend.Value
	if cfg.VideoBackend == "" {
		cfg.VideoBackend = videosrc.BackendSynthetic
	}
	cfg.CameraID = u.selectedCam.ID
	cfg.CameraName = u.selectedCam.Name
	return cfg
}

func (u *UI) frame(gtx C) {
	u.processWinPointer(gtx)
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &u.winPtr)

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
	if u.copyLogsBtn.Clicked(gtx) {
		u.copyLogsToClipboard(gtx)
	}
	if u.clearLogsBtn.Clicked(gtx) {
		u.clearLogs()
	}
	if u.exitBtn.Clicked(gtx) {
		u.shutdown()
		u.win.Perform(system.ActionClose)
	}

	u.handleCameraInput(gtx, running)
	u.syncPreview(running)

	if u.previewExpanded {
		u.layoutExpandedPreview(gtx)
		return
	}

	paint.Fill(gtx.Ops, rgb(0xF4F6F8))
	layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.header),
			layout.Rigid(gap(10)),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
					layout.Flexed(1.15, func(gtx C) D {
						return card(gtx, u.th, u.formPane)
					}),
					layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx) }),
					layout.Flexed(0.85, func(gtx C) D {
						return card(gtx, u.th, u.statusPane)
					}),
				)
			}),
			layout.Rigid(gap(10)),
			layout.Rigid(func(gtx C) D {
				h := logPaneHeight(gtx)
				gtx.Constraints.Min.Y = h
				gtx.Constraints.Max.Y = h
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
			l := material.Label(u.th, unit.Sp(13), "基于 gb28181-go 设备侧（UAC）库。选中摄像头后即可在右侧预览画面，启动后预览会继续显示。")
			l.Color = rgb(0x9A5B00)
			return l.Layout(gtx)
		}),
	)
}

func (u *UI) formPane(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, u.scrollForm),
		layout.Rigid(gap(8)),
		layout.Rigid(hairline),
		layout.Rigid(gap(10)),
		layout.Rigid(u.actionBar),
	)
}

func (u *UI) scrollForm(gtx C) D {
	return u.formList.Layout(gtx, 1, func(gtx C, _ int) D {
		return u.form(gtx)
	})
}

func (u *UI) actionBar(gtx C) D {
	running := u.run.Running()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, u.primaryBtn(&u.startBtn, "启动", rgb(0x157A3A), !running)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Flexed(1, u.primaryBtn(&u.stopBtn, "停止", rgb(0xC0392B), running)),
			)
		}),
		layout.Rigid(gap(8)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(u.btn(&u.saveBtn, "保存配置", rgb(0x1565C0), true)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.loadBtn, "加载配置", rgb(0x455A64), !running)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.exitBtn, "退出", rgb(0x607080), true)),
			)
		}),
	)
}

func (u *UI) form(gtx C) D {
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
		layout.Rigid(gap(8)),
		layout.Rigid(u.sourceRow),
		layout.Rigid(u.cameraRow),
		layout.Rigid(u.ffmpegHint),
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

func (u *UI) sourceRow(gtx C) D {
	running := u.run.Running()
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(unit.Dp(108))
				gtx.Constraints.Max.X = gtx.Dp(unit.Dp(108))
				return material.Label(u.th, unit.Sp(13), "视频源").Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if running {
					gtx = gtx.Disabled()
				}
				return material.RadioButton(u.th, &u.backend, videosrc.BackendSynthetic, "合成占位画面").Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx) }),
			layout.Rigid(func(gtx C) D {
				if running {
					gtx = gtx.Disabled()
				}
				return material.RadioButton(u.th, &u.backend, videosrc.BackendFFmpeg, "本机摄像头（FFmpeg）").Layout(gtx)
			}),
		)
	})
}

func (u *UI) cameraRow(gtx C) D {
	running := u.run.Running()
	ffmpeg := u.backend.Value == videosrc.BackendFFmpeg
	enabled := !running && ffmpeg
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(unit.Dp(108))
						gtx.Constraints.Max.X = gtx.Dp(unit.Dp(108))
						return material.Label(u.th, unit.Sp(13), "摄像头").Layout(gtx)
					}),
					layout.Flexed(1, func(gtx C) D {
						if !enabled {
							gtx = gtx.Disabled()
						}
						return u.cameraCombo(gtx, enabled)
					}),
					layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
					layout.Rigid(u.btn(&u.refreshCamBtn, "刷新", rgb(0x455A64), enabled)),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if !u.camOpen || !enabled {
					return D{}
				}
				return u.cameraMenu(gtx)
			}),
		)
	})
}

func (u *UI) cameraCombo(gtx C, enabled bool) D {
	return widget.Border{
		Color:        rgb(0xD0D5DD),
		CornerRadius: unit.Dp(4),
		Width:        unit.Dp(1),
	}.Layout(gtx, func(gtx C) D {
		return u.camBtn.Layout(gtx, func(gtx C) D {
			return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
				label := u.cameraLabel()
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						l := material.Label(u.th, unit.Sp(13), label)
						if !enabled {
							l.Color = rgb(0x9AA3AD)
						}
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						mark := "▾"
						if u.camOpen && enabled {
							mark = "▴"
						}
						l := material.Label(u.th, unit.Sp(13), mark)
						l.Color = rgb(0x616E7C)
						return l.Layout(gtx)
					}),
				)
			})
		})
	})
}

func (u *UI) cameraMenu(gtx C) D {
	u.mu.Lock()
	cams := append([]videosrc.Device(nil), u.cameras...)
	u.mu.Unlock()
	if len(cams) == 0 {
		return layout.Inset{Top: unit.Dp(4), Left: unit.Dp(108)}.Layout(gtx, func(gtx C) D {
			l := material.Label(u.th, unit.Sp(12), "没有可选择的摄像头")
			l.Color = rgb(0x616E7C)
			return l.Layout(gtx)
		})
	}
	if len(u.camClicks) != len(cams) {
		u.camClicks = make([]widget.Clickable, len(cams))
	}
	children := make([]layout.FlexChild, 0, len(cams))
	for i := range cams {
		i := i
		children = append(children, layout.Rigid(func(gtx C) D {
			return u.camClicks[i].Layout(gtx, func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
					l := material.Label(u.th, unit.Sp(13), cams[i].Name)
					if u.selectedCam.ID == cams[i].ID && cams[i].ID != "" {
						l.Font.Weight = font.SemiBold
						l.Color = rgb(0x1565C0)
					}
					return l.Layout(gtx)
				})
			})
		}))
	}
	return layout.Inset{Top: unit.Dp(4), Left: unit.Dp(108)}.Layout(gtx, func(gtx C) D {
		return widget.Border{
			Color:        rgb(0xD0D5DD),
			CornerRadius: unit.Dp(4),
			Width:        unit.Dp(1),
		}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

func (u *UI) cameraLabel() string {
	if u.backend.Value != videosrc.BackendFFmpeg {
		return "（合成占位，无需选择摄像头）"
	}
	u.mu.Lock()
	scanning := u.camScanning
	u.mu.Unlock()
	if scanning && u.selectedCam.Name == "" && u.selectedCam.ID == "" {
		return "正在扫描摄像头…"
	}
	if u.selectedCam.Name != "" {
		return u.selectedCam.Name
	}
	if u.selectedCam.ID != "" {
		return u.selectedCam.ID
	}
	return "请选择摄像头"
}

func (u *UI) ffmpegHint(gtx C) D {
	msg, warn := u.videoHint()
	if msg == "" {
		return D{}
	}
	return layout.Inset{Top: unit.Dp(4), Left: unit.Dp(108)}.Layout(gtx, func(gtx C) D {
		l := material.Label(u.th, unit.Sp(12), msg)
		if warn {
			l.Color = rgb(0x9A5B00)
		} else {
			l.Color = rgb(0x616E7C)
		}
		return l.Layout(gtx)
	})
}

func (u *UI) videoHint() (string, bool) {
	if u.backend.Value != videosrc.BackendFFmpeg {
		return "", false
	}
	if b, ok := videosrc.Lookup(videosrc.BackendFFmpeg); ok {
		if err := b.Available(); err != nil {
			return "未找到 ffmpeg.exe。请将它放到本程序同一目录，或加入系统 PATH。", true
		}
	}
	u.mu.Lock()
	scanning, errText, n := u.camScanning, u.camErr, len(u.cameras)
	u.mu.Unlock()
	if scanning {
		return "正在扫描本机摄像头…", false
	}
	if errText != "" {
		return errText, true
	}
	if n == 0 {
		return "未扫描到摄像头。请确认已安装摄像头驱动，并点击「刷新」。", true
	}
	return "选中摄像头后即可预览；启动后预览会继续显示，并由同一路采集推流。", false
}

func (u *UI) handleCameraInput(gtx C, running bool) {
	prevCam := u.selectedCam
	prevBackend := u.lastBackend

	u.mu.Lock()
	if u.camPick != nil {
		u.selectedCam = *u.camPick
		u.camPick = nil
	}
	u.mu.Unlock()

	if u.refreshCamBtn.Clicked(gtx) && !running && u.backend.Value == videosrc.BackendFFmpeg {
		u.restartPreview()
		u.refreshCameras()
	}
	if u.backend.Value == videosrc.BackendFFmpeg && u.lastBackend != videosrc.BackendFFmpeg {
		u.refreshCameras()
	}
	if !running && prevBackend != "" && u.backend.Value != prevBackend {
		u.restartPreview()
	}
	u.lastBackend = u.backend.Value
	if u.backend.Value != videosrc.BackendFFmpeg || running {
		u.camOpen = false
	} else if u.camBtn.Clicked(gtx) {
		u.camOpen = !u.camOpen
	}

	u.mu.Lock()
	cams := append([]videosrc.Device(nil), u.cameras...)
	u.mu.Unlock()
	if len(u.camClicks) != len(cams) {
		u.camClicks = make([]widget.Clickable, len(cams))
	}
	if !running && u.camOpen {
		for i := range u.camClicks {
			if i < len(cams) && u.camClicks[i].Clicked(gtx) {
				u.selectedCam = cams[i]
				u.camOpen = false
			}
		}
	}
	if !running && videosrcDeviceKey(prevCam) != videosrcDeviceKey(u.selectedCam) {
		u.restartPreview()
	}
}

func videosrcDeviceKey(d videosrc.Device) string {
	id := strings.TrimSpace(d.ID)
	if id == "" {
		id = strings.TrimSpace(d.Name)
	}
	return id
}

func (u *UI) refreshCameras() {
	u.mu.Lock()
	if u.camScanning {
		u.mu.Unlock()
		return
	}
	u.camScanning = true
	u.camErr = ""
	u.mu.Unlock()
	sel := u.selectedCam
	if u.win != nil {
		u.win.Invalidate()
	}
	go func() {
		defer func() {
			u.mu.Lock()
			u.camScanning = false
			u.mu.Unlock()
			if u.win != nil {
				u.win.Invalidate()
			}
		}()
		b, ok := videosrc.Lookup(videosrc.BackendFFmpeg)
		if !ok {
			u.mu.Lock()
			u.camErr = "未登记 FFmpeg 视频源"
			u.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		devs, err := b.Devices(ctx)
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			u.camErr = err.Error()
			return
		}
		u.cameras = devs
		u.camErr = ""
		var pick *videosrc.Device
		if sel.ID != "" || sel.Name != "" {
			for i := range devs {
				d := devs[i]
				if (sel.ID != "" && d.ID == sel.ID) || d.Name == sel.Name || (sel.ID != "" && d.Name == sel.ID) {
					dd := d
					pick = &dd
					break
				}
			}
		}
		if pick == nil && len(devs) > 0 && sel.ID == "" && sel.Name == "" {
			dd := devs[0]
			pick = &dd
		}
		if pick != nil {
			u.camPick = pick
		}
	}()
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
	return u.styledBtn(clk, label, bg, enabled, unit.Sp(13), layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(14), Right: unit.Dp(14)}, false)
}

func (u *UI) primaryBtn(clk *widget.Clickable, label string, bg color.NRGBA, enabled bool) layout.Widget {
	return u.styledBtn(clk, label, bg, enabled, unit.Sp(15), layout.Inset{Top: unit.Dp(9), Bottom: unit.Dp(9), Left: unit.Dp(16), Right: unit.Dp(16)}, true)
}

func (u *UI) styledBtn(clk *widget.Clickable, label string, bg color.NRGBA, enabled bool, size unit.Sp, inset layout.Inset, fill bool) layout.Widget {
	return func(gtx C) D {
		if fill {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
		}
		style := material.Button(u.th, clk, label)
		style.TextSize = size
		style.CornerRadius = unit.Dp(4)
		style.Inset = inset
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
		layout.Rigid(u.previewPane),
		layout.Rigid(gap(12)),
		layout.Flexed(1, func(gtx C) D {
			return u.statusList.Layout(gtx, 1, func(gtx C, _ int) D {
				return u.statusBody(gtx, st, errText, sess, ipText, ipErr, stateColor)
			})
		}),
	)
}

func (u *UI) statusBody(gtx C, st runner.State, errText string, sess logview.Session, ipText, ipErr string, stateColor color.NRGBA) D {
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
	copyLabel := "复制日志"
	if !u.logCopiedAt.IsZero() && time.Since(u.logCopiedAt) < 2*time.Second {
		copyLabel = "已复制"
		gtx.Execute(op.InvalidateCmd{At: u.logCopiedAt.Add(2 * time.Second)})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					l := material.Label(u.th, unit.Sp(15), "日志（可划选后 Ctrl+C）")
					l.Font.Weight = font.SemiBold
					return l.Layout(gtx)
				}),
				layout.Rigid(u.btn(&u.copyLogsBtn, copyLabel, rgb(0x455A64), true)),
				layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
				layout.Rigid(u.btn(&u.clearLogsBtn, "清除日志", rgb(0x607080), true)),
			)
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
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, u.layoutLogText)
			})
		}),
	)
}

func (u *UI) copyLogsToClipboard(gtx C) {
	text := u.selectedLogText()
	if text == "" {
		text = u.logs.Dump()
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
	u.logCopiedAt = time.Now()
	if u.win != nil {
		u.win.Invalidate()
	}
}

func (u *UI) onStart() {
	cfg := u.readConfig()
	u.logs.SetSecret(cfg.Password)
	u.refreshLocalIP(true)
	// 同一摄像头不能被打开两次：先停独立预览，再启动带预览的双输出采集。
	u.stopStandalonePreview(false)
	if err := u.run.Start(cfg, u.preview); err != nil {
		u.logs.Append(slog.LevelError, err.Error())
		u.syncPreview(false)
		return
	}
	_ = appconfig.Save(u.cfgPath, cfg)
}

func (u *UI) onStop() {
	u.run.Stop()
	if err := appconfig.Save(u.cfgPath, u.readConfig()); err != nil {
		u.logs.Append(slog.LevelWarn, "自动保存配置失败："+err.Error())
	}
	u.syncPreview(false)
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

func logPaneHeight(gtx C) int {
	avail := gtx.Constraints.Max.Y
	minH := gtx.Dp(unit.Dp(120))
	maxH := gtx.Dp(unit.Dp(168))
	h := avail * 24 / 100
	if h < minH {
		h = minH
	}
	if h > maxH {
		h = maxH
	}
	if avail > 0 && h > avail/2 {
		h = avail / 2
	}
	if h < 1 {
		h = minH
	}
	return h
}

func gap(n unit.Dp) layout.Widget {
	return func(gtx C) D { return layout.Spacer{Height: n}.Layout(gtx) }
}

func hairline(gtx C) D {
	h := gtx.Dp(unit.Dp(1))
	w := gtx.Constraints.Max.X
	if w < 1 {
		w = gtx.Constraints.Min.X
	}
	defer clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, rgb(0xE4E7EB))
	return D{Size: image.Pt(w, h)}
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
