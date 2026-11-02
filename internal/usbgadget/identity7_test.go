package usbgadget

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Catches stale interface strings across transitions and mutation of defaults.
func TestIdentity7InterfaceStringPolicy(t *testing.T) {
	u := standardTestGadget(t)
	check := func(want string) {
		t.Helper()
		for _, key := range []string{"keyboard", "relative_mouse"} {
			if got := u.configMap[key].attrs["no_interface_string"]; got != want {
				t.Errorf("%s no_interface_string=%q want %q", key, got, want)
			}
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
	for _, key := range []string{"keyboard", "relative_mouse"} {
		if got := other.configMap[key].attrs["no_interface_string"]; got != "0" {
			t.Errorf("independent %s=%q", key, got)
		}
		if _, exists := defaultGadgetConfig[key].attrs["no_interface_string"]; exists {
			t.Errorf("global %s defaults mutated", key)
		}
	}
	check("1")
	if got := u.configMap["keyboard"].attrs["no_out_endpoint"]; got != "1" {
		t.Errorf("Identity6 transport regressed: %q", got)
	}
}

// Configfs does not allow creating an absent kernel attribute. Exercise the real
// attribute writer/compensator with that filesystem failure, including legacy0.
func TestIdentity7RequiredInterfaceAttributeFailureAndRollback(t *testing.T) {
	for _, profile := range []string{"standard", "legacy"} {
		for _, failAt := range []string{"keyboard", "relative_mouse", "after-interfaces"} {
			t.Run(profile+"/"+failAt, func(t *testing.T) {
				u := standardTestGadget(t)
				if profile == "legacy" {
					legacy := defaultUsbGadgetDevices
					if err := u.SetGadgetDevices(&legacy); err != nil {
						t.Fatal(err)
					}
				}
				gadget := t.TempDir()
				initial := map[string]string{filepath.Join(gadget, "UDC"): "old.usb", filepath.Join(gadget, "max_speed"): "full-speed", filepath.Join(gadget, "after-interfaces"): "old"}
				for _, key := range []string{"keyboard", "relative_mouse"} {
					if err := os.Mkdir(filepath.Join(gadget, key), 0755); err != nil {
						t.Fatal(err)
					}
					if failAt != key {
						initial[filepath.Join(gadget, key, "no_interface_string")] = "old"
					}
				}
				for p, v := range initial {
					if err := os.WriteFile(p, []byte(v), 0644); err != nil {
						t.Fatal(err)
					}
				}
				tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: gadget, udc: "old.usb"}
				tx.mkdirAll("base", gadget, "existing gadget", nil)
				tx.writeGadgetAttrs(gadget, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
				var deps []string
				for _, key := range []string{"keyboard", "relative_mouse"} {
					base := filepath.Join(gadget, key)
					tx.mkdirAll(key, base, "existing function", nil)
					// Use the complete selected attributes: absence from policy must
					// fail this test rather than inventing the new option in the fixture.
					deps = append(deps, tx.writeGadgetAttrs(base, u.configMap[key].attrs, key, nil)...)
				}
				tx.addFileChange("base", RequestedFileChange{Path: filepath.Join(gadget, "after-interfaces"), ExpectedState: FileStateFileContentMatch, ExpectedContent: []byte("new"), DependsOn: deps})
				tx.WriteUDC()
				injected := errors.New("later interface transaction failure")
				if failAt != "after-interfaces" {
					injected = os.ErrNotExist
				}
				failed := false
				tx.writeFile = func(p string, b []byte) error {
					missing := filepath.Join(gadget, failAt, "no_interface_string")
					if !failed && (p == missing || (failAt == "after-interfaces" && filepath.Base(p) == failAt)) {
						failed = true
						return injected
					}
					return os.WriteFile(p, b, 0644)
				}
				if err := tx.Commit(); !errors.Is(err, injected) {
					t.Fatalf("missing mandatory failure: %v", err)
				}
				for p, want := range initial {
					got, err := os.ReadFile(p)
					if err != nil || string(got) != want {
						t.Errorf("rollback %s=%q err=%v want %q", p, got, err, want)
					}
				}
				if failAt != "after-interfaces" {
					if _, err := os.Stat(filepath.Join(gadget, failAt, "no_interface_string")); !os.IsNotExist(err) {
						t.Errorf("missing attribute created: %v", err)
					}
				}
			})
		}
	}
}
