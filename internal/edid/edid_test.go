package edid

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Public upstream default at 36c008f. Its checksums are valid, but the
// 1280x720 DTD is 119.912 Hz while the range descriptor allows only 23..76 Hz.
const contradictoryBaseline = "00FFFFFFFFFFFF0028B4010001EEFFC0302301038047287856EE91A3544C99260F5054000000D1C081C0318001010101010101010101023A801871382D40582C4500C48E2100001E773300A050D02B2030203500122C2100001A000000FD00174C0F5111000A202020202020000000FC004A65744B564D2076310A20202001D5020322D1431004012309070783010000E200CFE40D100401E305000065030C001000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000CF"

// Independent fixture: 1080p60, no established/standard modes, 59..61 Hz,
// 67..69 kHz, 150 MHz limit, CTA VIC16 and HDMI vendor block, no audio.
var validVideo = "00FFFFFFFFFFFF0028B40100000000000024010380341D78020000000000000000000000000001010101010101010101010101010101023A801871382D40582C450008221100001E000000FD003B3D43450F000A202020202020000000FC004A65744B564D2031303830700A0000001000000000000000000000000000000143" + "02030C00419065030C001000" + strings.Repeat("00", 115) + "9A"

func fixture(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Recomputing test fixture checksums isolates semantic errors from transport
// corruption; it is never used by the production validator or setter.
func checksummed(b []byte) string {
	for offset := 0; offset < len(b); offset += 128 {
		b[offset+127] = 0
		var sum byte
		for _, value := range b[offset : offset+127] {
			sum += value
		}
		b[offset+127] = -sum
	}
	return hex.EncodeToString(b)
}

// Removing any corresponding rejection should make its subtest fail even
// though all semantic fixtures retain valid block checksums.
func TestValidateHexRejectsMalformedEDID(t *testing.T) {
	cases := []struct {
		name   string
		change func([]byte)
	}{
		{"header", func(b []byte) { b[0] = 1 }},
		{"extension count", func(b []byte) { b[126] = 0 }},
		{"zero horizontal active", func(b []byte) { b[56], b[58] = 0, b[58]&15 }},
		{"sync outside blanking", func(b []byte) { b[62] = 255 }},
		{"invalid descriptor header", func(b []byte) { b[92] = 1 }},
		{"CTA offset below header", func(b []byte) { b[130] = 3 }},
		{"CTA offset includes checksum", func(b []byte) { b[130] = 128 }},
		{"CTA block overruns collection", func(b []byte) { b[132] = 0x5f }},
		{"CTA audio descriptor length", func(b []byte) { b[132] = 0x21 }},
		{"CTA zero VIC", func(b []byte) { b[133] = 0 }},
		{"CTA DTD crosses checksum", func(b []byte) { b[130] = 126; b[254] = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fixture(t, validVideo)
			tc.change(b)
			if err := ValidateHex(checksummed(b)); err == nil {
				t.Fatal("malformed EDID accepted")
			}
		})
	}
	for _, value := range []string{"", "00", validVideo[:255], validVideo + "00", " " + validVideo, strings.Replace(validVideo, "FF", "GG", 1)} {
		if err := ValidateHex(value); err == nil {
			t.Errorf("accepted malformed hex/length (%d characters)", len(value))
		}
	}
	b := fixture(t, validVideo)
	b[20] ^= 1
	if err := ValidateHex(hex.EncodeToString(b)); err == nil {
		t.Error("bad base checksum accepted")
	}
	b = fixture(t, validVideo)
	b[140] ^= 1
	if err := ValidateHex(hex.EncodeToString(b)); err == nil {
		t.Error("bad extension checksum accepted")
	}
}

func TestValidateSemanticsRejectsBaselineHighRefreshDTD(t *testing.T) {
	b := fixture(t, contradictoryBaseline)
	// Isolate the range mismatch from the independent native-DTD-count issue;
	// otherwise that earlier rejection could mask a missing range check.
	b[131] &= 0xf0
	if err := ValidateSemantics(checksummed(b)); err == nil || !strings.Contains(err.Error(), "119.912 Hz") {
		t.Fatalf("expected the independently decoded 119.912 Hz range contradiction, got %v", err)
	}
}

func TestValidateSemanticsRejectsContradictoryRanges(t *testing.T) {
	for _, change := range []func([]byte){
		func(b []byte) { b[77], b[78] = 70, 76 },
		func(b []byte) { b[79], b[80] = 70, 80 },
		func(b []byte) { b[81] = 10 },
		func(b []byte) { b[77], b[78] = 61, 59 },
		func(b []byte) { b[133] = 4 },
		func(b []byte) { b[131] |= 1 },
	} {
		b := fixture(t, validVideo)
		change(b)
		if err := ValidateSemantics(checksummed(b)); err == nil {
			t.Error("contradiction accepted")
		}
	}
}

func TestValidateHexAcceptsCustomProfiles(t *testing.T) {
	if err := ValidateHex(strings.ToLower(validVideo)); err != nil {
		t.Fatal(err)
	}
	b := fixture(t, validVideo)[:128]
	b[126] = 0
	if err := ValidateHex(checksummed(b)); err != nil {
		t.Fatalf("base-only: %v", err)
	}
	// Keep the old structurally valid high-refresh/audio EDID usable as custom
	// input/rollback; strict authored-profile assumptions are a separate check.
	b = fixture(t, contradictoryBaseline)
	if err := ValidateHex(checksummed(b)); err != nil {
		t.Fatalf("custom high-refresh/audio profile: %v", err)
	}
}

// Base dimensions are centimetres; DTD dimensions are millimetres with the
// high nibbles packed together. A wrong packed nibble must fail independently.
func TestStandard1080p60PhysicalDimensions(t *testing.T) {
	b := fixture(t, Standard1080p60)
	baseWidth, baseHeight := int(b[21])*10, int(b[22])*10
	if baseWidth != 600 || baseHeight != 330 {
		t.Errorf("base physical dimensions = %dx%d mm, want 600x330 mm", baseWidth, baseHeight)
	}
	d := b[54:72]
	dtdWidth := int(d[12]) | int(d[14]>>4)<<8
	dtdHeight := int(d[13]) | int(d[14]&15)<<8
	if dtdWidth != 600 || dtdHeight != 330 {
		t.Errorf("DTD physical dimensions = %dx%d mm, want 600x330 mm", dtdWidth, dtdHeight)
	}
}

// Decode independently rather than compare the profile to its own constant.
// Restoring the revealing JET/JetKVM name or changing measured identity fails.
func TestIdentity3MeasuredDisplayIdentity(t *testing.T) {
	b := fixture(t, Standard1080p60)
	id := int(b[8])<<8 | int(b[9])
	manufacturer := string([]byte{byte((id>>10)&31) + 'A' - 1, byte((id>>5)&31) + 'A' - 1, byte(id&31) + 'A' - 1})
	product := int(b[10]) | int(b[11])<<8
	serial := uint32(b[12]) | uint32(b[13])<<8 | uint32(b[14])<<16 | uint32(b[15])<<24
	if manufacturer != "SAC" || product != 0x2750 || serial != 0 || b[16] != 30 || int(b[17])+1990 != 2024 {
		t.Fatalf("identity = %s/%04x serial=%d week=%d year=%d", manufacturer, product, serial, b[16], int(b[17])+1990)
	}
	if b[93] != 0xfc || strings.TrimSpace(string(b[95:108])) != "LED MONITOR" {
		t.Fatalf("display name = %q", b[90:108])
	}
	if strings.Contains(strings.ToUpper(string(b)), "JETKVM") || manufacturer == "JET" {
		t.Fatal("revealing JetKVM identity advertised")
	}
}

// Independently decode the public profile: accidentally retaining an extra
// mode/audio block, wrong timing or a contradictory range must fail here.
func TestStandard1080p60AdvertisesOnlyVideo1080p60(t *testing.T) {
	if err := ValidateSemantics(Standard1080p60); err != nil {
		t.Fatal(err)
	}
	b := fixture(t, Standard1080p60)
	if len(b) != 256 || b[126] != 1 {
		t.Fatal("expected one CTA extension")
	}
	// An authored EDID needs plausible chromaticity; zero coordinates would
	// describe no usable RGB primaries even though transport checksums pass.
	for i, want := range []float64{0.64, 0.33, 0.30, 0.60, 0.15, 0.06, 0.3127, 0.329} {
		lowByte := b[25+i/4]
		lowBits := (lowByte >> uint(6-2*(i%4))) & 3
		coordinate := float64(int(b[27+i])<<2|int(lowBits)) / 1024
		if coordinate < want-0.003 || coordinate > want+0.003 {
			t.Errorf("RGB/white coordinate %d = %.4f, want approximately %.4f", i, coordinate, want)
		}
	}
	if b[35] != 0 || b[36] != 0 || b[37]&0x80 != 0 {
		t.Error("unexpected established timings")
	}
	for i := 38; i < 54; i += 2 {
		if b[i] != 1 || b[i+1] != 1 {
			t.Errorf("unexpected standard timing at %d", i)
		}
	}
	d := b[54:72]
	h := int(d[2]) + int(d[4]&0xf0)<<4
	v := int(d[5]) + int(d[7]&0xf0)<<4
	htotal := h + int(d[3]) + int(d[4]&15)<<8
	vtotal := v + int(d[6]) + int(d[7]&15)<<8
	clock := (int(d[0]) + int(d[1])<<8) * 10000
	if h != 1920 || v != 1080 || clock != 148500000 || htotal != 2200 || vtotal != 1125 || d[17]&0x80 != 0 {
		t.Fatalf("unexpected preferred timing: %dx%d clock=%d totals=%dx%d flags=%x", h, v, clock, htotal, vtotal, d[17])
	}
	for i := 72; i < 126; i += 18 {
		if b[i] != 0 || b[i+1] != 0 {
			t.Errorf("unexpected extra DTD at %d", i)
		}
	}
	if b[77] != 59 || b[78] != 61 || b[79] != 67 || b[80] != 69 || b[81] != 15 {
		t.Error("unexpected range limits")
	}
	c := b[128:]
	if c[0] != 2 || c[3]&0x40 != 0 {
		t.Fatal("CTA must be video-only")
	}
	var vics []byte
	for i := 4; i < int(c[2]); {
		tag, size := c[i]>>5, int(c[i]&31)
		if i+1+size > int(c[2]) {
			t.Fatal("CTA block overrun")
		}
		if tag == 1 || tag == 4 {
			t.Error("audio/speaker block advertised")
		}
		if tag == 2 {
			for _, vic := range c[i+1 : i+1+size] {
				vics = append(vics, vic&127)
			}
		}
		i += size + 1
	}
	if len(vics) != 1 || vics[0] != 16 {
		t.Fatalf("unexpected CTA modes %v", vics)
	}
	for _, value := range c[int(c[2]):127] {
		if value != 0 {
			t.Fatal("unexpected extra CTA DTD/data")
		}
	}
}
