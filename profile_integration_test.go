package kvm

import (
	"encoding/json"
	"testing"

	"github.com/jetkvm/kvm/internal/usbgadget"
)

func TestProfileConfigMigration(t *testing.T) {
	for _, tc := range []struct {
		name, raw, profile string
		invalid            bool
	}{
		{"missing devices", `{}`, "standard-relative", false},
		{"old full config", `{"usb_devices":{"absolute_mouse":true,"keyboard":true,"relative_mouse":true,"audio":true,"mass_storage":true},"audio_enabled":true}`, "standard-relative", false},
		{"explicit legacy", `{"usb_devices":{"hid_profile":"","absolute_mouse":true,"keyboard":true,"relative_mouse":true,"audio":true,"mass_storage":true}}`, "", false},
		{"unknown profile", `{"usb_devices":{"hid_profile":"future"}}`, "", true},
		{"null profile", `{"usb_devices":{"hid_profile":null}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded := getDefaultConfig()
			err := json.Unmarshal([]byte(tc.raw), &loaded)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid profile accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := loaded.UsbDevices
			if string(d.Profile) != tc.profile {
				t.Fatalf("profile %q, want %q", d.Profile, tc.profile)
			}
			if tc.profile != "" && (!d.Keyboard || !d.RelativeMouse || d.AbsoluteMouse || d.Audio || d.MassStorage || loaded.AudioEnabled) {
				t.Fatalf("migration composition %+v", d)
			}
			if tc.profile == "" && (!d.AbsoluteMouse || !d.Audio || !d.MassStorage) {
				t.Fatalf("legacy recovery was changed %+v", d)
			}
		})
	}
}

func withProfileConfig(t *testing.T, devices usbgadget.Devices) {
	t.Helper()
	oldConfig, oldGadget := config, gadget
	config = &Config{UsbDevices: &devices, AudioEnabled: true}
	gadget = nil // Invalid requests must fail before hardware or persistence.
	t.Cleanup(func() { config, gadget = oldConfig, oldGadget })
}

func TestForkDefaultsAndEffectiveUSBProfile(t *testing.T) {
	d := *getDefaultConfig().UsbDevices
	if string(d.Profile) != "standard-relative" || !d.Keyboard || !d.RelativeMouse || d.AbsoluteMouse || d.Audio || d.MassStorage || d.SerialConsole {
		t.Fatalf("unexpected fork composition: %+v", d)
	}
	raw := usbgadget.Devices{Profile: "standard-relative", AbsoluteMouse: true, Audio: true, MassStorage: true}
	withProfileConfig(t, raw)
	effective := effectiveUsbDevices()
	if !effective.Keyboard || !effective.RelativeMouse || effective.AbsoluteMouse || !effective.Audio || effective.MassStorage {
		t.Fatalf("effective composition: %+v", effective)
	}
	if *config.UsbDevices != raw {
		t.Fatal("effective state mutated saved config")
	}
	if !effectiveAudioEnabled() {
		t.Fatal("standard profile suppressed explicitly selected audio")
	}
	got, err := rpcGetUsbDevices()
	if err != nil || got != *effective {
		t.Fatalf("get devices: %+v, %v", got, err)
	}
}

func TestUSBRejectedMutationsPreserveConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func() error
	}{
		{"unknown profile", func() error { return rpcSetUsbDevices(usbgadget.Devices{Profile: "unknown"}) }},
		{"profile switch", func() error { return rpcSetUsbDevices(usbgadget.Devices{Keyboard: true}) }},
		{"absolute enable", func() error { return rpcSetUsbDeviceState("absoluteMouse", true) }},
		{"keyboard disable", func() error { return rpcSetUsbDeviceState("keyboard", false) }},
		{"relative disable", func() error { return rpcSetUsbDeviceState("relativeMouse", false) }},
		{"serial enable", func() error { return rpcSetUsbDeviceState("serialConsole", true) }},
		{"storage enable", func() error { return rpcSetUsbDeviceState("massStorage", true) }},
		{"capture audio enable", func() error { return rpcSetAudioConfig(AudioConfig{Enabled: true}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withProfileConfig(t, usbgadget.Devices{Profile: "standard-relative", Keyboard: true, RelativeMouse: true})
			before := *config.UsbDevices
			if err := tc.change(); err == nil {
				t.Fatal("unsupported mutation was accepted")
			}
			if *config.UsbDevices != before || !config.AudioEnabled {
				t.Fatal("rejected mutation changed config")
			}
		})
	}
}

func TestDisabledAbsoluteAndWakeRejectBeforeUSBReadiness(t *testing.T) {
	withProfileConfig(t, usbgadget.Devices{Profile: "standard-relative"})
	if err := rpcAbsMouseReport(20, 30, 1); err == nil {
		t.Fatal("absolute report silently accepted")
	}
	if err := rpcWakeHost(); err == nil {
		t.Fatal("wake silently accepted")
	}
}
