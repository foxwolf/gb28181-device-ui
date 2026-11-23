package videosrc

import (
	"bytes"
	"testing"

	"github.com/mickeyzzc/gb28181-go/device"
)

func sc3(payload []byte) []byte {
	return append([]byte{0, 0, 1}, payload...)
}

func sc4(payload []byte) []byte {
	return append([]byte{0, 0, 0, 1}, payload...)
}

func TestAnnexB_SPS_PPS_IDRThenP(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xC0, 0x1E}
	pps := []byte{0x68, 0xEB, 0x80}
	idr := []byte{0x65, 0x88, 0x01}
	p := []byte{0x41, 0x9A, 0x02}

	var s AnnexBSplitter
	stream := bytes.Join([][]byte{sc4(sps), sc4(pps), sc4(idr), sc3(p)}, nil)
	aus := s.Push(stream)
	aus = append(aus, s.Flush()...)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if !aus[0].KeyFrame || len(aus[0].NALUs) != 3 {
		t.Fatalf("first AU = %+v", dumpAU(aus[0]))
	}
	if !aus[0].NALUs[0].IsSPS || !aus[0].NALUs[1].IsPPS || !aus[0].NALUs[2].IsIDR {
		t.Fatalf("first AU types = %+v", dumpAU(aus[0]))
	}
	if !bytes.Equal(aus[0].NALUs[0].Data, sps) || aus[0].NALUs[0].Type != 7 {
		t.Fatalf("SPS mismatch: %+v", aus[0].NALUs[0])
	}
	if aus[1].KeyFrame || len(aus[1].NALUs) != 1 || aus[1].NALUs[0].Type != 1 {
		t.Fatalf("second AU = %+v", dumpAU(aus[1]))
	}
	if aus[0].Timestamp.IsZero() || aus[1].Timestamp.IsZero() {
		t.Fatal("timestamps should be set")
	}
}

func TestAnnexB_MixedStartCodesAndPartialPushes(t *testing.T) {
	sps := []byte{0x67, 0x42}
	pps := []byte{0x68, 0xEB}
	idr := []byte{0x65, 0x01, 0x02, 0x03, 0x04}
	stream := append(sc3(sps), append(sc4(pps), sc4(idr)...)...)

	var s AnnexBSplitter
	var aus []device.AccessUnit
	// 切成很小的块，确保起始码和 NALU 被拆开时仍能还原。
	for i := 0; i < len(stream); i++ {
		aus = append(aus, s.Push(stream[i:i+1])...)
	}
	aus = append(aus, s.Flush()...)
	if len(aus) != 1 {
		t.Fatalf("got %d AUs, want 1; %#v", len(aus), aus)
	}
	if !aus[0].KeyFrame || len(aus[0].NALUs) != 3 {
		t.Fatalf("AU = %+v", dumpAU(aus[0]))
	}
}

func TestAnnexB_AUDStartsNewAU(t *testing.T) {
	idr := []byte{0x65, 0x01}
	p := []byte{0x41, 0x02}
	aud := []byte{0x09, 0xF0}
	stream := bytes.Join([][]byte{sc4(aud), sc4(idr), sc4(aud), sc4(p)}, nil)

	var s AnnexBSplitter
	aus := append(s.Push(stream), s.Flush()...)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2: %v", len(aus), dumpAUs(aus))
	}
	if !aus[0].KeyFrame || aus[1].KeyFrame {
		t.Fatalf("keyframe flags: %v %v", aus[0].KeyFrame, aus[1].KeyFrame)
	}
}

func TestAnnexB_SEIAccumulatesWithVCL(t *testing.T) {
	sps := []byte{0x67, 0x42}
	pps := []byte{0x68, 0xEB}
	sei := []byte{0x06, 0x05, 0x01}
	idr := []byte{0x65, 0x01}
	stream := bytes.Join([][]byte{sc4(sps), sc4(pps), sc4(sei), sc4(idr)}, nil)

	var s AnnexBSplitter
	aus := append(s.Push(stream), s.Flush()...)
	if len(aus) != 1 {
		t.Fatalf("got %d AUs, want 1", len(aus))
	}
	if len(aus[0].NALUs) != 4 {
		t.Fatalf("want SPS+PPS+SEI+IDR, got %v", dumpAU(aus[0]))
	}
	if aus[0].NALUs[2].Type != 6 || !aus[0].NALUs[3].IsIDR {
		t.Fatalf("unexpected types: %v", dumpAU(aus[0]))
	}
}

func TestAnnexB_IDRGetsCachedParameterSets(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xC0}
	pps := []byte{0x68, 0xEB}
	idr1 := []byte{0x65, 0x01}
	idr2 := []byte{0x65, 0x02}
	// 第一帧带 SPS/PPS，第二帧只有 IDR，应补上缓存的参数集。
	stream := bytes.Join([][]byte{sc4(sps), sc4(pps), sc4(idr1), sc4(idr2)}, nil)

	var s AnnexBSplitter
	aus := append(s.Push(stream), s.Flush()...)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if len(aus[1].NALUs) != 3 || !aus[1].NALUs[0].IsSPS || !aus[1].NALUs[1].IsPPS || !aus[1].NALUs[2].IsIDR {
		t.Fatalf("second AU should include cached SPS/PPS: %v", dumpAU(aus[1]))
	}
	if !bytes.Equal(aus[1].NALUs[0].Data, sps) || !bytes.Equal(aus[1].NALUs[1].Data, pps) {
		t.Fatalf("cached parameter sets mismatch: %v", dumpAU(aus[1]))
	}
	if !aus[1].KeyFrame {
		t.Fatal("second AU should be a keyframe")
	}
}

func TestFindStartCode(t *testing.T) {
	idx, n := findStartCode([]byte{1, 2, 0, 0, 0, 1, 9})
	if idx != 2 || n != 4 {
		t.Fatalf("4-byte: idx=%d n=%d", idx, n)
	}
	idx, n = findStartCode([]byte{0, 0, 1, 9})
	if idx != 0 || n != 3 {
		t.Fatalf("3-byte: idx=%d n=%d", idx, n)
	}
	idx, n = findStartCode([]byte{0, 0})
	if idx != -1 {
		t.Fatalf("incomplete: idx=%d", idx)
	}
}

func dumpAU(au device.AccessUnit) []byte {
	var types []byte
	for _, n := range au.NALUs {
		types = append(types, n.Type)
	}
	return types
}

func dumpAUs(aus []device.AccessUnit) [][]byte {
	var out [][]byte
	for _, au := range aus {
		out = append(out, dumpAU(au))
	}
	return out
}

func encodeUE(v uint32) []byte {
	n := v + 1
	zeros := 0
	for (uint32(1) << (zeros + 1)) <= n {
		zeros++
	}
	total := zeros*2 + 1
	out := make([]byte, (total+7)/8)
	bit := 0
	put := func(b uint32) {
		if b != 0 {
			out[bit/8] |= 1 << (7 - (bit % 8))
		}
		bit++
	}
	for i := 0; i < zeros; i++ {
		put(0)
	}
	put(1)
	info := n - (1 << zeros)
	for i := zeros - 1; i >= 0; i-- {
		put((info >> i) & 1)
	}
	return out
}

func codedSlice(nalType byte, firstMB uint32) []byte {
	hdr := []byte{(1 << 5) | (nalType & 0x1F)}
	return append(hdr, encodeUE(firstMB)...)
}

func TestEncodeUE(t *testing.T) {
	cases := []uint32{0, 1, 2, 319, 320, 3280, 3600}
	for _, v := range cases {
		got, ok := decodeUE(encodeUE(v))
		if !ok || got != v {
			t.Fatalf("ue(%d) roundtrip got %d ok=%v bytes=%x", v, got, ok, encodeUE(v))
		}
	}
}

func TestFirstMbInSlice(t *testing.T) {
	n := codedSlice(1, 0)
	got, ok := firstMbInSlice(n)
	if !ok || got != 0 {
		t.Fatalf("first_mb=0: got %d ok=%v data=%x", got, ok, n)
	}
	n = codedSlice(5, 320)
	got, ok = firstMbInSlice(n)
	if !ok || got != 320 {
		t.Fatalf("first_mb=320: got %d ok=%v data=%x", got, ok, n)
	}
	if _, ok := firstMbInSlice([]byte{0x67, 0x42}); ok {
		t.Fatal("SPS should not parse as a slice")
	}
}

func TestAnnexB_MultiSliceSamePicture(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xC0, 0x1E}
	pps := []byte{0x68, 0xEB, 0x80}
	idr0 := codedSlice(5, 0)
	idr1 := codedSlice(5, 320)
	p0 := codedSlice(1, 0)
	p1 := codedSlice(1, 320)

	var s AnnexBSplitter
	stream := bytes.Join([][]byte{sc4(sps), sc4(pps), sc4(idr0), sc4(idr1), sc3(p0), sc3(p1)}, nil)
	aus := append(s.Push(stream), s.Flush()...)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2 (one IDR picture, one P picture): %v", len(aus), dumpAUs(aus))
	}
	if !aus[0].KeyFrame || len(aus[0].NALUs) != 4 {
		t.Fatalf("IDR AU want SPS+PPS+2 slices, got %v key=%v", dumpAU(aus[0]), aus[0].KeyFrame)
	}
	if aus[0].NALUs[2].Type != 5 || aus[0].NALUs[3].Type != 5 {
		t.Fatalf("IDR slices types: %v", dumpAU(aus[0]))
	}
	if aus[1].KeyFrame || len(aus[1].NALUs) != 2 || aus[1].NALUs[0].Type != 1 || aus[1].NALUs[1].Type != 1 {
		t.Fatalf("P AU want 2 non-IDR slices, got %v key=%v", dumpAU(aus[1]), aus[1].KeyFrame)
	}
}

func TestAnnexB_NewPictureStartsAtFirstMbZero(t *testing.T) {
	p0 := codedSlice(1, 0)
	p1 := codedSlice(1, 320)
	p2 := codedSlice(1, 0)
	var s AnnexBSplitter
	stream := bytes.Join([][]byte{sc4(p0), sc4(p1), sc4(p2)}, nil)
	aus := append(s.Push(stream), s.Flush()...)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2: %v", len(aus), dumpAUs(aus))
	}
	if len(aus[0].NALUs) != 2 || len(aus[1].NALUs) != 1 {
		t.Fatalf("slice grouping: %v", dumpAUs(aus))
	}
}
