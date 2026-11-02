package usbgadget

import (
	"bytes"
	"reflect"
	"testing"
)

// Catches silently suppressing explicit audio, or exposing extra gadget
// functions / replacing either existing HID interface when audio is selected.
func TestIdentity3OptionalAudioComposition(t *testing.T) {
	u := standardTestGadget(t)
	keyboard := append([]byte(nil), u.configMap["keyboard"].reportDesc...)
	mouse := append([]byte(nil), u.configMap["relative_mouse"].reportDesc...)
	for _, enabled := range []bool{false, true, false} {
		devices := Devices{Profile: HIDProfileStandardRelative, Audio: enabled, AbsoluteMouse: true, MassStorage: true, SerialConsole: true}
		if err := u.SetGadgetDevices(&devices); err != nil {
			t.Fatal(err)
		}
		var functions []string
		for _, item := range u.getOrderedConfigItems() {
			if item.item.device != "" && u.isGadgetConfigItemEnabled(item.key) {
				functions = append(functions, item.key)
			}
		}
		want := []string{"keyboard", "relative_mouse"}
		if enabled {
			want = append(want, "audio")
		}
		if !reflect.DeepEqual(functions, want) {
			t.Fatalf("audio=%v functions=%v, want %v", enabled, functions, want)
		}
		if len(keyboard) != 59 || !bytes.Equal(keyboard, u.configMap["keyboard"].reportDesc) || !bytes.Equal(mouse, u.configMap["relative_mouse"].reportDesc) {
			t.Fatal("audio toggle changed HID descriptors")
		}
		if enabled {
			item := u.configMap["audio"]
			// Preserve capture format and upstream's disabled unimplemented
			// feature controls in both directions (supported by System8).
			if item.device != "uac1.usb0" || !reflect.DeepEqual(item.attrs, gadgetAttributes{
				"c_chmask": "3", "c_srate": "48000", "c_ssize": "2", "p_chmask": "0",
				"c_mute_present": "0", "c_volume_present": "0",
				"p_mute_present": "0", "p_volume_present": "0",
			}) {
				t.Fatalf("wrong USB audio function: %+v", item)
			}
		}
		if u.configMap["base"].configAttrs["bmAttributes"] != "0x80" {
			t.Fatal("audio enabled remote wake")
		}
	}
}
