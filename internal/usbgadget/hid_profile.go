package usbgadget

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jetkvm/kvm/internal/receiverreport"
)

// NormalizeDevices resolves the supported profile into its complete composition.
// An empty profile retains the legacy device switches for recovery.
func NormalizeDevices(devices Devices) Devices {
	if devices.Profile == HIDProfileReceiverEnumeration {
		return Devices{Profile: devices.Profile, Keyboard: true, RelativeMouse: true}
	}
	if devices.Profile == HIDProfileStandardRelative {
		return Devices{Profile: devices.Profile, Keyboard: true, RelativeMouse: true, Audio: devices.Audio}
	}
	return devices
}

func validateHIDProfile(profile HIDProfile) error {
	if profile != "" && !IsRestrictedRelativeProfile(profile) {
		return fmt.Errorf("unsupported HID profile %q", profile)
	}
	return nil
}

// GetGadgetDevices returns the normalized composition used by the gadget.
func (u *UsbGadget) GetGadgetDevices() Devices {
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()
	return u.enabledDevices
}

// IsRestrictedRelativeProfile identifies compositions with the measured keyboard
// and relative pointer, without legacy wake, absolute, serial or storage functions.
func IsRestrictedRelativeProfile(profile HIDProfile) bool {
	return profile == HIDProfileStandardRelative || profile == HIDProfileReceiverEnumeration
}

func (u *UsbGadget) restrictedRelativeProfile() bool {
	return IsRestrictedRelativeProfile(u.enabledDevices.Profile)
}

func (u *UsbGadget) receiverEnumerationProfile() bool {
	return u.enabledDevices.Profile == HIDProfileReceiverEnumeration
}

func (u *UsbGadget) standardRelativeProfile() bool {
	return u.enabledDevices.Profile == HIDProfileStandardRelative
}

func (u *UsbGadget) loadHIDProfile() {
	defer u.loadIdentityPolicy()
	// Restore only profile-owned attributes; preserve custom identity and other
	// caller overrides. A profile can be changed back to legacy in one process.
	for key, attrs := range map[string][]string{
		"keyboard":       {"protocol", "subclass", "no_out_endpoint"},
		"relative_mouse": {"protocol", "subclass", "report_length"},
	} {
		item, ok := u.configMap[key]
		if !ok {
			continue
		}
		// Explicit profiles require their kernel contracts; reset profile-owned kernel options on every load.
		item.attrs["no_interface_string"] = "0"
		item.attrs["fs_interval"] = "0"
		item.attrs["report_contract"] = "0"
		for _, attr := range attrs {
			item.attrs[attr] = defaultGadgetConfig[key].attrs[attr]
		}
		if key == "relative_mouse" {
			item.reportDesc = relativeMouseCombinedReportDesc
		}
		if key == "keyboard" {
			item.reportDesc = keyboardReportDesc
		}
		if u.restrictedRelativeProfile() {
			item.attrs["no_interface_string"] = "1"
			item.attrs["subclass"] = "1"
			if key == "keyboard" {
				item.attrs["fs_interval"] = "8"
				item.attrs["protocol"], item.attrs["report_contract"] = "1", "1"
				item.attrs["no_out_endpoint"] = "1"
				item.reportDesc = measuredMK275KeyboardReportDesc
			}
			if key == "relative_mouse" {
				item.attrs["fs_interval"] = "2"
				item.attrs["protocol"], item.attrs["report_contract"] = "2", "2"
				item.attrs["report_length"] = "4"
				item.reportDesc = standardRelativeMouseReportDesc
				if u.receiverEnumerationProfile() {
					item.attrs["report_contract"] = "3"
					item.attrs["report_length"] = "20"
					item.attrs["no_out_endpoint"] = "1"
					item.reportDesc = receiverreport.Descriptor()
				}
			}
		}
		u.configMap[key] = item
	}
	if base, ok := u.configMap["base"]; ok && base.configAttrs != nil {
		base.configAttrs["bmAttributes"] = defaultGadgetConfig["base"].configAttrs["bmAttributes"]
		if u.restrictedRelativeProfile() {
			base.configAttrs["bmAttributes"] = "0x80"
		}
	}
}

// hidDevicePath uses f_hid's allocated minor, not the interface/link order.
// Function instances can survive disabled links and retain noncontiguous minors.
// Standard profile fails closed if the kernel does not expose its dev attribute.
// Legacy kernels keep the original fallback paths for recovery.
func (u *UsbGadget) hidDevicePath(key, legacyPath string) (string, error) {
	item, ok := u.configMap[key]
	if !ok {
		if !u.restrictedRelativeProfile() {
			return legacyPath, nil
		}
		return "", fmt.Errorf("HID function %s is missing", key)
	}
	devPath := filepath.Join(joinPath(u.kvmGadgetPath, item.path), "dev")
	data, err := os.ReadFile(devPath)
	if err != nil {
		if !u.restrictedRelativeProfile() && os.IsNotExist(err) {
			return legacyPath, nil
		}
		return "", fmt.Errorf("read %s HID device number: %w", key, err)
	}
	majorText, minorText, ok := strings.Cut(strings.TrimSpace(string(data)), ":")
	major, majorErr := strconv.ParseUint(majorText, 10, 32)
	minor, minorErr := strconv.ParseUint(minorText, 10, 32)
	if !ok || majorErr != nil || minorErr != nil || major == 0 {
		return "", fmt.Errorf("invalid %s HID device number %q", key, strings.TrimSpace(string(data)))
	}
	return fmt.Sprintf("/dev/hidg%d", minor), nil
}

func disabledHIDFunction(name string) error {
	return fmt.Errorf("%s is disabled by the HID profile", name)
}
