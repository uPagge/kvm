package usbgadget

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func receiverTestGadget(t *testing.T) *UsbGadget {
	t.Helper()
	u := standardTestGadget(t)
	u.receiverControl.reference = syntheticControlReference(t)
	d := Devices{Profile: "receiver-enumeration", Audio: true, AbsoluteMouse: true, MassStorage: true, SerialConsole: true}
	if err := u.SetGadgetDevices(&d); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestReceiverEnumerationCompositionAndContract(t *testing.T) {
	u := receiverTestGadget(t)
	var enabled []string
	for _, item := range u.getOrderedConfigItems() {
		if item.item.device != "" && u.isGadgetConfigItemEnabled(item.key) {
			enabled = append(enabled, item.key)
		}
	}
	if !reflect.DeepEqual(enabled, []string{"keyboard", "relative_mouse"}) {
		t.Fatalf("functions = %v", enabled)
	}
	mouse := u.configMap["relative_mouse"]
	for k, v := range map[string]string{"subclass": "1", "protocol": "2", "report_contract": "3", "report_length": "20", "fs_interval": "2", "no_out_endpoint": "1", "no_interface_string": "1"} {
		if mouse.attrs[k] != v {
			t.Errorf("%s=%s want %s", k, mouse.attrs[k], v)
		}
	}
	if len(mouse.reportDesc) != 177 || fmt.Sprintf("%x", sha256.Sum256(mouse.reportDesc)) != "cb1dac598eb6e18ab0e67086660d0dd0ce9234ad4203b85862e393df809731d5" {
		t.Fatal("mouse reference descriptor changed")
	}
	if len(u.configMap["keyboard"].reportDesc) != 59 || u.configMap["keyboard"].attrs["report_contract"] != "1" {
		t.Fatal("keyboard contract changed")
	}
	for _, profile := range []HIDProfile{HIDProfileStandardRelative, "", "receiver-enumeration"} {
		if err := u.SetGadgetDevices(&Devices{Profile: profile}); err != nil {
			t.Fatal(err)
		}
		want := map[HIDProfile]string{HIDProfileStandardRelative: "4", "": "5", "receiver-enumeration": "20"}[profile]
		if u.configMap["relative_mouse"].attrs["report_length"] != want {
			t.Fatalf("stale mouse length for %q", profile)
		}
	}
}

func TestReceiverEnumerationActualMouseFlow(t *testing.T) {
	u := receiverTestGadget(t)
	r, w := reportPipe(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		if name != "/dev/hidg7" {
			t.Errorf("opened %s", name)
		}
		return w, nil
	}
	for _, step := range []struct {
		run  func() error
		want []byte
	}{
		{func() error { return u.RelMouseReport(1, -1, 0x81) }, []byte{2, 0x81, 0, 1, 0xf0, 0xff, 0, 0}},
		{func() error { return u.RelMouseWheelReport(-127, 127) }, []byte{2, 0x81, 0, 0, 0, 0, 0x81, 127}},
		{func() error { return u.RelMouseReport(-128, 127, 0xff) }, []byte{2, 0xff, 0, 0x80, 0xff, 7, 0, 0}},
		{func() error { return u.RelMouseReport(0, 0, 0) }, []byte{2, 0, 0, 0, 0, 0, 0, 0}},
		{func() error { return u.RelMouseWheelReport(1, 0) }, []byte{2, 0, 0, 0, 0, 0, 1, 0}},
	} {
		if err := step.run(); err != nil {
			t.Fatal(err)
		}
		readReport(t, r, step.want)
	}
}

func TestReceiverEnumerationFailurePreservesButtons(t *testing.T) {
	u := receiverTestGadget(t)
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	if err := u.RelMouseReport(0, 0, 0x81); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{2, 0x81, 0, 0, 0, 0, 0, 0})
	for _, axes := range [][2]int8{{-128, 0}, {0, -128}} {
		if err := u.RelMouseWheelReport(axes[0], axes[1]); err == nil {
			t.Fatal("accepted invalid wheel")
		}
	}
	fillPipeBuffer(t, w)
	if err := u.RelMouseReport(0, 0, 2); err == nil {
		t.Fatal("lost write error")
	}
	nextR, nextW := reportPipe(t)
	u.hidOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nextW, nil }
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, nextR, []byte{2, 0x81, 0, 0, 0, 0, 1, 0})
	u.ResetHIDFiles()
	finalR, finalW := reportPipe(t)
	u.hidOpenFile = func(string, int, os.FileMode) (*os.File, error) { return finalW, nil }
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, finalR, []byte{2, 0, 0, 0, 0, 0, 1, 0})
}

func TestReceiverEnumerationMissingDevFailsClosed(t *testing.T) {
	u := receiverTestGadget(t)
	if err := os.Remove(filepath.Join(u.kvmGadgetPath, "functions", "hid.usb2", "dev")); err != nil {
		t.Fatal(err)
	}
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		t.Errorf("opened guessed node %s", name)
		return nil, os.ErrNotExist
	}
	if err := u.RelMouseReport(0, 0, 0); err == nil {
		t.Fatal("missing dev succeeded")
	}
}

// An older contract1/2 kernel must reject contract3 before UDC is rebound.
// Use the real transaction and mandatory attribute policy; emulate only the
// kernel configfs store's EINVAL for its unsupported new contract value.
func TestReceiverEnumerationOldKernelRejectsContract(t *testing.T) {
	u := receiverTestGadget(t)
	root := t.TempDir()
	function := filepath.Join(root, "mouse")
	if err := os.Mkdir(function, 0700); err != nil {
		t.Fatal(err)
	}
	initial := map[string]string{filepath.Join(root, "UDC"): "old.usb", filepath.Join(root, "max_speed"): "full-speed", filepath.Join(function, "report_contract"): "2"}
	for attr := range u.configMap["relative_mouse"].attrs {
		p := filepath.Join(function, attr)
		if _, ok := initial[p]; !ok {
			initial[p] = "0"
		}
	}
	for p, v := range initial {
		if err := os.WriteFile(p, []byte(v), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tx := &UsbGadgetTransaction{c: &ChangeSet{}, log: defaultLogger, kvmGadgetPath: root, udc: "old.usb"}
	tx.mkdirAll("base", root, "existing gadget", nil)
	tx.writeGadgetAttrs(root, gadgetAttributes{"max_speed": "full-speed"}, "base", nil)
	tx.mkdirAll("mouse", function, "existing HID", nil)
	tx.writeGadgetAttrs(function, u.configMap["relative_mouse"].attrs, "mouse", nil)
	tx.WriteUDC()
	rejected := false
	tx.writeFile = func(p string, b []byte) error {
		if p == filepath.Join(function, "report_contract") && string(b) == "3" {
			rejected = true
			return syscall.EINVAL
		}
		return os.WriteFile(p, b, 0600)
	}
	if err := tx.Commit(); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("old kernel contract rejection = %v", err)
	}
	if !rejected {
		t.Fatal("did not negotiate contract3")
	}
	for p, want := range initial {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("rollback %s=%q err=%v want %q", p, got, err, want)
		}
	}
	if u.GetGadgetDevices().Profile != "receiver-enumeration" || u.configMap["relative_mouse"].attrs["report_contract"] != "3" {
		t.Fatal("silently downgraded profile")
	}
}

func TestReceiverEnumerationDisabledLegacyFunctions(t *testing.T) {
	u := receiverTestGadget(t)
	u.hidOpenFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
		t.Errorf("opened absent %s", name)
		return nil, os.ErrNotExist
	}
	for _, run := range []func() error{
		func() error { return u.WakeReport(true) },
		func() error { return u.AbsMouseReport(1, 1, 1) },
		func() error { return u.AbsMouseWheelReport(1, 0) },
	} {
		if err := run(); err == nil {
			t.Fatal("disabled operation succeeded")
		}
	}
}

func TestReceiverEnumerationKeyboardAndAdoptionRelease(t *testing.T) {
	u := receiverTestGadget(t)
	kr, kw := reportPipe(t)
	mr, mw := reportPipe(t)
	u.keyboardHidFile, u.relMouseHidFile = kw, mw
	if err := u.KeyboardReport(2, []byte{4}); err != nil {
		t.Fatal(err)
	}
	readReport(t, kr, []byte{2, 0, 4, 0, 0, 0, 0, 0})
	if err := u.KeyboardReport(0, []byte{0xa5}); err == nil {
		t.Fatal("keyboard accepted undeclared array code")
	}
	if err := u.RelMouseReport(0, 0, 0xff); err != nil {
		t.Fatal(err)
	}
	readReport(t, mr, []byte{2, 0xff, 0, 0, 0, 0, 0, 0})
	u.adoptLiveGadget()
	readReport(t, kr, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	readReport(t, mr, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	if err := u.RelMouseWheelReport(1, 0); err != nil {
		t.Fatal(err)
	}
	readReport(t, mr, []byte{2, 0, 0, 0, 0, 0, 1, 0})
	u.updateKeyboardState(0)
	u.updateKeyboardState(0x40)
	state := u.GetKeyboardState()
	if state.Byte() != 0 {
		t.Fatal("accepted undeclared LED bit")
	}
}

func TestReceiverEnumerationIdentityPolicyPreservesCustomIdentity(t *testing.T) {
	u := receiverTestGadget(t)
	measured := Config{VendorId: "0x046d", ProductId: "0xc534", Manufacturer: "Logitech", Product: "USB Receiver"}
	u.SetGadgetConfig(&measured)
	if u.configMap["base"].attrs["max_speed"] != "full-speed" || u.configMap["base"].attrs["bcdDevice"] != "0x5100" {
		t.Fatal("lost measured speed/revision policy")
	}
	custom := measured
	custom.SerialNumber = "custom-serial"
	u.SetGadgetConfig(&custom)
	if u.customConfig != custom || u.configMap["base_info"].attrs["serialnumber"] != "custom-serial" || u.configMap["base"].attrs["bcdDevice"] != "0x0100" {
		t.Fatal("custom identity rewritten")
	}
}
