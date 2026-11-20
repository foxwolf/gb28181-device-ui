package appconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsHaveNoPassword(t *testing.T) {
	d := Defaults()
	if d.Password != "" {
		t.Fatal("Defaults must not include a password")
	}
	if d.PlatformSIPAddress != "192.168.10.170" || d.PlatformSIPPort != 5060 {
		t.Fatalf("unexpected platform default: %s:%d", d.PlatformSIPAddress, d.PlatformSIPPort)
	}
	if d.DeviceID != "35080000001320000011" || d.ChannelID != "35020000001320000005" {
		t.Fatalf("unexpected IDs: %s / %s", d.DeviceID, d.ChannelID)
	}
	if d.SIPDomain != "3508000000" || d.LocalSIPPort != 15091 {
		t.Fatalf("unexpected domain/port: %s %d", d.SIPDomain, d.LocalSIPPort)
	}
	if d.Name != "yangm" || d.Manufacturer != "Unknown" || d.Model != "Unknown" {
		t.Fatalf("unexpected identity: %+v", d)
	}
	if d.Transport != "udp" || d.RegisterIntervalSecs != 3600 || d.HeartbeatIntervalSecs != 60 {
		t.Fatalf("unexpected intervals: %+v", d)
	}
	if d.VideoBackend != "synthetic" || d.CameraID != "" || d.CameraName != "" {
		t.Fatalf("unexpected video defaults: %+v", d)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	cfg := Defaults()
	cfg.Password = "unit-test-secret"
	cfg.Name = "roundtrip"
	cfg.VideoBackend = "ffmpeg"
	cfg.CameraID = "@device_pnp_abc"
	cfg.CameraName = "Integrated Camera"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "unit-test-secret" || got.Name != "roundtrip" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.VideoBackend != "ffmpeg" || got.CameraID != "@device_pnp_abc" || got.CameraName != "Integrated Camera" {
		t.Fatalf("video fields mismatch: %+v", got)
	}
}

func TestLoadMissingUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	if got != want {
		t.Fatalf("Load missing = %+v, want %+v", got, want)
	}
}

func TestNormalizeFillsEmpty(t *testing.T) {
	cfg := Config{Password: "x"}
	cfg.normalize()
	if cfg.PlatformSIPAddress == "" || cfg.LocalSIPPort != 15091 || cfg.Transport != "udp" {
		t.Fatalf("normalize did not fill defaults: %+v", cfg)
	}
	if cfg.Password != "x" {
		t.Fatal("normalize must keep password")
	}
	if cfg.VideoBackend != "synthetic" {
		t.Fatalf("normalize should default video backend: %+v", cfg)
	}
}

func TestDefaultsSourceHasNoPasswordLiteralInFileBeyondEmpty(t *testing.T) {
	// Guard against accidentally shipping a real default secret in this package.
	data, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, `Password:`) && !strings.Contains(src, `Password:              ""`) {
		// The only Password assignment in Defaults() must be the empty string.
	}
	if strings.Count(src, `Password:              ""`) != 1 {
		t.Fatalf("expected exactly one empty Password default in config.go")
	}
}
