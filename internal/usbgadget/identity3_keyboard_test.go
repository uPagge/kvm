package usbgadget

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fixture is the owner's measured MK275 59-byte keyboard descriptor from
// jetkvm-brief/evidence/usb-comparison/host/comparison-receipt.json, genuine_reports.
// Its 8-byte input layout is tested against actual writer output below.
func TestIdentity3MeasuredKeyboardSelectedAndDecoded(t *testing.T) {
	raw, err := os.ReadFile("testdata/identity3-mk275-keyboard.hex")
	if err != nil {
		t.Fatal(err)
	}
	measured, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(measured)) != "0b34d8e4baacc7f90438d40a29e6221f5b034238677957bb145c4f337161a809" {
		t.Fatal("measured fixture integrity failed")
	}
	u := standardTestGadget(t)
	if !bytes.Equal(u.configMap["keyboard"].reportDesc, measured) {
		t.Fatalf("selected keyboard descriptor is not measured59 (got %d bytes)", len(u.configMap["keyboard"].reportDesc))
	}
	fields, inputBits, outputBits := decodeHIDTestFields(t, u.configMap["keyboard"].reportDesc)
	want := []hidTestField{{7, 0xe0, 0xe7, 1, 8, 0, 1, 2}, {7, 0, 0, 1, 8, 0, 1, 3}, {7, 0, 0xa4, 8, 6, 0, 0xa4, 0}}
	if inputBits != 64 || outputBits != 8 || !reflect.DeepEqual(fields, want) {
		t.Fatalf("keyboard layout = %+v input/output=%d/%d", fields, inputBits, outputBits)
	}
	legacy := defaultUsbGadgetDevices
	if err = u.SetGadgetDevices(&legacy); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(u.configMap["keyboard"].reportDesc)); got != "f3e62a32638f14efff361d5f49be6a23cce36f13ff900522aedd41935b3c3721" {
		t.Fatalf("legacy descriptor = %s", got)
	}
}

func TestIdentity3KeyboardReportBoundsBeforeWriteAndState(t *testing.T) {
	for _, keys := range [][]byte{{0xa5}, {0xff}, {0xe0}, {4, 5, 6, 7, 8, 9, 0xff}} {
		t.Run(fmt.Sprintf("%x", keys), func(t *testing.T) {
			u := standardTestGadget(t)
			r, w := reportPipe(t)
			u.keyboardHidFile = w
			if err := u.KeyboardReport(2, []byte{0xa4}); err != nil {
				t.Fatal(err)
			}
			readReport(t, r, []byte{2, 0, 0xa4, 0, 0, 0, 0, 0})
			before := u.GetKeysDownState()
			if err := u.KeyboardReport(0, keys); err == nil {
				t.Error("unsupported key-array accepted")
			}
			if got := u.GetKeysDownState(); !reflect.DeepEqual(got, before) {
				t.Errorf("rejection changed held state: %+v", got)
			}
			identity3NoReport(t, r)
		})
	}
}

func TestIdentity3KeypressRejectsUnsupportedPressAndRelease(t *testing.T) {
	for _, key := range []byte{0xa5, 0xff} {
		for _, press := range []bool{true, false} {
			t.Run(fmt.Sprintf("%x/%t", key, press), func(t *testing.T) {
				u := standardTestGadget(t)
				r, w := reportPipe(t)
				u.keyboardHidFile = w
				if err := u.KeyboardReport(0, []byte{4}); err != nil {
					t.Fatal(err)
				}
				readReport(t, r, []byte{0, 0, 4, 0, 0, 0, 0, 0})
				before := u.GetKeysDownState()
				if err := u.KeypressReport(key, press); err == nil {
					t.Error("unsupported keypress accepted")
				}
				if got := u.GetKeysDownState(); !reflect.DeepEqual(got, before) {
					t.Errorf("rejection changed held state: %+v", got)
				}
				if len(u.kbdAutoReleaseTimers) != 0 {
					t.Error("rejection installed auto-release timer")
				}
				identity3NoReport(t, r)
			})
		}
	}
}

func TestIdentity3KeyboardAcceptedReportReleaseAndModifier(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.keyboardHidFile = w
	for _, action := range []struct {
		run  func() error
		want []byte
	}{
		{func() error { return u.KeyboardReport(0xff, []byte{0xa4, 4, 5, 6, 7, 8}) }, []byte{255, 0, 164, 4, 5, 6, 7, 8}},
		{func() error { return u.KeyboardReport(0, nil) }, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
		{func() error { return u.KeypressReport(0xe0, true) }, []byte{1, 0, 0, 0, 0, 0, 0, 0}},
		{func() error { return u.KeypressReport(0xe0, false) }, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
		{func() error { return u.KeypressReport(0xa4, true) }, []byte{0, 0, 164, 0, 0, 0, 0, 0}},
		{func() error { return u.KeypressReport(0xa4, false) }, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
	} {
		if err := action.run(); err != nil {
			t.Fatal(err)
		}
		readReport(t, r, action.want)
	}
	if got := u.GetKeysDownState(); got.Modifier != 0 || !bytes.Equal(got.Keys, make([]byte, 6)) {
		t.Fatalf("release retained state %+v", got)
	}
	u.updateKeyboardState(0x1f)
	state := u.GetKeyboardState()
	if got := state.Byte(); got != 0x1f {
		t.Fatalf("LED5 = %x", got)
	}
	u.updateKeyboardState(0x40)
	state = u.GetKeyboardState()
	if got := state.Byte(); got != 0x1f {
		t.Fatal("undeclared LED changed state")
	}
}

func TestIdentity3KeyboardRolloverAndLegacyFF(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.keyboardHidFile = w
	if err := u.KeyboardReport(0, []byte{4, 5, 6, 7, 8, 9}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0, 0, 4, 5, 6, 7, 8, 9})
	if err := u.KeypressReport(10, true); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0, 0, 1, 1, 1, 1, 1, 1})
	if err := u.KeyboardReport(0, nil); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, make([]byte, 8))
	// Switch through the real profile setter; attach a fresh pipe after reset closes the old one.
	legacy := defaultUsbGadgetDevices
	if err := u.SetGadgetDevices(&legacy); err != nil {
		t.Fatal(err)
	}
	r, w = reportPipe(t)
	u.keyboardHidFile = w
	if err := u.KeyboardReport(0, []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0, 0, 255, 0, 0, 0, 0, 0})
	if err := u.KeypressReport(0xff, false); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, make([]byte, 8))
}

func identity3NoReport(t *testing.T, r *os.File) {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [8]byte
	n, err := r.Read(b[:])
	if n != 0 || !os.IsTimeout(err) {
		t.Errorf("rejected operation wrote %d bytes %x error=%v", n, b[:n], err)
	}
}
