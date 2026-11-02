package usbgadget

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Catches missing opt-in, wrong boot flags, stale values, or shared defaults.
func TestIdentity10ReportContractPolicy(t *testing.T) {
	u := standardTestGadget(t)
	check := func(standard bool) {
		t.Helper()
		for _, tc := range []struct{ key, contract, protocol, subclass, interval, iface string }{
			{"keyboard", "1", "1", "1", "8", "1"},
			{"relative_mouse", "2", "2", "1", "2", "1"},
		} {
			wants := map[string]string{"report_contract": tc.contract, "protocol": tc.protocol, "subclass": tc.subclass, "fs_interval": tc.interval, "no_interface_string": tc.iface}
			if !standard {
				wants = map[string]string{"report_contract": "0", "protocol": tc.protocol, "subclass": "1", "fs_interval": "0", "no_interface_string": "0"}
			}
			for attr, want := range wants {
				if got := u.configMap[tc.key].attrs[attr]; got != want {
					t.Errorf("%s %s=%q want %q", tc.key, attr, got, want)
				}
			}
		}
	}
	check(true)
	for _, std := range []bool{true, false, false, true, true} {
		for _, key := range []string{"keyboard", "relative_mouse"} {
			for _, attr := range []string{"report_contract", "protocol", "subclass", "fs_interval", "no_interface_string"} {
				u.configMap[key].attrs[attr] = "255"
			}
		}
		devices := defaultUsbGadgetDevices
		if std {
			devices = standardDevices(t)
			devices.Audio = true
		}
		if err := u.SetGadgetDevices(&devices); err != nil {
			t.Fatal(err)
		}
		check(std)
		for _, key := range []string{"keyboard", "relative_mouse"} {
			u.configMap[key].attrs["report_contract"] = "255"
		}
		u.loadHIDProfile()
		check(std)
	}
	other := standardTestGadget(t)
	legacy := defaultUsbGadgetDevices
	if err := other.SetGadgetDevices(&legacy); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"keyboard", "relative_mouse"} {
		if got := other.configMap[key].attrs["report_contract"]; got != "0" {
			t.Errorf("independent %s=%q", key, got)
		}
		if _, ok := defaultGadgetConfig[key].attrs["report_contract"]; ok {
			t.Errorf("global %s defaults mutated", key)
		}
	}
	check(true)
	if u.configMap["keyboard"].attrs["report_length"] != "8" || u.configMap["relative_mouse"].attrs["report_length"] != "4" {
		t.Fatal("canonical input lengths changed")
	}
}

// Configfs does not allow creating an absent kernel attribute. Exercise the real
// attribute writer/compensator with that filesystem failure, including legacy0.
func TestIdentity10RequiredReportContractAttributeFailureAndRollback(t *testing.T) {
	for _, profile := range []string{"standard", "legacy", "receiver"} {
		for _, failAt := range []string{"keyboard", "relative_mouse", "after-contracts"} {
			t.Run(profile+"/"+failAt, func(t *testing.T) {
				u := standardTestGadget(t)
				if profile == "receiver" {
					u = receiverTestGadget(t)
				}
				if profile == "legacy" {
					legacy := defaultUsbGadgetDevices
					if err := u.SetGadgetDevices(&legacy); err != nil {
						t.Fatal(err)
					}
				}
				gadget := t.TempDir()
				initial := map[string]string{filepath.Join(gadget, "UDC"): "old.usb", filepath.Join(gadget, "max_speed"): "full-speed", filepath.Join(gadget, "after-contracts"): "old"}
				for _, key := range []string{"keyboard", "relative_mouse"} {
					if err := os.Mkdir(filepath.Join(gadget, key), 0755); err != nil {
						t.Fatal(err)
					}
					protocol, length, noOut := "1", "8", "0"
					if key == "relative_mouse" {
						protocol, length, noOut = "2", "5", "1"
					}
					for attr, val := range map[string]string{"protocol": protocol, "subclass": "1", "no_interface_string": "0", "fs_interval": "0", "report_length": length, "no_out_endpoint": noOut} {
						if profile == "legacy" {
							val = "1"
							if attr == "protocol" && key == "relative_mouse" {
								val = "2"
							}
							if attr == "report_length" {
								val = "8"
								if key == "relative_mouse" {
									val = "4"
								}
							}
							if attr == "fs_interval" {
								if key == "keyboard" {
									val = "8"
								} else {
									val = "2"
								}
							}
						}
						initial[filepath.Join(gadget, key, attr)] = val
					}
					if failAt != key {
						// Initial attributes belong to the opposite profile: a failed
						// switch must restore standard1/2 or legacy0/0, including UDC.
						want := "0"
						if profile == "legacy" {
							if key == "keyboard" {
								want = "1"
							} else {
								want = "2"
							}
						}
						initial[filepath.Join(gadget, key, "report_contract")] = want
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
				tx.addFileChange("base", RequestedFileChange{Path: filepath.Join(gadget, "after-contracts"), ExpectedState: FileStateFileContentMatch, ExpectedContent: []byte("new"), DependsOn: deps})
				tx.WriteUDC()
				injected := errors.New("later interface transaction failure")
				if failAt != "after-contracts" {
					injected = os.ErrNotExist
				}
				failed := false
				tx.writeFile = func(p string, b []byte) error {
					missing := filepath.Join(gadget, failAt, "report_contract")
					if !failed && (p == missing || (failAt == "after-contracts" && filepath.Base(p) == failAt)) {
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
				if failAt != "after-contracts" {
					if _, err := os.Stat(filepath.Join(gadget, failAt, "report_contract")); !os.IsNotExist(err) {
						t.Errorf("missing attribute created: %v", err)
					}
				}
			})
		}
	}
}
