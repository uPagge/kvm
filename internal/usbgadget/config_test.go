package usbgadget

import "testing"

func TestIdentity3NeutralLowLevelUSBDefaults(t *testing.T) {
	u := &UsbGadget{configMap: deepCopyConfigMap(defaultGadgetConfig), customConfig: Config{isEmpty: true}, log: defaultLogger}
	u.loadGadgetConfig()
	identity := u.configMap["base_info"].attrs
	if identity["manufacturer"] != "" || identity["product"] != "USB Input Device" || identity["serialnumber"] != "" {
		t.Fatalf("revealing low-level USB identity: %v", identity)
	}
	base := u.configMap["base"].attrs
	if base["idVendor"] != "0x1d6b" || base["idProduct"] != "0x0104" {
		t.Fatalf("VID/PID changed: %v", base)
	}
}

func TestIdentity3LowLevelCustomIdentityPreserved(t *testing.T) {
	u := &UsbGadget{configMap: deepCopyConfigMap(defaultGadgetConfig), customConfig: Config{VendorId: "0x1234", ProductId: "0x5678", Manufacturer: "My manufacturer", Product: "My product", SerialNumber: "custom-serial"}, log: defaultLogger}
	u.loadGadgetConfig()
	identity, base := u.configMap["base_info"].attrs, u.configMap["base"].attrs
	if identity["manufacturer"] != "My manufacturer" || identity["product"] != "My product" || identity["serialnumber"] != "custom-serial" || base["idVendor"] != "0x1234" || base["idProduct"] != "0x5678" {
		t.Fatalf("custom gadget identity overwritten: %v / %v", identity, base)
	}
}

func TestAudioGadgetConfigFollowsEnabledDevice(t *testing.T) {
	u := &UsbGadget{enabledDevices: Devices{Audio: false}}
	if u.isGadgetConfigItemEnabled("audio") {
		t.Fatal("audio gadget should be disabled when audio device is disabled")
	}

	u.enabledDevices.Audio = true
	if !u.isGadgetConfigItemEnabled("audio") {
		t.Fatal("audio gadget should be enabled when audio device is enabled")
	}
}

func TestBaseGadgetConfigItemsAlwaysEnabled(t *testing.T) {
	u := &UsbGadget{}
	for _, item := range []string{"base", "base_info", "wake_hid"} {
		if !u.isGadgetConfigItemEnabled(item) {
			t.Fatalf("%s should always be enabled", item)
		}
	}
}
