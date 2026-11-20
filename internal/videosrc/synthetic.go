package videosrc

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"
)

const (
	BackendSynthetic  = "synthetic"
	syntheticDeviceID = "placeholder"
)

func init() {
	Register(Synthetic{})
}

// Synthetic 写入库内合成的占位 H.264（约 1 fps，每 5 帧一个 IDR）。
type Synthetic struct{}

func (Synthetic) ID() string       { return BackendSynthetic }
func (Synthetic) Label() string    { return "合成占位画面" }
func (Synthetic) Available() error { return nil }

func (Synthetic) Devices(context.Context) ([]Device, error) {
	return []Device{{ID: syntheticDeviceID, Name: "合成占位 H.264（每秒一帧）"}}, nil
}

func (Synthetic) Open(ctx context.Context, _ Device, _ Options, dest Sink) (Session, error) {
	if dest == nil {
		return nil, errNilSink
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &basicSession{cancel: cancel, done: make(chan error, 1)}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var err error
		defer func() { s.finish(err) }()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				err = nil
				return
			case <-ticker.C:
				dest.Write(syntheticAU(i))
			}
		}
	}()
	return s, nil
}

func syntheticAU(i int) device.AccessUnit {
	au := device.AccessUnit{
		Timestamp: time.Now(),
		KeyFrame:  i%5 == 0,
	}
	if au.KeyFrame {
		au.NALUs = []device.NALU{
			{Type: 7, IsSPS: true, Data: []byte{0x67, 0x42}},
			{Type: 8, IsPPS: true, Data: []byte{0x68, 0xEB}},
			{Type: 5, IsIDR: true, Data: []byte{0x65, 0x01}},
		}
	} else {
		au.NALUs = []device.NALU{{Type: 1, Data: []byte{0x41, 0x02}}}
	}
	return au
}

// basicSession 用于不需要杀子进程的后端（如 synthetic）。
type basicSession struct {
	cancel context.CancelFunc
	done   chan error
	closed atomic.Bool
	once   atomic.Bool
}

func (s *basicSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *basicSession) Wait() error {
	err, ok := <-s.done
	if !ok {
		return nil
	}
	return err
}

func (s *basicSession) finish(err error) {
	if s.once.Swap(true) {
		return
	}
	s.done <- err
	close(s.done)
}
