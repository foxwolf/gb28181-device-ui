package videosrc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var (
	errFFmpegNotFound = errors.New("未找到 ffmpeg。请将 ffmpeg.exe 放到本程序同一目录，或加入系统 PATH")
	errNilSink        = errors.New("视频输出（Sink）不能为空")
	errNilPreview     = errors.New("预览输出不能为空")
	errNoCamera       = errors.New("请先选择一个摄像头，或点击「刷新」扫描本机设备")
	errFFmpegNotWin   = errors.New("本机摄像头采集目前仅支持 Windows（DirectShow）")
	errNoH264Encoder  = errors.New("当前 ffmpeg 未包含 libx264 或 h264_mf 编码器，请更换带 H.264 编码的 Windows 构建")
)

// FindFFmpeg 先在可执行文件同目录查找，再查 PATH。
// Windows 上文件名是 ffmpeg.exe，其他系统是 ffmpeg。
func FindFFmpeg() (string, error) {
	names := []string{"ffmpeg"}
	if runtime.GOOS == "windows" {
		names = []string{"ffmpeg.exe", "ffmpeg"}
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		for _, n := range names {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errFFmpegNotFound
}
