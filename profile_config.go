package kvm

import (
	"encoding/json"
	"fmt"

	"github.com/jetkvm/kvm/internal/usbgadget"
)

func validateUSBProfile(profile usbgadget.HIDProfile) error {
	if profile != "" && !usbgadget.IsRestrictedRelativeProfile(profile) {
		return fmt.Errorf("unsupported HID profile %q", profile)
	}
	return nil
}

// UnmarshalJSON distinguishes absent profile (migration) from an explicitly
// empty legacy recovery profile. Decode into a copy so errors cannot mutate c.
func (c *Config) UnmarshalJSON(raw []byte) error {
	type plainConfig Config
	loaded := plainConfig(*c)
	// A default merged into an older file is not explicit audio consent.
	loaded.StandardRelativeAudioConfigVersion = 0
	var payload struct {
		USBDevices map[string]json.RawMessage `json:"usb_devices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if profileRaw, exists := payload.USBDevices["hid_profile"]; exists {
		var profile usbgadget.HIDProfile
		if string(profileRaw) == "null" {
			return fmt.Errorf("hid_profile must be a string")
		}
		if err := json.Unmarshal(profileRaw, &profile); err != nil {
			return err
		}
		if err := validateUSBProfile(profile); err != nil {
			return err
		}
		legacyDefaults := usbgadget.Devices{Keyboard: true, AbsoluteMouse: true, RelativeMouse: true, MassStorage: true, Audio: true}
		loaded.UsbDevices = &legacyDefaults
	} else {
		devices := defaultUsbDevices
		loaded.UsbDevices = &devices
	}
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return err
	}
	if loaded.UsbDevices == nil {
		devices := defaultUsbDevices
		loaded.UsbDevices = &devices
	}
	if _, exists := payload.USBDevices["hid_profile"]; !exists {
		loaded.UsbDevices.Profile = usbgadget.HIDProfileStandardRelative
	}
	if loaded.UsbDevices.Profile == usbgadget.HIDProfileStandardRelative && loaded.StandardRelativeAudioConfigVersion < 1 {
		// Previous standard-relative releases ignored raw audio=true. Preserve
		// their effective off state once, then retain subsequent explicit choices.
		loaded.UsbDevices.Audio = false
		loaded.AudioEnabled = false
		loaded.StandardRelativeAudioConfigVersion = 1
	}
	normalized := usbgadget.NormalizeDevices(*loaded.UsbDevices)
	loaded.UsbDevices = &normalized
	// A persisted upstream setting must never authorize replacing this fork.
	loaded.AutoUpdateEnabled = false
	if !normalized.Audio {
		loaded.AudioEnabled = false
	}
	*c = Config(loaded)
	return nil
}
