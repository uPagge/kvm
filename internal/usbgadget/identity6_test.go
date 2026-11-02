package usbgadget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Catches stale transport after profile transitions and shared-default mutation.
func TestIdentity6KeyboardControlOutputPolicy(t *testing.T) {
	u := standardTestGadget(t)
	check := func(want string) {
		t.Helper()
		if got := u.configMap["keyboard"].attrs["no_out_endpoint"]; got != want {
			t.Errorf("keyboard no_out_endpoint = %q, want %q", got, want)
		}
	}
	check("1")
	legacy := defaultUsbGadgetDevices
	if err := u.SetGadgetDevices(&legacy); err != nil {
		t.Fatal(err)
	}
	check("0")
	standard := standardDevices(t)
	if err := u.SetGadgetDevices(&standard); err != nil {
		t.Fatal(err)
	}
	check("1")
	standard.Audio = true
	if err := u.SetGadgetDevices(&standard); err != nil {
		t.Fatal(err)
	}
	check("1")
	other := standardTestGadget(t)
	if err := other.SetGadgetDevices(&legacy); err != nil {
		t.Fatal(err)
	}
	if got := other.configMap["keyboard"].attrs["no_out_endpoint"]; got != "0" {
		t.Errorf("independent legacy = %q", got)
	}
	check("1")
	if got := defaultGadgetConfig["keyboard"].attrs["no_out_endpoint"]; got != "0" {
		t.Errorf("default mutated = %q", got)
	}
}

// Catches treating the new required attribute as optional or failing to restore it.
func TestIdentity6TransportTransactionFailureAndRollback(t *testing.T) {
	for _, failAt := range []string{"no_out_endpoint", "after-transport"} {
		t.Run(failAt, func(t *testing.T) {
			u := standardTestGadget(t)
			gadget := t.TempDir()
			keyboard := filepath.Join(gadget, "keyboard")
			if err := os.Mkdir(keyboard, 0755); err != nil {
				t.Fatal(err)
			}
			for p, v := range map[string]string{filepath.Join(gadget, "UDC"): "old.usb", filepath.Join(gadget, "max_speed"): "full-speed", filepath.Join(keyboard, "no_out_endpoint"): "0", filepath.Join(keyboard, "after-transport"): "old"} {
				if err := os.WriteFile(p, []byte(v), 0644); err != nil {
					t.Fatal(err)
				}
			}
			tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, udc: "old.usb"}
			tx.mkdirAll("base", gadget, "existing gadget", nil)
			tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
			tx.mkdirAll("keyboard", keyboard, "existing keyboard", nil)
			deps := tx.writeGadgetAttrs(keyboard, gadgetAttributes{"no_out_endpoint": u.configMap["keyboard"].attrs["no_out_endpoint"]}, "keyboard", nil)
			tx.addFileChange("keyboard", RequestedFileChange{Path: filepath.Join(keyboard, "after-transport"), ExpectedState: FileStateFileContentMatch, ExpectedContent: []byte("new"), DependsOn: deps})
			tx.WriteUDC()
			injected := errors.New("transport transaction failure")
			failed := false
			tx.writeFile = func(p string, b []byte) error {
				if filepath.Base(p) == failAt && !failed {
					failed = true
					return injected
				}
				return os.WriteFile(p, b, 0644)
			}
			if err := tx.Commit(); !errors.Is(err, injected) {
				t.Fatalf("lost failure: %v", err)
			}
			for p, want := range map[string]string{filepath.Join(gadget, "UDC"): "old.usb", filepath.Join(gadget, "max_speed"): "full-speed", filepath.Join(keyboard, "no_out_endpoint"): "0", filepath.Join(keyboard, "after-transport"): "old"} {
				got, err := os.ReadFile(p)
				if err != nil || string(got) != want {
					t.Errorf("rollback %s=%q err=%v want %q", p, got, err, want)
				}
			}
		})
	}
}

// Exercises the unchanged actual reader and decoder using controlled output reports.
func TestIdentity6KeyboardLEDReader(t *testing.T) {
	initial, initialErr := os.ReadFile(hidHandoverPath)
	if initialErr != nil && !os.IsNotExist(initialErr) {
		t.Fatal(initialErr)
	}
	t.Cleanup(func() {
		hidHandoverLock.Lock()
		defer hidHandoverLock.Unlock()
		if initialErr == nil {
			if err := os.WriteFile(hidHandoverPath, initial, 0600); err != nil {
				t.Error(err)
			}
		} else if err := os.Remove(hidHandoverPath); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	u := standardTestGadget(t)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if err := syscall.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
	}
	r, w := os.NewFile(uintptr(fds[0]), "LED-reader"), os.NewFile(uintptr(fds[1]), "LED-host-fixture")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); r.Close(); w.Close() })
	updates := make(chan byte, 8)
	u.SetOnKeyboardStateChange(func(state KeyboardState) { updates <- state.Byte() })
	u.listenKeyboardEvents(ctx, r)
	for _, payload := range [][]byte{{0x1f}, {0x02, 0x03}, {0x40}, {0x02}} {
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []byte{0x1f, 0x02} {
		select {
		case got := <-updates:
			if got != want {
				t.Fatalf("LED update=%x want %x", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("no valid LED update")
		}
	}
	select {
	case got := <-updates:
		t.Fatalf("malformed report published %x", got)
	default:
	}
	state := u.GetKeyboardState()
	if state.Byte() != 0x02 || !state.CapsLock || state.NumLock || state.ScrollLock || state.Compose || state.Kana || state.Shift {
		t.Fatalf("decoded LED state=%+v", state)
	}
}
