package receiverreport

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestEncodeMouseGolden(t *testing.T) {
	got, err := EncodeMouse(1, -1, 0x81, -127, 127)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]byte{0x02, 0x81, 0x00, 0x01, 0xf0, 0xff, 0x81, 0x7f}
	if got != want {
		t.Fatalf("report = % x, want % x", got, want)
	}
}

func TestEncodeMouseSignedCoordinates(t *testing.T) {
	// Decode the on-wire 12-bit two's-complement fields independently of the
	// encoder, including every pair accepted by the existing int8 input API.
	signed12 := func(n int) int {
		if n >= 2048 {
			return n - 4096
		}
		return n
	}
	for x := -128; x <= 127; x++ {
		for y := -128; y <= 127; y++ {
			got, err := EncodeMouse(int8(x), int8(y), 0xff, 0, 0)
			if err != nil {
				t.Fatalf("x=%d y=%d: %v", x, y, err)
			}
			dx := signed12(int(got[3]) + int(got[4]&0x0f)*256)
			dy := signed12(int(got[4]>>4) + int(got[5])*16)
			if dx != x || dy != y || got[0] != 2 || got[1] != 0xff || got[2] != 0 || got[6] != 0 || got[7] != 0 {
				t.Fatalf("x=%d y=%d: report=% x decoded=(%d,%d)", x, y, got, dx, dy)
			}
		}
	}
}

func TestEncodeMouseWheelPanDomain(t *testing.T) {
	for value := -127; value <= 127; value++ {
		got, err := EncodeMouse(0, 0, 0, int8(value), int8(-value))
		if err != nil {
			t.Fatalf("wheel=%d: %v", value, err)
		}
		if got[6] != byte(int8(value)) || got[7] != byte(int8(-value)) {
			t.Fatalf("wheel=%d: report=% x", value, got)
		}
	}
	for _, input := range [][2]int8{{-128, 0}, {0, -128}, {-128, -128}} {
		got, err := EncodeMouse(0, 0, 0, input[0], input[1])
		if err == nil {
			t.Errorf("wheel/pan=%v accepted: % x", input, got)
		}
		if got != [8]byte{} {
			t.Errorf("wheel/pan=%v returned a usable partial report: % x", input, got)
		}
	}
}

func TestDescriptorReferenceAndIsolation(t *testing.T) {
	const wantSHA256 = "cb1dac598eb6e18ab0e67086660d0dd0ce9234ad4203b85862e393df809731d5"
	first := Descriptor()
	if len(first) != 177 {
		t.Fatalf("descriptor length = %d, want 177", len(first))
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(first)); got != wantSHA256 {
		t.Fatalf("descriptor hash = %s, want %s", got, wantSHA256)
	}
	if DescriptorSHA256 != wantSHA256 {
		t.Fatalf("published hash = %s, want %s", DescriptorSHA256, wantSHA256)
	}
	snapshot := append([]byte(nil), first...)
	for i := range first {
		first[i] ^= 0xff
	}
	if next := Descriptor(); !bytes.Equal(next, snapshot) {
		t.Fatal("caller mutation changed the reference descriptor")
	}
}
