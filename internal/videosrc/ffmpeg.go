package videosrc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const BackendFFmpeg = "ffmpeg"

func init() {
	Register(NewFFmpeg())
}

// FFmpeg 通过外部 ffmpeg 进程采集本机摄像头（Windows DirectShow）。
type FFmpeg struct {
	encOnce sync.Once
	encName string // "libx264" 或 "h264_mf"
	encErr  error
	encArgs []string
}

func NewFFmpeg() *FFmpeg { return &FFmpeg{} }

func (f *FFmpeg) ID() string    { return BackendFFmpeg }
func (f *FFmpeg) Label() string { return "本机摄像头（FFmpeg）" }

func (f *FFmpeg) Available() error {
	_, err := FindFFmpeg()
	return err
}

func (f *FFmpeg) Devices(ctx context.Context) ([]Device, error) {
	if runtime.GOOS != "windows" {
		return nil, errFFmpegNotWin
	}
	bin, err := FindFFmpeg()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	cmd.SysProcAttr = cmdSysProcAttr()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // list_devices 通常以非 0 退出
	if ctx.Err() != nil {
		return nil, fmt.Errorf("扫描摄像头超时或已取消")
	}
	text := decodeFFmpegOutput(stderr.Bytes()) + "\n" + decodeFFmpegOutput(stdout.Bytes())
	devs := parseDShowDevices(text)
	return devs, nil
}

func (f *FFmpeg) Open(ctx context.Context, dev Device, opts Options, dest Sink) (Session, error) {
	if dest == nil {
		return nil, errNilSink
	}
	if runtime.GOOS != "windows" {
		return nil, errFFmpegNotWin
	}
	bin, err := FindFFmpeg()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(dev.ID)
	if id == "" {
		id = strings.TrimSpace(dev.Name)
	}
	if id == "" {
		return nil, errNoCamera
	}
	encArgs, err := f.encoderArgs(ctx, bin)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	args := captureArgs(id, opts, encArgs)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = cmdSysProcAttr()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("无法读取 ffmpeg 输出：%w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("无法读取 ffmpeg 日志：%w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("无法启动 ffmpeg：%w", err)
	}

	s := &ffmpegSession{
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan error, 1),
	}

	var stderrMu sync.Mutex
	var stderrTail strings.Builder
	go drainFFmpegLog(stderr, &stderrMu, &stderrTail)

	started := make(chan struct{})
	var startOnce sync.Once
	signalStart := func() { startOnce.Do(func() { close(started) }) }

	go func() {
		err := readAnnexB(ctx, stdout, dest, signalStart)
		_ = stdout.Close()
		killCmd(cmd)
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			s.finish(nil)
			return
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			s.finish(fmt.Errorf("读取摄像头码流失败：%v", err))
			return
		}
		if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			msg := ffmpegFailMessage(&stderrMu, &stderrTail, waitErr)
			s.finish(errors.New(msg))
			return
		}
		s.finish(nil)
	}()

	fail := func(err error) (Session, error) {
		cancel()
		killCmd(cmd)
		if err == nil {
			err = errors.New("ffmpeg 在输出码流前已退出")
		}
		return nil, fmt.Errorf("启动摄像头采集失败：%w", err)
	}

	slog.Info("界面：已启动 FFmpeg 采集进程", "encoder", f.encName, "camera", strings.TrimSpace(dev.Name))

	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case err := <-s.done:
		return fail(err)
	case <-started:
		// 刚写出数据又立刻退出时，优先当作启动失败，避免界面误以为已经在采集。
		select {
		case err := <-s.done:
			return fail(err)
		default:
			return s, nil
		}
	case <-ctx.Done():
		cancel()
		killCmd(cmd)
		<-s.done
		return nil, fmt.Errorf("启动摄像头采集已取消")
	case <-timer.C:
		// 进程仍在运行但尚未出帧：部分摄像头初始化较慢，先交给界面继续启动。
		select {
		case err := <-s.done:
			return fail(err)
		default:
			return s, nil
		}
	}
}

func captureArgs(deviceID string, opts Options, encArgs []string) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "dshow", "-rtbufsize", "100M",
		"-i", dshowVideoInput(deviceID),
		"-an",
		"-pix_fmt", "yuv420p",
	}
	if opts.Width > 0 && opts.Height > 0 {
		args = append(args, "-s", fmt.Sprintf("%dx%d", opts.Width, opts.Height))
	}
	if opts.FPS > 0 {
		args = append(args, "-r", fmt.Sprintf("%d", opts.FPS))
	}
	args = append(args, encArgs...)
	args = append(args, "-bsf:v", "h264_mp4toannexb", "-f", "h264", "pipe:1")
	return args
}

func (f *FFmpeg) encoderArgs(_ context.Context, bin string) ([]string, error) {
	f.encOnce.Do(func() {
		pctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		f.encName, f.encArgs, f.encErr = probeH264Encoder(pctx, bin)
	})
	if f.encErr != nil {
		return nil, f.encErr
	}
	return f.encArgs, nil
}

func probeH264Encoder(ctx context.Context, bin string) (string, []string, error) {
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-encoders")
	cmd.SysProcAttr = cmdSysProcAttr()
	out, err := cmd.CombinedOutput()
	text := decodeFFmpegOutput(out)
	if err != nil && text == "" {
		return "", nil, fmt.Errorf("无法查询 ffmpeg 编码器：%w", err)
	}
	hasX264 := strings.Contains(text, "libx264")
	hasMF := strings.Contains(text, "h264_mf")
	switch {
	case hasX264:
		return "libx264", []string{
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "baseline",
			"-bf", "0",
			"-g", "25",
			"-keyint_min", "25",
		}, nil
	case hasMF:
		// 部分 Windows 构建只有 Media Foundation 编码器，不含 libx264。
		return "h264_mf", []string{
			"-c:v", "h264_mf",
			"-profile:v", "baseline",
			"-bf", "0",
			"-g", "25",
		}, nil
	default:
		return "", nil, errNoH264Encoder
	}
}

func readAnnexB(ctx context.Context, r io.Reader, dest Sink, onByte func()) error {
	var split AnnexBSplitter
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			for _, au := range split.Flush() {
				dest.Write(au)
			}
			return ctx.Err()
		}
		n, err := r.Read(buf)
		if n > 0 {
			if onByte != nil {
				onByte()
			}
			for _, au := range split.Push(buf[:n]) {
				dest.Write(au)
			}
		}
		if err != nil {
			for _, au := range split.Flush() {
				dest.Write(au)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func drainFFmpegLog(r io.Reader, mu *sync.Mutex, tail *strings.Builder) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		slog.Warn("ffmpeg", "msg", line)
		mu.Lock()
		if tail.Len() > 0 {
			tail.WriteByte('\n')
		}
		if tail.Len() < 2048 {
			tail.WriteString(line)
		}
		mu.Unlock()
	}
}

func ffmpegFailMessage(mu *sync.Mutex, tail *strings.Builder, waitErr error) string {
	mu.Lock()
	log := strings.TrimSpace(tail.String())
	mu.Unlock()
	switch {
	case strings.Contains(strings.ToLower(log), "unknown encoder") && strings.Contains(log, "libx264"):
		return "当前 ffmpeg 不包含 libx264，且未能使用 h264_mf。请更换带 H.264 编码的 Windows 构建"
	case strings.Contains(strings.ToLower(log), "i/o error"),
		strings.Contains(strings.ToLower(log), "could not find video"),
		strings.Contains(strings.ToLower(log), "cannot find"),
		strings.Contains(log, "Failed to query") || strings.Contains(log, "Failed to"):
		if log != "" {
			return "无法打开摄像头：" + firstLine(log)
		}
	}
	if log != "" {
		return "ffmpeg 退出：" + firstLine(log)
	}
	return "ffmpeg 退出：" + waitErr.Error()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

type ffmpegSession struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan error
	closed atomic.Bool
	once   atomic.Bool
}

func (s *ffmpegSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	killCmd(s.cmd)
	return nil
}

func (s *ffmpegSession) Wait() error {
	err, ok := <-s.done
	if !ok {
		return nil
	}
	return err
}

func (s *ffmpegSession) finish(err error) {
	if s.once.Swap(true) {
		return
	}
	s.done <- err
	close(s.done)
}
