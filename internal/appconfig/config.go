// Package appconfig 负责设备界面配置的默认值、以及可执行文件旁边的本地 JSON 读写。
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const FileName = "gb28181-device-ui.json"

// Config 是界面表单对应的可序列化配置。Password 只写入本机配置文件，
// 不会出现在仓库样例或源码默认值里。
type Config struct {
	PlatformSIPAddress    string `json:"platform_sip_address"`
	PlatformSIPPort       int    `json:"platform_sip_port"`
	DeviceID              string `json:"device_id"`
	ChannelID             string `json:"channel_id"`
	SIPDomain             string `json:"sip_domain"`
	Password              string `json:"password,omitempty"`
	LocalSIPPort          int    `json:"local_sip_port"`
	Name                  string `json:"name"`
	Manufacturer          string `json:"manufacturer"`
	Model                 string `json:"model"`
	Transport             string `json:"transport"`
	RegisterIntervalSecs  int    `json:"register_interval_secs"`
	HeartbeatIntervalSecs int    `json:"heartbeat_interval_secs"`
	// VideoBackend 是采集后端 ID：synthetic（默认）或 ffmpeg。日后可扩展 native。
	VideoBackend string `json:"video_backend,omitempty"`
	CameraID     string `json:"camera_id,omitempty"`
	CameraName   string `json:"camera_name,omitempty"`
}

// Defaults 返回不含密码的非敏感测试默认值（与当前 EasyGBD / sdsipsignal 联调一致）。
func Defaults() Config {
	return Config{
		PlatformSIPAddress:    "192.168.10.170",
		PlatformSIPPort:       5060,
		DeviceID:              "35080000001320000011",
		ChannelID:             "35020000001320000005",
		SIPDomain:             "3508000000",
		Password:              "",
		LocalSIPPort:          15091,
		Name:                  "yangm",
		Manufacturer:          "Unknown",
		Model:                 "Unknown",
		Transport:             "udp",
		RegisterIntervalSecs:  3600,
		HeartbeatIntervalSecs: 60,
		VideoBackend:          "synthetic",
	}
}

// Path 返回配置文件路径：优先可执行文件所在目录；若是 go run 产生的临时目录则改用当前工作目录。
func Path() string {
	return filepath.Join(Dir(), FileName)
}

// Dir 返回配置文件所在目录。
func Dir() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		base := filepath.Base(dir)
		if strings.Contains(dir, string(filepath.Separator)+"go-build") || strings.HasPrefix(base, "go-build") {
			if cwd, err := os.Getwd(); err == nil {
				return cwd
			}
		}
		return dir
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// Load 读取配置。文件不存在时返回 Defaults（密码为空）。已有文件会在缺项处补默认值。
func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Defaults(), fmt.Errorf("解析配置 %s: %w", path, err)
	}
	cfg.normalize()
	return cfg, nil
}

// Save 原子写入配置（含密码，仅本机文件）。
func Save(path string, cfg Config) error {
	cfg.normalize()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "gb28181-device-ui-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入配置: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		// Windows 上 chmod 可能无意义，忽略即可
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存配置 %s: %w", path, err)
	}
	ok = true
	return nil
}

func (c *Config) normalize() {
	d := Defaults()
	if strings.TrimSpace(c.PlatformSIPAddress) == "" {
		c.PlatformSIPAddress = d.PlatformSIPAddress
	}
	if c.PlatformSIPPort <= 0 {
		c.PlatformSIPPort = d.PlatformSIPPort
	}
	if strings.TrimSpace(c.DeviceID) == "" {
		c.DeviceID = d.DeviceID
	}
	if strings.TrimSpace(c.ChannelID) == "" {
		c.ChannelID = d.ChannelID
	}
	if strings.TrimSpace(c.SIPDomain) == "" {
		c.SIPDomain = d.SIPDomain
	}
	if c.LocalSIPPort <= 0 {
		c.LocalSIPPort = d.LocalSIPPort
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = d.Name
	}
	if strings.TrimSpace(c.Manufacturer) == "" {
		c.Manufacturer = d.Manufacturer
	}
	if strings.TrimSpace(c.Model) == "" {
		c.Model = d.Model
	}
	t := strings.ToLower(strings.TrimSpace(c.Transport))
	if t == "" {
		t = d.Transport
	}
	c.Transport = t
	if c.RegisterIntervalSecs <= 0 {
		c.RegisterIntervalSecs = d.RegisterIntervalSecs
	}
	if c.HeartbeatIntervalSecs <= 0 {
		c.HeartbeatIntervalSecs = d.HeartbeatIntervalSecs
	}
	c.VideoBackend = strings.ToLower(strings.TrimSpace(c.VideoBackend))
	if c.VideoBackend == "" {
		c.VideoBackend = d.VideoBackend
	}
	c.CameraID = strings.TrimSpace(c.CameraID)
	c.CameraName = strings.TrimSpace(c.CameraName)
}
