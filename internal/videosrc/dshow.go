package videosrc

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// FFmpeg 7 及更早在列表里用 [dshow @ ...]；FFmpeg 8 改为 [in#0 @ ...]。
const dshowLogPrefix = `\[(?:dshow|in#\d+) @ [^\]]*\]`

var (
	dshowNameRe = regexp.MustCompile(dshowLogPrefix + `\s+"([^"]+)"`)
	dshowAltRe  = regexp.MustCompile(dshowLogPrefix + `\s+Alternative name\s+"([^"]+)"`)
)

// parseDShowDevices 从 ffmpeg -list_devices 的 stderr 中解析 DirectShow 视频设备。
// 忽略音频设备。优先把 Alternative name 作为 Device.ID。
func parseDShowDevices(output string) []Device {
	videoSection := false
	seenSection := false
	var out []Device
	var cur *Device

	flush := func() {
		if cur == nil {
			return
		}
		if cur.ID == "" {
			cur.ID = cur.Name
		}
		if cur.Name == "" {
			cur.Name = cur.ID
		}
		out = append(out, *cur)
		cur = nil
	}

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)

		if strings.Contains(lower, "directshow video devices") {
			flush()
			videoSection = true
			seenSection = true
			continue
		}
		if strings.Contains(lower, "directshow audio devices") {
			flush()
			videoSection = false
			seenSection = true
			continue
		}

		if alt := dshowAltRe.FindStringSubmatch(line); len(alt) == 2 {
			if cur != nil {
				cur.ID = alt[1]
			}
			continue
		}

		if name := dshowNameRe.FindStringSubmatch(line); len(name) == 2 {
			// 无章节标题时，用 "(audio)" / "(video)" 兜底；默认当作视频。
			isAudio := strings.Contains(lower, "(audio)")
			isVideo := strings.Contains(lower, "(video)")
			inVideo := videoSection
			if !seenSection {
				inVideo = !isAudio
			} else if isAudio && !isVideo {
				inVideo = false
			} else if isVideo {
				inVideo = true
			}
			flush()
			if !inVideo {
				continue
			}
			cur = &Device{Name: name[1], ID: name[1]}
			continue
		}
	}
	flush()
	return out
}

// dshowVideoInput 构造传给 ffmpeg -i 的单个参数。
// 由 os/exec 作为独立参数传递，不再额外加引号：FFmpeg 8 会把引号当成设备名的一部分，从而导致打不开摄像头。
func dshowVideoInput(id string) string {
	id = strings.TrimSpace(id)
	if len(id) >= 2 && id[0] == '"' && id[len(id)-1] == '"' {
		id = id[1 : len(id)-1]
	}
	return "video=" + id
}

// decodeFFmpegOutput 把 ffmpeg 的输出转成字符串。
// 管道里在中文 Windows 上有时是系统 ANSI（GBK）而不是 UTF-8。
func decodeFFmpegOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil && utf8.Valid(s) {
		return string(s)
	}
	return string(b)
}
