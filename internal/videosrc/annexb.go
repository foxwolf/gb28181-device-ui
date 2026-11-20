package videosrc

import (
	"bytes"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"
)

// AnnexBSplitter 把 Annex-B 字节流切成 H.264 访问单元。
// NALU.Data 不含起始码；SPS/PPS 会缓存并在后续 IDR 缺少它们时补上。
type AnnexBSplitter struct {
	buf    []byte
	au     []device.NALU
	hasVCL bool
	sps    *device.NALU
	pps    *device.NALU
}

// Push 追加数据并返回已经完整的访问单元（不含尚未结束的当前 AU）。
func (s *AnnexBSplitter) Push(chunk []byte) []device.AccessUnit {
	if len(chunk) > 0 {
		s.buf = append(s.buf, chunk...)
	}
	var out []device.AccessUnit
	for {
		sc1, n1 := findStartCode(s.buf)
		if sc1 < 0 {
			if len(s.buf) > 3 {
				s.buf = append([]byte(nil), s.buf[len(s.buf)-3:]...)
			}
			break
		}
		rest := s.buf[sc1+n1:]
		sc2, _ := findStartCode(rest)
		if sc2 < 0 {
			s.buf = append([]byte(nil), s.buf[sc1:]...)
			break
		}
		if n := naluFrom(rest[:sc2]); len(n.Data) > 0 {
			out = append(out, s.feed(n)...)
		}
		s.buf = rest[sc2:]
	}
	return out
}

// Flush 在流结束时取出缓冲区里最后一组 NALU，并结束当前访问单元。
func (s *AnnexBSplitter) Flush() []device.AccessUnit {
	var out []device.AccessUnit
	sc, n := findStartCode(s.buf)
	if sc >= 0 && sc+n < len(s.buf) {
		if nal := naluFrom(s.buf[sc+n:]); len(nal.Data) > 0 {
			out = append(out, s.feed(nal)...)
		}
	}
	s.buf = nil
	if au := s.popAU(true); au != nil {
		out = append(out, *au)
	}
	return out
}

func (s *AnnexBSplitter) feed(n device.NALU) []device.AccessUnit {
	if n.IsSPS {
		cp := n
		s.sps = &cp
	}
	if n.IsPPS {
		cp := n
		s.pps = &cp
	}
	startNew := false
	if isAUD(n.Type) {
		startNew = true
	} else if isVCL(n.Type) && s.hasVCL {
		startNew = true
	}
	var out []device.AccessUnit
	if startNew {
		if au := s.popAU(false); au != nil {
			out = append(out, *au)
		}
	}
	s.au = append(s.au, n)
	if isVCL(n.Type) {
		s.hasVCL = true
	}
	return out
}

// popAU 取出当前 AU。force 为 false 时，只有已经包含 VCL 的 AU 才会被取出
// （SPS/PPS/SEI 会留给随后的 VCL）。
func (s *AnnexBSplitter) popAU(force bool) *device.AccessUnit {
	if len(s.au) == 0 {
		return nil
	}
	if !s.hasVCL && !force {
		return nil
	}
	if !s.hasVCL {
		s.au = nil
		return nil
	}
	au := decorateAU(s.au, s.sps, s.pps, time.Now())
	s.au = nil
	s.hasVCL = false
	return &au
}

func decorateAU(nalus []device.NALU, sps, pps *device.NALU, ts time.Time) device.AccessUnit {
	key, hasSPS, hasPPS := false, false, false
	for _, n := range nalus {
		if n.IsIDR {
			key = true
		}
		if n.IsSPS {
			hasSPS = true
		}
		if n.IsPPS {
			hasPPS = true
		}
	}
	if key {
		var prefix []device.NALU
		if !hasSPS && sps != nil {
			prefix = append(prefix, *sps)
		}
		if !hasPPS && pps != nil {
			prefix = append(prefix, *pps)
		}
		if len(prefix) > 0 {
			nalus = append(prefix, nalus...)
		}
	}
	return device.AccessUnit{NALUs: nalus, Timestamp: ts, KeyFrame: key}
}

func naluFrom(payload []byte) device.NALU {
	// 起始码后可能残留的 0 填充不是 NALU。
	payload = bytes.TrimRight(payload, "\x00")
	if len(payload) == 0 {
		return device.NALU{}
	}
	typ := payload[0] & 0x1F
	return device.NALU{
		Type:  typ,
		Data:  bytes.Clone(payload),
		IsIDR: typ == 5,
		IsSPS: typ == 7,
		IsPPS: typ == 8,
	}
}

func isVCL(t byte) bool { return t >= 1 && t <= 5 }
func isAUD(t byte) bool { return t == 9 }

// findStartCode 返回起始码的起始下标和长度（3 或 4）。找不到时 idx 为 -1。
func findStartCode(b []byte) (idx, length int) {
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 {
			continue
		}
		if i+3 < len(b) && b[i+2] == 0 && b[i+3] == 1 {
			return i, 4
		}
		if b[i+2] == 1 {
			return i, 3
		}
	}
	return -1, 0
}
