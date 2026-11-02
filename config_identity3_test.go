package kvm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jetkvm/kvm/internal/edid"
)

// Exact installed first-stage authored profile, SHA256 9480955c...358e146.
// This fixture remains fixed when the current default changes.
const identity3PreviousAuthoredEDID = "00FFFFFFFFFFFF0028B40100000000000024010380341D7802EE91A3544C99260F505400000001010101010101010101010101010101023A801871382D40582C450008222100001E000000FD003B3D43450F000A202020202020000000FC004A65744B564D2031303830700A00000010000000000000000000000000000001FF02030C00419065030C001000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000009A"

func loadIdentity3Config(t *testing.T, fields map[string]any) *Config {
	t.Helper()
	oldConfig, oldError := config, configLoadError
	config, configLoadError = nil, nil
	t.Cleanup(func() { config, configLoadError = oldConfig, oldError })
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity-config.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	loadConfigFromPath(path)
	if configLoadError != nil {
		t.Fatal(configLoadError)
	}
	return config
}

func TestIdentity3MigratesExactPreviousAuthoredEDID(t *testing.T) {
	for _, value := range []string{identity3PreviousAuthoredEDID, strings.ToLower(identity3PreviousAuthoredEDID)} {
		t.Run(value[2:8], func(t *testing.T) {
			loaded := loadIdentity3Config(t, map[string]any{"hdmi_edid_string": value})
			raw, err := hex.DecodeString(loaded.EdidString)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "a3551602ae62804ff5fe52536813b3c5dca02616253bf2a9304122538c5b7976" {
				t.Fatalf("saved default not migrated: SHA256=%s", got)
			}
		})
	}
}

func TestIdentity3PreservesCustomEDIDAndUSBIdentity(t *testing.T) {
	// A one-byte identity difference, with a valid checksum, must remain custom.
	custom := strings.Replace(strings.Replace(identity3PreviousAuthoredEDID, "28B40100", "29B40100", 1), "01FF0203", "01FE0203", 1)
	if err := edid.ValidateHex(custom); err != nil {
		t.Fatal(err)
	}
	loaded := loadIdentity3Config(t, map[string]any{
		"hdmi_edid_string": custom,
		"usb_config":       map[string]string{"vendor_id": "0x1234", "product_id": "0x5678", "manufacturer": "My manufacturer", "product": "My product", "serial_number": "custom-serial"},
	})
	if loaded.EdidString != custom {
		t.Fatal("custom EDID overwritten")
	}
	if c := loaded.UsbConfig; c.VendorId != "0x1234" || c.ProductId != "0x5678" || c.Manufacturer != "My manufacturer" || c.Product != "My product" || c.SerialNumber != "custom-serial" {
		t.Fatalf("custom USB identity overwritten: %+v", c)
	}
}

func TestIdentity3NeutralUSBDefaults(t *testing.T) {
	c := getDefaultConfig().UsbConfig
	if c.Manufacturer != "" || c.Product != "USB Input Device" || c.SerialNumber != "" {
		t.Fatalf("revealing USB defaults: %+v", c)
	}
	if c.VendorId != "0x1d6b" || c.ProductId != "0x0104" {
		t.Fatalf("USB VID/PID changed: %+v", c)
	}
}
