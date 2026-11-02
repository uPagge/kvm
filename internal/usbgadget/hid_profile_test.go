package usbgadget

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func standardDevices(t *testing.T) Devices {
	t.Helper()
	var devices Devices
	if err := json.Unmarshal([]byte(`{"hid_profile":"standard-relative","keyboard":true,"relative_mouse":true,"absolute_mouse":true,"mass_storage":true,"serial_console":true,"audio":false}`), &devices); err != nil {
		t.Fatal(err)
	}
	return devices
}

func standardTestGadget(t *testing.T) *UsbGadget {
	t.Helper()
	u := newTestGadgetWithKeyboard(nil)
	u.configMap = deepCopyConfigMap(defaultGadgetConfig)
	u.kvmGadgetPath = t.TempDir()
	u.keysDownState = KeysDownState{Keys: make([]byte, 6)}
	u.kbdAutoReleaseTimers = make(map[byte]*time.Timer)
	devices := standardDevices(t)
	u.SetGadgetDevices(&devices)
	for function, dev := range map[string]string{"hid.usb0": "240:4", "hid.usb2": "240:7"} {
		dir := filepath.Join(u.kvmGadgetPath, "functions", function)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dev"), []byte(dev+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(u.ResetHIDFiles)
	return u
}

func reportPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

func readReport(t *testing.T, r *os.File, want []byte) {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("report = %v, want %v", got, want)
	}
}

// Catches selecting functions from stale per-device booleans instead of the profile.
func TestStandardProfileNormalizesComposition(t *testing.T) {
	u := standardTestGadget(t)
	var enabled []string
	for _, item := range u.getOrderedConfigItems() {
		if item.item.device != "" && u.isGadgetConfigItemEnabled(item.key) {
			enabled = append(enabled, item.key)
		}
	}
	if !reflect.DeepEqual(enabled, []string{"keyboard", "relative_mouse"}) {
		t.Fatalf("enabled functions = %v, want keyboard + relative_mouse", enabled)
	}
	for _, key := range []string{"keyboard", "relative_mouse"} {
		attrs := u.configMap[key].attrs
		wantProtocol := "1"
		if key == "relative_mouse" {
			wantProtocol = "2"
		}
		if attrs["protocol"] != wantProtocol || attrs["subclass"] != "1" {
			t.Errorf("%s must declare its implemented boot protocol: %v", key, attrs)
		}
	}
	if got := u.configMap["relative_mouse"].attrs["report_length"]; got != "4" {
		t.Errorf("mouse report_length = %s, want 4", got)
	}
	if got := u.configMap["base"].configAttrs["bmAttributes"]; got != "0x80" {
		t.Errorf("bus attributes = %s, want bus powered without remote wake", got)
	}
	legacy := defaultUsbGadgetDevices
	u.SetGadgetDevices(&legacy)
	if !u.isGadgetConfigItemEnabled("wake_hid") || u.configMap["relative_mouse"].attrs["report_length"] != "5" {
		t.Fatal("returning to empty profile did not restore legacy composition")
	}
}

func TestStandardConstructorNormalizesOldDeviceSwitches(t *testing.T) {
	// Missing keyboard/relative booleans in a persisted config must not disable
	// the only supported functions, even before a setter is called.
	var devices Devices
	if err := json.Unmarshal([]byte(`{"hid_profile":"standard-relative","audio":true}`), &devices); err != nil {
		t.Fatal(err)
	}
	u := NewUsbGadget("standard-constructor-test", &devices, nil, nil)
	if u == nil {
		t.Fatal("constructor failed")
	}
	t.Cleanup(func() { _ = u.Close() })
	if got := u.GetGadgetDevices(); !got.Keyboard || !got.RelativeMouse || !got.Audio || got.AbsoluteMouse || got.MassStorage || got.SerialConsole {
		t.Fatalf("constructor composition = %+v", got)
	}
	if u.configMap["relative_mouse"].attrs["report_length"] != "4" || u.configMap["keyboard"].attrs["subclass"] != "1" {
		t.Fatal("constructor descriptors did not select standard profile")
	}
}

// Catches treating symlink/interface order as the allocated hidg minor, or scrolling with buttons=0.
func TestStandardRelativeRoutesAllocatedMinorAndPreservesDrag(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		if name != "/dev/hidg7" {
			t.Errorf("mouse opened %s, want actual allocated minor /dev/hidg7", name)
		}
		return w, nil
	}
	for _, action := range []struct {
		run  func() error
		want []byte
	}{
		{func() error { return u.RelMouseReport(10, -5, 1) }, []byte{1, 10, 251, 0}},
		{func() error { return u.RelMouseWheelReport(1, 0) }, []byte{1, 0, 0, 1}},
		{func() error { return u.RelMouseReport(-127, 127, 7) }, []byte{7, 129, 127, 0}},
		{func() error { return u.RelMouseReport(0, 0, 0) }, []byte{0, 0, 0, 0}},
		{func() error { return u.RelMouseWheelReport(-127, 0) }, []byte{0, 0, 0, 129}},
	} {
		if err := action.run(); err != nil {
			t.Fatal(err)
		}
		readReport(t, r, action.want)
	}
}

// Catches accepting values outside the descriptor or silently changing the held buttons on rejection.
func TestStandardRelativeRejectsInvalidInputsWithoutWriting(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	if err := u.RelMouseReport(0, 0, 1); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{1, 0, 0, 0})
	for name, run := range map[string]func() error{
		"x -128":           func() error { return u.RelMouseReport(-128, 0, 0) },
		"y -128":           func() error { return u.RelMouseReport(0, -128, 0) },
		"button 4":         func() error { return u.RelMouseReport(0, 0, 8) },
		"wheel -128":       func() error { return u.RelMouseWheelReport(-128, 0) },
		"horizontal wheel": func() error { return u.RelMouseWheelReport(0, 1) },
	} {
		if err := run(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := u.RelMouseWheelReport(127, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{1, 0, 0, 127})
	if err := r.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(make([]byte, 32)); n != 0 || !os.IsTimeout(err) {
		t.Fatalf("invalid input wrote extra bytes: n=%d err=%v", n, err)
	}
}

// Catches pretending unavailable absolute/wake functions succeeded and opening removed functions.
func TestStandardProfileDisabledOperationsFailWithoutOpening(t *testing.T) {
	u := standardTestGadget(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		t.Errorf("disabled function opened %s", name)
		return nil, os.ErrNotExist
	}
	for name, run := range map[string]func() error{
		"wake":            func() error { return u.WakeReport(true) },
		"absolute motion": func() error { return u.AbsMouseReport(1, 1, 1) },
		"absolute wheel":  func() error { return u.AbsMouseWheelReport(1, 0) },
	} {
		if err := run(); err == nil {
			t.Errorf("%s returned success although absent from profile", name)
		}
	}
}

// Catches swallowing a timeout/short write, committing unsent buttons, or losing last successful buttons on reopen.
func TestStandardRelativeFailedWriteDoesNotCommitState(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	if err := u.RelMouseReport(0, 0, 1); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{1, 0, 0, 0})
	fillPipeBuffer(t, w)
	if err := u.RelMouseReport(0, 0, 2); err == nil {
		t.Fatal("timed out report returned success")
	}
	nextR, nextW := reportPipe(t)
	u.hidOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nextW, nil }
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, nextR, []byte{1, 0, 0, 1})
}

// Catches inheriting stale held buttons across reset/adoption and accepting non-LED bit 6.
func TestStandardResetAndAdoptionReleaseHeldInputs(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	if err := u.RelMouseReport(0, 0, 1); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{1, 0, 0, 0})
	u.ResetHIDFiles()
	nextR, nextW := reportPipe(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		if name == "/dev/hidg7" {
			return nextW, nil
		}
		return nil, os.ErrNotExist
	}
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, nextR, []byte{0, 0, 0, 1})
	if err := u.RelMouseReport(0, 0, 7); err != nil {
		t.Fatal(err)
	}
	readReport(t, nextR, []byte{7, 0, 0, 0})
	u.adoptLiveGadget()
	readReport(t, nextR, []byte{0, 0, 0, 0})
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, nextR, []byte{0, 0, 0, 1})
	u.updateKeyboardState(0x40)
	if got := u.GetKeyboardState(); got.Shift || got.Byte() != 0 {
		t.Fatalf("undeclared LED accepted: %+v", got)
	}
}

func TestStandardKeyboardRoutesAllocatedMinorAndKeepsFailedState(t *testing.T) {
	u := standardTestGadget(t)
	r, w := reportPipe(t)
	// Pre-open only for writing; the independent opener test below verifies routing.
	u.keyboardHidFile = w
	if err := u.KeyboardReport(2, []byte{4}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{2, 0, 4, 0, 0, 0, 0, 0})
	fillPipeBuffer(t, w)
	if err := u.KeyboardReport(0, nil); err == nil {
		t.Fatal("failed keyboard release returned success")
	}
	state := u.GetKeysDownState()
	if state.Modifier != 2 || !bytes.Equal(state.Keys, []byte{4, 0, 0, 0, 0, 0}) {
		t.Fatalf("failed report committed keyboard state: %+v", state)
	}
	file := hidTestSocket(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		if name != "/dev/hidg4" {
			t.Errorf("keyboard opened %s, want /dev/hidg4", name)
		}
		return file, nil
	}
	if err := u.OpenKeyboardHidFile(); err != nil {
		t.Fatal(err)
	}
}

// Independently decode HID short items: catches descriptors declaring extra buttons,
// horizontal scrolling, report IDs or absolute axes even when report_length is four.
func TestStandardDescriptorsMatchEmittedReports(t *testing.T) {
	u := standardTestGadget(t)
	for _, tc := range []struct {
		key        string
		inputBits  int
		outputBits int
		wantInputs []hidTestField
	}{
		{"relative_mouse", 32, 0, []hidTestField{
			{9, 1, 3, 1, 3, 0, 1, 2},
			{9, 0, 0, 5, 1, 0, 1, 3},
			{1, 0x30, 0x38, 8, 3, -127, 127, 6},
		}},
		{"keyboard", 64, 8, nil},
	} {
		inputs, inBits, outBits := decodeHIDTestFields(t, u.configMap[tc.key].reportDesc)
		if inBits != tc.inputBits || outBits != tc.outputBits {
			t.Errorf("%s report bits input/output = %d/%d, want %d/%d", tc.key, inBits, outBits, tc.inputBits, tc.outputBits)
		}
		if tc.wantInputs != nil && !reflect.DeepEqual(inputs, tc.wantInputs) {
			t.Errorf("%s fields = %+v, want %+v", tc.key, inputs, tc.wantInputs)
		}
	}
}

type hidTestField struct {
	page, firstUsage, lastUsage, size, count, minimum, maximum, flags int
}

func decodeHIDTestFields(t *testing.T, desc []byte) ([]hidTestField, int, int) {
	t.Helper()
	var field hidTestField
	var fields []hidTestField
	var inputBits, outputBits int
	for offset := 0; offset < len(desc); {
		prefix := desc[offset]
		offset++
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if prefix == 0xfe || offset+size > len(desc) {
			t.Fatal("unsupported or truncated descriptor item")
		}
		value := 0
		for i := 0; i < size; i++ {
			value |= int(desc[offset+i]) << (8 * i)
		}
		offset += size
		switch prefix & 0xfc {
		case 0x04:
			field.page = value
		case 0x08:
			if field.firstUsage == 0 {
				field.firstUsage = value
			}
			field.lastUsage = value
		case 0x18:
			field.firstUsage = value
		case 0x28:
			field.lastUsage = value
		case 0x14:
			if size > 0 && value&(1<<(size*8-1)) != 0 {
				value -= 1 << (size * 8)
			}
			field.minimum = value
		case 0x24:
			field.maximum = value
		case 0x74:
			field.size = value
		case 0x94:
			field.count = value
		case 0x84:
			t.Fatal("standard profile must not use report IDs")
		case 0x80:
			field.flags = value
			fields = append(fields, field)
			inputBits += field.size * field.count
		case 0x90:
			outputBits += field.size * field.count
		}
		if prefix&0x0c == 0 { // Main items clear local usages.
			field.firstUsage, field.lastUsage = 0, 0
		}
	}
	return fields, inputBits, outputBits
}

// Catches guessing a node when the kernel cannot identify the function's chardev.
func TestStandardRoutingFailsClosedForMissingOrMalformedDev(t *testing.T) {
	for _, value := range []string{"", "240:bad", "240:-1", "240:7:8", "garbage"} {
		t.Run(value, func(t *testing.T) {
			u := standardTestGadget(t)
			devPath := filepath.Join(u.kvmGadgetPath, "functions", "hid.usb2", "dev")
			if err := os.WriteFile(devPath, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
				t.Errorf("opened guessed node %s", name)
				return nil, os.ErrNotExist
			}
			if err := u.RelMouseReport(0, 0, 0); err == nil {
				t.Fatal("invalid dev returned success")
			}
		})
	}
}

// The OS normally writes four-byte reports atomically. This syscall-boundary
// double injects the otherwise rare short count so real writer validation is tested.
type shortHIDReportFile struct {
	n   int
	err error
}

func (f shortHIDReportFile) Name() string                     { return "short-hid" }
func (f shortHIDReportFile) SetWriteDeadline(time.Time) error { return nil }
func (f shortHIDReportFile) Write([]byte) (int, error)        { return f.n, f.err }

func TestStandardWriterRejectsShortAndFailedWrites(t *testing.T) {
	u := standardTestGadget(t)
	for _, tc := range []struct {
		name string
		n    int
		err  error
		want error
	}{
		{"short success", 3, nil, io.ErrShortWrite},
		{"zero success", 0, nil, io.ErrShortWrite},
		{"timeout", 0, os.ErrDeadlineExceeded, os.ErrDeadlineExceeded},
		{"partial timeout", 2, os.ErrDeadlineExceeded, os.ErrDeadlineExceeded},
		{"closed", 0, os.ErrClosed, os.ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := u.writeWithTimeout(shortHIDReportFile{tc.n, tc.err}, []byte{1, 0, 0, 0})
			if n != tc.n || !errors.Is(err, tc.want) {
				t.Fatalf("write result = %d, %v; want %d, %v", n, err, tc.n, tc.want)
			}
		})
	}
}

// Catches state/profile races between input, LED reader, config normalization and diagnostics.
func TestStandardConcurrentInputAndNormalization(t *testing.T) {
	u := standardTestGadget(t)
	_, w := reportPipe(t)
	u.relMouseHidFile = w
	devices := standardDevices(t)
	var wg sync.WaitGroup
	for _, operation := range []func(){
		func() { _ = u.RelMouseReport(1, -1, 1) },
		func() { _ = u.RelMouseWheelReport(1, 0) },
		func() { _ = u.GetLastUserInputTime() },
		func() { _ = u.SetGadgetDevices(&devices) },
		func() { u.updateKeyboardState(0) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				operation()
			}
		}()
	}
	wg.Wait()
}

// Catches the recovery probe opening a removed or unrelated legacy minor.
func TestStandardRecoveryProbeRoutesAllocatedKeyboardMinor(t *testing.T) {
	u := standardTestGadget(t)
	_, w := reportPipe(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		if name != "/dev/hidg4" {
			t.Errorf("recovery probe opened %s, want /dev/hidg4", name)
		}
		return w, nil
	}
	if !u.isHidgChardevHealthy() {
		t.Fatal("allocated keyboard minor was not probed")
	}
}

// Catches hiding stalled writes from the existing recovery monitor after closing a failed handle.
func TestStandardKeyboardTimeoutStreakSurvivesFailedHandleClose(t *testing.T) {
	u := standardTestGadget(t)
	_, w := reportPipe(t)
	u.keyboardHidFile = w
	fillPipeBuffer(t, w)
	if err := u.KeyboardReport(0, nil); !os.IsTimeout(err) {
		t.Fatalf("expected explicit timeout, got %v", err)
	}
	if got := u.KeyboardWriteTimeoutStreak(); got != 1 {
		t.Fatalf("timeout streak after failed handle close = %d, want 1", got)
	}
	u.ResetHIDFiles()
	if got := u.KeyboardWriteTimeoutStreak(); got != 0 {
		t.Fatalf("timeout streak after reset = %d, want 0", got)
	}
}
