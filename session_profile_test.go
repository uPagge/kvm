package kvm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jetkvm/kvm/internal/hidrpc"
	"github.com/jetkvm/kvm/internal/usbgadget"
	"github.com/pion/webrtc/v4"
)

type releasedInput struct {
	reports [][]byte
	fail    error
}

func (r *releasedInput) KeyboardReport(modifier byte, keys []byte) error {
	r.reports = append(r.reports, append([]byte{modifier, 0}, keys...))
	return r.fail
}
func (r *releasedInput) RelMouseReport(dx, dy int8, buttons byte) error {
	r.reports = append(r.reports, []byte{buttons, byte(dx), byte(dy), 0})
	return nil
}

func TestSessionReleaseClearsKeyboardAndMouseEvenOnKeyboardFailure(t *testing.T) {
	r := &releasedInput{fail: errors.New("keyboard write failed")}
	err := releaseHIDInputs(&usbgadget.Devices{Keyboard: true, RelativeMouse: true}, r)
	if !errors.Is(err, r.fail) {
		t.Fatalf("release error lost: %v", err)
	}
	if !reflect.DeepEqual(r.reports, [][]byte{{0, 0, 0, 0, 0, 0, 0, 0}, {0, 0, 0, 0}}) {
		t.Fatalf("release reports %v", r.reports)
	}
}

func TestClosedSessionDiscardsAlreadyQueuedKeepAlive(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	s.close()
	// The queue worker can take a packet just before the session closes. This
	// reproduces its already-dequeued callback without scheduling or hardware.
	onHidMessage(hidQueueMessage{DataChannelMessage: webrtc.DataChannelMessage{Data: []byte{byte(hidrpc.TypeKeypressKeepAliveReport)}}}, s)
	if !s.lastTimerResetTime.IsZero() || !s.lastKeepAliveArrivalTime.IsZero() {
		t.Fatal("closed session revived input keepalive")
	}
}

func TestClosedSessionMacroCannotCancelReplacement(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	s.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setKeyboardMacroCancel(cancel)
	t.Cleanup(func() { setKeyboardMacroCancel(nil) })
	if err := executeKeyboardMacroForSession([]hidrpc.KeyboardMacroStep{{Keys: []byte{4}}}, s); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed macro admission: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("closed session canceled replacement macro")
	}
}

func TestClosedSessionJSONCannotMutateConfiguration(t *testing.T) {
	old := config
	config = &Config{KeyboardLayout: "en-US"}
	t.Cleanup(func() { config = old })
	s := &Session{done: make(chan struct{})}
	s.close()
	onRPCMessage(webrtc.DataChannelMessage{Data: []byte(`{"jsonrpc":"2.0","method":"setKeyboardLayout","params":{"layout":"fr-FR"},"id":1}`)}, s)
	if config.KeyboardLayout != "en-US" {
		t.Fatal("closed session mutated configuration")
	}
}

func TestStandardMediaRejectedBeforePreparingMount(t *testing.T) {
	withProfileConfig(t, usbgadget.Devices{Profile: "standard-relative"})
	if state, err := getInitialVirtualMediaState(); err != nil || state != nil {
		t.Fatalf("disabled storage probed hardware: %+v %v", state, err)
	}
	if err := rpcMountBuiltInImage("missing.iso"); err == nil {
		t.Fatal("storage accepted a mount")
	}
}
