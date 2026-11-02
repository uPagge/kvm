package kvm

import (
	"encoding/json"
	"github.com/jetkvm/kvm/internal/usbgadget"
	"testing"
)

func TestReceiverEnumerationConfigExplicitOptIn(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"usb_devices":{"hid_profile":"receiver-enumeration","audio":true,"absolute_mouse":true,"mass_storage":true},"audio_enabled":true}`), &c); err != nil {
		t.Fatal(err)
	}
	want := usbgadget.Devices{Profile: usbgadget.HIDProfileReceiverEnumeration, Keyboard: true, RelativeMouse: true}
	if c.UsbDevices == nil || *c.UsbDevices != want || c.AudioEnabled {
		t.Fatalf("receiver config = %+v audio=%v", c.UsbDevices, c.AudioEnabled)
	}
	if err := validateUSBProfile(usbgadget.HIDProfileReceiverEnumeration); err != nil {
		t.Fatal(err)
	}
	if err := validateUSBProfile("receiver-enumeration-typo"); err == nil {
		t.Fatal("accepted unknown profile")
	}
	if err := json.Unmarshal([]byte(`{}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.UsbDevices.Profile != usbgadget.HIDProfileStandardRelative {
		t.Fatal("changed default profile")
	}
}
