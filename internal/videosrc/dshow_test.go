package videosrc

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const sampleDShowList = `ffmpeg version 7.0 Copyright (c) 2000-2024 the FFmpeg developers
[dshow @ 0000024b0c0aabb] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0000024b0c0aabb]  "Integrated Camera"
[dshow @ 0000024b0c0aabb]     Alternative name "@device_pnp_\\?\usb#vid_04f2&pid_b67c&mi_00#6&2d0c34c3&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[dshow @ 0000024b0c0aabb]  "OBS Virtual Camera"
[dshow @ 0000024b0c0aabb]     Alternative name "@device_sw_{860BB310-5D01-11D0-BD3B-00A0C911CE86}\{some-guid}"
[dshow @ 0000024b0c0aabb]  "USB 摄像头"
[dshow @ 0000024b0c0aabb]     Alternative name "@device_pnp_\\?\usb#vid_1234&pid_5678#6&abcd&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[dshow @ 0000024b0c0aabb] DirectShow audio devices
[dshow @ 0000024b0c0aabb]  "Microphone Array"
[dshow @ 0000024b0c0aabb]     Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave"
[dshow @ 0000024b0c0aabb]  "Stereo Mix"
[dshow @ 0000024b0c0aabb]     Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\mix"
`

func TestParseDShowDevices_SkipsAudio(t *testing.T) {
	devs := parseDShowDevices(sampleDShowList)
	if len(devs) != 3 {
		t.Fatalf("got %d devices: %+v", len(devs), devs)
	}
	if devs[0].Name != "Integrated Camera" {
		t.Fatalf("name[0]=%q", devs[0].Name)
	}
	if !strings.HasPrefix(devs[0].ID, "@device_pnp_") {
		t.Fatalf("id[0] should be alternative name, got %q", devs[0].ID)
	}
	if !strings.Contains(devs[0].ID, `usb#vid_04f2`) {
		t.Fatalf("id[0]=%q", devs[0].ID)
	}
	if devs[1].Name != "OBS Virtual Camera" {
		t.Fatalf("name[1]=%q", devs[1].Name)
	}
	if devs[2].Name != "USB 摄像头" {
		t.Fatalf("name[2]=%q", devs[2].Name)
	}
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), "microphone") || strings.Contains(d.Name, "Stereo Mix") {
			t.Fatalf("audio device leaked: %+v", d)
		}
	}
}

func TestParseDShowDevices_NoAlternativeName(t *testing.T) {
	in := `[dshow @ 1] DirectShow video devices
[dshow @ 1]  "Camera One"
[dshow @ 1] DirectShow audio devices
[dshow @ 1]  "Mic"
`
	devs := parseDShowDevices(in)
	if len(devs) != 1 || devs[0].ID != "Camera One" || devs[0].Name != "Camera One" {
		t.Fatalf("got %+v", devs)
	}
}

func TestParseDShowDevices_EmptyOrNone(t *testing.T) {
	in := `[dshow @ 1] DirectShow video devices (some may be both video and audio devices)
[dshow @ 1] Could not enumerate video devices (or none found).
[dshow @ 1] DirectShow audio devices
[dshow @ 1]  "Microphone"
`
	devs := parseDShowDevices(in)
	if len(devs) != 0 {
		t.Fatalf("want empty, got %+v", devs)
	}
}

func TestParseDShowDevices_FFmpeg8InPrefix(t *testing.T) {
	// FFmpeg 8.x 的 -list_devices 不再使用 [dshow @ ...] 和章节标题。
	in := `[in#0 @ 000001e607454dc0] "HD Camera" (video)
[in#0 @ 000001e607454dc0]   Alternative name "@device_pnp_\\?\usb#vid_5858&pid_133a&mi_00#6&abcd&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[in#0 @ 000001e607454dc0] "ToDesk Camera" (video)
[in#0 @ 000001e607454dc0]   Alternative name "@device_sw_{860BB310-5D01-11D0-BD3B-00A0C911CE86}\{8A9B8FA2-8893-4C7D-9385-A22AF40A37B0}"
[in#0 @ 000001e607454dc0] "本机麦克风 (适用于数字麦克风的英特尔智音技术)" (audio)
[in#0 @ 000001e607454dc0]   Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{C597104B-A065-47E3-AAA8-4B9D77EB0BD1}"
[in#0 @ 000001e607454dc0] "麦克风 (ToDesk Virtual Audio)" (audio)
[in#0 @ 000001e607454dc0]   Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{325CBC5A-9EE8-4F34-B7F4-9BC7CEA85AAD}"
Error opening input file dummy.
Error opening input files: Invalid argument
`
	devs := parseDShowDevices(in)
	if len(devs) != 2 {
		t.Fatalf("got %d devices: %+v", len(devs), devs)
	}
	if devs[0].Name != "HD Camera" || !strings.HasPrefix(devs[0].ID, "@device_pnp_") {
		t.Fatalf("dev[0]=%+v", devs[0])
	}
	if devs[1].Name != "ToDesk Camera" || !strings.HasPrefix(devs[1].ID, "@device_sw_") {
		t.Fatalf("dev[1]=%+v", devs[1])
	}
}

func TestParseDShowDevices_VideoTagWithoutSection(t *testing.T) {
	in := `[dshow @ 1] "HD WebCam" (video)
[dshow @ 1]     Alternative name "@device_pnp_\\?\usb#vid_abcd"
[dshow @ 1] "Microphone" (audio)
[dshow @ 1]     Alternative name "@device_cm_{mic}"
`
	devs := parseDShowDevices(in)
	if len(devs) != 1 || devs[0].Name != "HD WebCam" || !strings.HasPrefix(devs[0].ID, "@device_pnp_") {
		t.Fatalf("got %+v", devs)
	}
}

func TestDShowQuoteAndInput(t *testing.T) {
	got := dshowVideoInput(`Integrated Camera`)
	if got != `video=Integrated Camera` {
		t.Fatalf("spaces: %q", got)
	}
	got = dshowVideoInput(`Cam, Front`)
	if got != `video=Cam, Front` {
		t.Fatalf("comma: %q", got)
	}
	alt := `@device_pnp_\\?\usb#vid_04f2&pid_b67c`
	got = dshowVideoInput(alt)
	if got != `video=`+alt {
		t.Fatalf("alt: got %q", got)
	}
	// os/exec 按参数传递，名字里的引号应保留为设备名的一部分，但不要再外包一层引号。
	got = dshowVideoInput(`say "hi"`)
	if got != `video=say "hi"` {
		t.Fatalf("quotes: %q", got)
	}
	got = dshowVideoInput(`"@device_pnp_abc"`)
	if got != `video=@device_pnp_abc` {
		t.Fatalf("strip wrapping quotes: %q", got)
	}
}

func TestDecodeFFmpegOutput_GBK(t *testing.T) {
	src := []byte("USB 摄像头")
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(gbk, src) {
		t.Skip("GBK encoding produced UTF-8; nothing to test")
	}
	if got := decodeFFmpegOutput(gbk); got != "USB 摄像头" {
		t.Fatalf("got %q", got)
	}
	if got := decodeFFmpegOutput(src); got != "USB 摄像头" {
		t.Fatalf("utf8: %q", got)
	}
}

func TestCaptureArgsDefaultCommand(t *testing.T) {
	args := captureArgs("@device_pnp_abc", Options{}, []string{
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-bf", "0",
		"-g", "25",
		"-keyint_min", "25",
	}, 0)
	got := strings.Join(args, " ")
	wantParts := []string{
		"-hide_banner", "-nostdin", "-loglevel error",
		"-f dshow", "-rtbufsize 100M",
		`-i video=@device_pnp_abc`,
		"-an", "-pix_fmt yuv420p",
		"-c:v libx264", "-preset veryfast", "-tune zerolatency",
		"-profile:v baseline", "-bf 0", "-g 25", "-keyint_min 25",
		"-bsf:v h264_mp4toannexb", "-f h264", "pipe:1",
	}
	for _, p := range wantParts {
		if !strings.Contains(got, p) {
			t.Fatalf("missing %q in %q", p, got)
		}
	}
	if strings.Contains(got, " -r ") || strings.Contains(got, " -s ") {
		t.Fatalf("default opts should not add -r/-s: %q", got)
	}
}

func TestRegistryListsSyntheticAndFFmpeg(t *testing.T) {
	ids := map[string]bool{}
	for _, b := range ListBackends() {
		ids[b.ID()] = true
	}
	if !ids[BackendSynthetic] || !ids[BackendFFmpeg] {
		t.Fatalf("registry = %v", ids)
	}
	if _, ok := Lookup("native"); ok {
		t.Fatal("native backend must not be registered yet")
	}
	syn, ok := Lookup(BackendSynthetic)
	if !ok || syn.Available() != nil {
		t.Fatal("synthetic should always be available")
	}
	devs, err := syn.Devices(t.Context())
	if err != nil || len(devs) != 1 {
		t.Fatalf("synthetic devices: %v %v", devs, err)
	}
}

func TestDShowQuoteChinese(t *testing.T) {
	got := dshowVideoInput("USB 摄像头")
	if got != `video=USB 摄像头` {
		t.Fatalf("%q", got)
	}
}
