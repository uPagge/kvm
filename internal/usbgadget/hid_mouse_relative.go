package usbgadget

import (
	"fmt"
	"os"
	"time"

	"github.com/jetkvm/kvm/internal/receiverreport"
)

var relativeMouseConfig = gadgetConfigItem{
	order:      1002,
	device:     "hid.usb2",
	path:       []string{"functions", "hid.usb2"},
	configPath: []string{"hid.usb2"},
	attrs: gadgetAttributes{
		"protocol":        "2",
		"subclass":        "1",
		"report_length":   "5",
		"no_out_endpoint": "1",
		"wakeup_on_write": "0",
	},
	reportDesc: relativeMouseCombinedReportDesc,
}

// from: https://github.com/NicoHood/HID/blob/b16be57caef4295c6cd382a7e4c64db5073647f7/src/SingleReport/BootMouse.cpp#L26
var relativeMouseCombinedReportDesc = []byte{
	0x05, 0x01, // USAGE_PAGE (Generic Desktop)	  54
	0x09, 0x02, // USAGE (Mouse)
	0xa1, 0x01, // COLLECTION (Application)

	// Pointer and Physical are required by Apple Recovery
	0x09, 0x01, // USAGE (Pointer)
	0xa1, 0x00, // COLLECTION (Physical)

	// 8 Buttons
	0x05, 0x09, // USAGE_PAGE (Button)
	0x19, 0x01, // USAGE_MINIMUM (Button 1)
	0x29, 0x08, // USAGE_MAXIMUM (Button 8)
	0x15, 0x00, // LOGICAL_MINIMUM (0)
	0x25, 0x01, // LOGICAL_MAXIMUM (1)
	0x95, 0x08, // REPORT_COUNT (8)
	0x75, 0x01, // REPORT_SIZE (1)
	0x81, 0x02, // INPUT (Data,Var,Abs)

	// X, Y, Wheel
	0x05, 0x01, // USAGE_PAGE (Generic Desktop)
	0x09, 0x30, // USAGE (X)
	0x09, 0x31, // USAGE (Y)
	0x09, 0x38, // USAGE (Wheel)
	0x15, 0x81, // LOGICAL_MINIMUM (-127)
	0x25, 0x7f, // LOGICAL_MAXIMUM (127)
	0x75, 0x08, // REPORT_SIZE (8)
	0x95, 0x03, // REPORT_COUNT (3)
	0x81, 0x06, // INPUT (Data,Var,Rel)

	// Horizontal Scroll
	0x05, 0x0C, //   USAGE_PAGE (Consumer)
	0x0A, 0x38, 0x02, // USAGE (AC Pan)
	0x15, 0x81, //   LOGICAL_MINIMUM (-127)
	0x25, 0x7f, //   LOGICAL_MAXIMUM (127)
	0x75, 0x08, //   REPORT_SIZE (8)
	0x95, 0x01, //   REPORT_COUNT (1)
	0x81, 0x06, //   INPUT (Data,Var,Rel)

	// End
	0xc0, //       End Collection (Physical)
	0xc0, //       End Collection
}

// Four-byte, report-only mouse: three buttons, padding, relative X/Y/wheel.
var standardRelativeMouseReportDesc = []byte{
	0x05, 0x01, 0x09, 0x02, 0xa1, 0x01,
	0x09, 0x01, 0xa1, 0x00,
	0x05, 0x09, 0x19, 0x01, 0x29, 0x03,
	0x15, 0x00, 0x25, 0x01, 0x95, 0x03, 0x75, 0x01, 0x81, 0x02,
	0x95, 0x01, 0x75, 0x05, 0x81, 0x03,
	0x05, 0x01, 0x09, 0x30, 0x09, 0x31, 0x09, 0x38,
	0x15, 0x81, 0x25, 0x7f, 0x75, 0x08, 0x95, 0x03, 0x81, 0x06,
	0xc0, 0xc0,
}

func (u *UsbGadget) relMouseWriteHidFile(data []byte) error {
	if u.relMouseHidFile == nil {
		devicePath, err := u.hidDevicePath("relative_mouse", "/dev/hidg2")
		if err != nil {
			return err
		}
		u.relMouseHidFile, err = u.openWithTimeout(devicePath, os.O_RDWR, 0666, 3*time.Second)
		if err != nil {
			return fmt.Errorf("failed to open %s: %w", devicePath, err)
		}
	}

	_, err := u.writeWithTimeout(u.relMouseHidFile, data)
	if err != nil {
		u.logWithSuppression("relMouseWriteHidFile", 100, u.log, err, "failed to write relative mouse report")
		u.relMouseHidFile.Close()
		u.relMouseHidFile = nil
		return err
	}
	u.resetLogSuppressionCounter("relMouseWriteHidFile")
	return nil
}

func (u *UsbGadget) RelMouseReport(mx int8, my int8, buttons uint8) error {
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()

	if !u.enabledDevices.RelativeMouse {
		return nil
	}

	u.relMouseLock.Lock()
	defer u.relMouseLock.Unlock()

	if u.standardRelativeProfile() && (mx == -128 || my == -128 || buttons&^uint8(7) != 0) {
		return fmt.Errorf("relative mouse input outside profile range or button mask")
	}
	report := []byte{buttons, byte(mx), byte(my), 0, 0}
	if u.standardRelativeProfile() {
		report = report[:4]
	}
	if u.receiverEnumerationProfile() {
		encoded, err := receiverreport.EncodeMouse(mx, my, buttons, 0, 0)
		if err != nil {
			return err
		}
		report = encoded[:]
	}
	err := u.relMouseWriteHidFile(report)
	if err != nil {
		return err
	}
	u.relMouseButtons = buttons

	u.resetUserInputTime()
	return nil
}

func (u *UsbGadget) RelMouseWheelReport(wheelY int8, wheelX int8) error {
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()

	if !u.enabledDevices.RelativeMouse {
		return nil
	}

	u.relMouseLock.Lock()
	defer u.relMouseLock.Unlock()
	if u.standardRelativeProfile() && (wheelY == -128 || wheelX != 0) {
		return fmt.Errorf("relative mouse wheel outside profile range or unsupported horizontal scroll")
	}

	if wheelY == 0 && wheelX == 0 {
		return nil
	}

	report := []byte{u.relMouseButtons, 0, 0, byte(wheelY), byte(wheelX)}
	if u.standardRelativeProfile() {
		report = report[:4]
	}
	if u.receiverEnumerationProfile() {
		encoded, err := receiverreport.EncodeMouse(0, 0, u.relMouseButtons, wheelY, wheelX)
		if err != nil {
			return err
		}
		report = encoded[:]
	}
	if err := u.relMouseWriteHidFile(report); err != nil {
		return err
	}

	u.resetUserInputTime()
	return nil
}
