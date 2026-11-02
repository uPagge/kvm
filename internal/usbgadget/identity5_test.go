package usbgadget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// Catches applying the measured revision to an unknown identity, stale revision
// after a setter, and leaking per-instance policy into the global defaults.
func TestIdentity5ActivePolicy(t *testing.T) {
	u := standardTestGadget(t)
	other := standardTestGadget(t)
	target := Config{VendorId: "0x046D", ProductId: "0XC534", Manufacturer: "Logitech", Product: "USB Receiver"}
	for _, tc := range []struct {
		name       string
		config     Config
		profile    HIDProfile
		audio      bool
		bcd, speed string
	}{
		{"measured", target, HIDProfileStandardRelative, false, "0x5100", "full-speed"},
		{"audio", target, HIDProfileStandardRelative, true, "0x5100", "full-speed"},
		{"legacy", target, "", false, "0x0100", "super-speed-plus"},
		{"back", target, HIDProfileStandardRelative, false, "0x5100", "full-speed"},
		{"serial", Config{VendorId: "0x046d", ProductId: "0xc534", Manufacturer: "Logitech", Product: "USB Receiver", SerialNumber: "custom"}, HIDProfileStandardRelative, false, "0x0100", "full-speed"},
		{"unknown", Config{VendorId: "0x046d", ProductId: "0xc533", Manufacturer: "Logitech", Product: "USB Receiver"}, HIDProfileStandardRelative, false, "0x0100", "full-speed"},
		{"string", Config{VendorId: "0x046d", ProductId: "0xc534", Manufacturer: "Other", Product: "USB Receiver"}, HIDProfileStandardRelative, false, "0x0100", "full-speed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u.SetGadgetConfig(&tc.config)
			if err := u.SetGadgetDevices(&Devices{Profile: tc.profile, Audio: tc.audio}); err != nil {
				t.Fatal(err)
			}
			attrs := u.configMap["base"].attrs
			if attrs["bcdDevice"] != tc.bcd || attrs["max_speed"] != tc.speed {
				t.Fatalf("active attributes=%v; want bcd=%s speed=%s", attrs, tc.bcd, tc.speed)
			}
			if u.isGadgetConfigItemEnabled("audio") != tc.audio {
				t.Fatal("audio selection lost")
			}
			if attrs["idVendor"] != tc.config.VendorId || attrs["idProduct"] != tc.config.ProductId {
				t.Fatal("custom identity changed")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "UDC"), []byte("\n"), 0644); err != nil {
				t.Fatal(err)
			}
			tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: dir, udc: "test.usb"}
			tx.mkdirAll("base", dir, "gadget", nil)
			tx.writeGadgetAttrs(dir, attrs, "base", nil)
			tx.WriteUDC()
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"bcdDevice": tc.bcd, "max_speed": tc.speed} {
				got, err := os.ReadFile(filepath.Join(dir, key))
				if err != nil || string(got) != want {
					t.Fatalf("active write %s=%q err=%v want %q", key, got, err, want)
				}
			}
		})
	}
	if other.configMap["base"].attrs["bcdDevice"] != "0x0100" || defaultGadgetConfig["base"].attrs["bcdDevice"] != "0x0100" {
		t.Fatal("cross-instance revision leak")
	}
	// Config changes alone, without a profile setter, must recalculate policy.
	u.SetGadgetConfig(&target)
	if u.configMap["base"].attrs["bcdDevice"] != "0x5100" {
		t.Fatal("config setter did not derive revision")
	}
	u.SetGadgetConfig(&Config{isEmpty: true})
	if u.configMap["base"].attrs["bcdDevice"] != "0x0100" {
		t.Fatal("default reset retained measured revision")
	}
}

// Catches writing max_speed while bound, binding before the attributes commit,
// and reporting success or restoring old speed while bound after a failure.
func TestIdentity5TransactionalSpeed(t *testing.T) {
	for _, failure := range []string{"", "max_speed", "UDC", "bind", "bind-sideeffect", "unbind", "unbind-sideeffect"} {
		t.Run("failure="+failure, func(t *testing.T) {
			dir := t.TempDir()
			gadget := filepath.Join(dir, "gadget")
			platform := filepath.Join(dir, "platform")
			for _, p := range []string{gadget, platform} {
				if err := os.MkdirAll(p, 0755); err != nil {
					t.Fatal(err)
				}
			}
			initial := map[string]string{"UDC": "old.usb", "max_speed": "super-speed-plus", "bcdDevice": "0x0100"}
			for k, v := range initial {
				if err := os.WriteFile(filepath.Join(gadget, k), []byte(v), 0644); err != nil {
					t.Fatal(err)
				}
			}
			for _, k := range []string{"bind", "unbind"} {
				if err := os.WriteFile(filepath.Join(platform, k), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			configDir := filepath.Join(gadget, "configs", "c.1")
			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatal(err)
			}
			oldLink := filepath.Join(configDir, "old")
			if err := os.Symlink(gadget, oldLink); err != nil {
				t.Fatal(err)
			}
			tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, configC1Path: configDir, dwc3Path: platform, udc: "new.usb"}
			attachment := filepath.Join(platform, tx.udc)
			if err := os.Symlink(gadget, attachment); err != nil {
				t.Fatal(err)
			}
			var trace []string
			injected := errors.New("injected " + failure)
			failed := false
			tx.writeFile = func(p string, b []byte) error {
				k := filepath.Base(p)
				value := strings.TrimSpace(string(b))
				trace = append(trace, k+"="+value)
				if k == "UDC" && value == "" {
					bound, _ := os.ReadFile(p)
					if strings.TrimSpace(string(bound)) == "" {
						return syscall.ENODEV
					}
				}
				if k == "max_speed" {
					bound, _ := os.ReadFile(filepath.Join(gadget, "UDC"))
					if strings.TrimSpace(string(bound)) != "" {
						return syscall.EBUSY
					}
				}
				if k == strings.TrimSuffix(failure, "-sideeffect") && !failed && (k != "UDC" || value == "new.usb") {
					failed = true
					if failure == "unbind-sideeffect" {
						if err := os.Remove(attachment); err != nil {
							t.Fatal(err)
						}
					}
					if failure == "bind-sideeffect" {
						if err := os.Symlink(gadget, attachment); err != nil {
							t.Fatal(err)
						}
					}
					return injected
				}
				if k == "unbind" {
					if err := os.Remove(attachment); err != nil {
						return err
					}
				}
				if k == "bind" {
					if _, err := os.Stat(attachment); err == nil {
						return syscall.EBUSY
					}
					if err := os.Symlink(gadget, attachment); err != nil {
						return err
					}
				}
				return os.WriteFile(p, b, 0644)
			}
			// Put speed first deterministically; this is the same staged attribute
			// writer and UDC/platform path used by WriteGadgetConfig.
			tx.mkdirAll("base", gadget, "existing gadget", nil)
			tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
			tx.addFileChange("base", RequestedFileChange{Path: filepath.Join(gadget, "bcdDevice"), ExpectedState: FileStateFileContentMatch, ExpectedContent: []byte("0x5100"), DependsOn: []string{filepath.Join(gadget, "max_speed")}})
			tx.removeFile("base", oldLink, "remove old function")
			tx.addReorderSymlinkChange(filepath.Join(configDir, "new"), gadget, []string{filepath.Join(gadget, "bcdDevice"), oldLink})
			tx.WriteUDC()
			tx.RebindUsb(true) // Same flag as configureUsbGadgetLocked.
			err := tx.Commit()
			t.Logf("actual write trace: %v", trace)
			if failure == "" {
				if err != nil {
					t.Fatalf("commit: %v; trace=%v", err, trace)
				}
				if !reflect.DeepEqual(trace, []string{"UDC=", "max_speed=full-speed", "bcdDevice=0x5100", "UDC=new.usb", "unbind=new.usb", "bind=new.usb"}) {
					t.Fatalf("bad actual write order: %v", trace)
				}
			} else {
				if !errors.Is(err, injected) {
					t.Fatalf("lost injected error: %v trace=%v", err, trace)
				}
				for k, v := range initial {
					got, e := os.ReadFile(filepath.Join(gadget, k))
					if e != nil || strings.TrimSpace(string(got)) != v {
						t.Errorf("rollback %s=%q err=%v want %q; trace=%v", k, got, e, v, trace)
					}
				}
				if target, e := os.Readlink(oldLink); e != nil || target != gadget {
					t.Fatalf("old link not restored: %s %v", target, e)
				}
				if _, e := os.Lstat(filepath.Join(configDir, "new")); !os.IsNotExist(e) {
					t.Fatalf("new link left after rollback: %v", e)
				}
				if trace[len(trace)-1] != "UDC=old.usb" {
					t.Fatalf("old gadget was not restored last: %v", trace)
				}
			}
		})
	}
}

// Catches suppressing compensation errors or reattaching a partially restored gadget.
func TestIdentity5RollbackFailureRemainsUnbound(t *testing.T) {
	dir := t.TempDir()
	gadget := filepath.Join(dir, "gadget")
	platform := filepath.Join(dir, "platform")
	for _, p := range []string{gadget, platform} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"UDC": "old.usb", "max_speed": "super-speed-plus"} {
		if err := os.WriteFile(filepath.Join(gadget, k), []byte(v), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, dwc3Path: platform, udc: "old.usb"}
	primary := errors.New("primary bind failure")
	restore := errors.New("restore speed failure")
	failed := false
	tx.writeFile = func(p string, b []byte) error {
		if filepath.Base(p) == "bind" && !failed {
			failed = true
			return primary
		}
		if filepath.Base(p) == "max_speed" {
			bound, _ := os.ReadFile(filepath.Join(gadget, "UDC"))
			if strings.TrimSpace(string(bound)) != "" {
				return syscall.EBUSY
			}
			if string(b) == "super-speed-plus" {
				return restore
			}
		}
		return os.WriteFile(p, b, 0644)
	}
	tx.mkdirAll("base", gadget, "gadget", nil)
	tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
	tx.WriteUDC()
	tx.RebindUsb(true)
	err := tx.Commit()
	if !errors.Is(err, primary) || !errors.Is(err, restore) {
		t.Fatalf("lost original/compensation error: %v", err)
	}
	bound, _ := os.ReadFile(filepath.Join(gadget, "UDC"))
	if strings.TrimSpace(string(bound)) != "" {
		t.Fatalf("partially restored gadget rebound: %s", bound)
	}
}

// Catches relying on the enabled-function reorder to order the final bind:
// first creation and a legacy profile with no functions still need all attrs.
func TestIdentity5FirstCreationWithoutFunctions(t *testing.T) {
	for range 40 {
		gadget := filepath.Join(t.TempDir(), "gadget")
		tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, udc: "new.usb"}
		tx.mkdirAll("base", gadget, "create gadget", nil)
		tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed", "bcdDevice": "0x5100"}, "base", nil)
		tx.WriteUDC()
		var trace []string
		tx.writeFile = func(p string, b []byte) error {
			k := filepath.Base(p)
			v := strings.TrimSpace(string(b))
			trace = append(trace, k+"="+v)
			bound, _ := os.ReadFile(filepath.Join(gadget, "UDC"))
			if k == "max_speed" && strings.TrimSpace(string(bound)) != "" {
				return syscall.EBUSY
			}
			if k == "UDC" && v != "" {
				speed, _ := os.ReadFile(filepath.Join(gadget, "max_speed"))
				bcd, _ := os.ReadFile(filepath.Join(gadget, "bcdDevice"))
				if string(speed) != "full-speed" || string(bcd) != "0x5100" {
					return errors.New("bind before attributes")
				}
			}
			return os.WriteFile(p, b, 0644)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("first creation failed: %v trace=%v", err, trace)
		}
		if len(trace) != 3 || trace[2] != "UDC=new.usb" {
			t.Fatalf("first creation order=%v", trace)
		}
	}
}

// Catches disconnecting a matching inherited gadget despite no staged change,
// and accidentally dropping explicit platform rebind requests for matching attrs.
func TestIdentity5MatchingAdoptionAndForcedRebind(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			gadget := t.TempDir()
			platform := t.TempDir()
			for k, v := range map[string]string{"UDC": "test.usb", "max_speed": "full-speed", "bcdDevice": "0x5100"} {
				if err := os.WriteFile(filepath.Join(gadget, k), []byte(v), 0644); err != nil {
					t.Fatal(err)
				}
			}
			tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, dwc3Path: platform, udc: "test.usb"}
			tx.mkdirAll("base", gadget, "existing gadget", nil)
			tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed", "bcdDevice": "0x5100"}, "base", nil)
			tx.WriteUDC()
			if force {
				tx.RebindUsb(true)
			}
			pending, err := tx.HasPendingChanges()
			if err != nil || pending != force {
				t.Fatalf("pending=%v err=%v force=%v", pending, err, force)
			}
			var trace []string
			tx.writeFile = func(p string, b []byte) error {
				trace = append(trace, filepath.Base(p)+"="+strings.TrimSpace(string(b)))
				return os.WriteFile(p, b, 0644)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if !force && len(trace) != 0 {
				t.Fatalf("matching adoption disconnected: %v", trace)
			}
			if force && !reflect.DeepEqual(trace, []string{"UDC=", "UDC=test.usb", "unbind=test.usb", "bind=test.usb"}) {
				t.Fatalf("force rebind not executed: %v", trace)
			}
		})
	}
}

// Catches treating unreadable attachment state as detached, and skipping
// real UDC read errors as if this were an absent first-created gadget.
func TestIdentity5AttachmentReadErrors(t *testing.T) {
	for _, where := range []string{"initial-UDC", "rollback-UDC", "rollback-platform", "missing-UDC-existing-root"} {
		t.Run(where, func(t *testing.T) {
			gadget := t.TempDir()
			platform := t.TempDir()
			for k, v := range map[string]string{"UDC": "test.usb", "max_speed": "super-speed-plus"} {
				if err := os.WriteFile(filepath.Join(gadget, k), []byte(v), 0644); err != nil {
					t.Fatal(err)
				}
			}
			tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, dwc3Path: platform, udc: "test.usb"}
			tx.mkdirAll("base", gadget, "existing gadget", nil)
			tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
			tx.WriteUDC()
			tx.RebindUsb(true)
			primary := errors.New("primary write failure")
			readErr := errors.New("attachment read failure")
			failed := false
			writes := 0
			if where == "missing-UDC-existing-root" {
				if err := os.Remove(filepath.Join(gadget, "UDC")); err != nil {
					t.Fatal(err)
				}
			}
			tx.attachmentReader = &configAttachmentReader{
				readFile: func(p string) ([]byte, error) {
					if where == "initial-UDC" || (where == "rollback-UDC" && failed) {
						return nil, readErr
					}
					return os.ReadFile(p)
				},
				stat: func(p string) (os.FileInfo, error) {
					if where == "rollback-platform" && p == filepath.Join(platform, tx.udc) {
						return nil, readErr
					}
					return os.Stat(p)
				},
			}
			tx.writeFile = func(p string, b []byte) error {
				writes++
				if !failed && ((where == "rollback-UDC" && filepath.Base(p) == "max_speed") || (where == "rollback-platform" && filepath.Base(p) == "unbind")) {
					failed = true
					return primary
				}
				return os.WriteFile(p, b, 0644)
			}
			err := tx.Commit()
			if where == "missing-UDC-existing-root" {
				if !errors.Is(err, os.ErrNotExist) || writes != 0 {
					t.Fatalf("missing existing UDC: %v writes=%d", err, writes)
				}
				return
			}
			if !errors.Is(err, readErr) {
				t.Fatalf("lost attachment read error: %v", err)
			}
			if where == "initial-UDC" && writes != 0 {
				t.Fatalf("wrote after initial read failure: %d", writes)
			}
			if strings.HasPrefix(where, "rollback") {
				if !errors.Is(err, primary) {
					t.Fatalf("lost original error: %v", err)
				}
				bound, _ := os.ReadFile(filepath.Join(gadget, "UDC"))
				if strings.TrimSpace(string(bound)) != "" {
					t.Fatalf("reattached after uncertain state: %s", bound)
				}
			}
		})
	}
}
