package receivercontrol

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
)

// Synthetic fixture validates mechanics only; it is not physical evidence.
func syntheticReference() *Reference {
	r := &Reference{SchemaVersion: 1, Status: "measured", Source: "synthetic test fixture", InitialNotificationFlagsHex: "000000"}
	for i := byte(1); i <= 2; i++ {
		s := Slot{Slot: i, WirelessPID: 0x4000 + uint16(i), RFProtocol: 0x0a, RawConnectionFlags: 0x40 | i}
		for n, dst := range []*string{&s.BasicReplyHex, &s.ExtendedReplyHex, &s.NameReplyHex} {
			b := make([]byte, 20)
			copy(b, []byte{0x11, 0xff, 0x83, 0xb5, byte(0x20+0x10*n) + i - 1})
			*dst = hex.EncodeToString(b)
		}
		s.ProtocolPingReplyHex = fmt.Sprintf("10%02x001102005a", i)
		r.Slots = append(r.Slots, s)
	}
	return r
}
func TestPendingFailsClosed(t *testing.T) {
	if _, err := Load([]byte(`{"schema_version":1,"status":"pending"}`)); err == nil {
		t.Fatal("pending accepted")
	}
}
func TestRootStartupAndPerInstanceFlags(t *testing.T) {
	h, err := New(syntheticReference())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := New(syntheticReference())
	handle := func(req, want string) {
		t.Helper()
		b, _ := hex.DecodeString(req)
		before := append([]byte(nil), b...)
		replies, err := h.Handle(b)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, b) {
			t.Fatal("mutated request")
		}
		var got string
		for _, r := range replies {
			got += hex.EncodeToString(r) + " "
		}
		if got != want {
			t.Fatalf("%s => %q want %q", req, got, want)
		}
	}
	handle("10ff8100000000", "10ff8100000000 ")
	handle("10ff8002020000", "10ff8002000000 ")
	handle("10ff8000000900", "10ff8000000000 ")
	handle("10ff8100000000", "10ff8100000900 ")
	handle("10ff8002020000", "10ff8002000000 1001410a110140 1002410a120240 ")
	got, _ := other.Handle([]byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
	if got[0][5] != 0 {
		t.Fatal("shared flags")
	}
	handle("10ff8000000100", "10ff8000000000 ")
	handle("10ff8002020000", "10ff8002000000 1001410a010140 1002410a020240 ")
	h.Reset()
	handle("10ff8100000000", "10ff8100000000 ")
}
func TestMeasuredRepliesPingEchoAndValidation(t *testing.T) {
	ref := syntheticReference()
	h, _ := New(ref)
	// Reference ownership and returned frames are independent.
	ref.Slots[0].ProtocolPingReplyHex = ""
	for _, index := range []byte{0x20, 0x21, 0x30, 0x31, 0x40, 0x41} {
		got, err := h.Handle([]byte{0x10, 0xff, 0x83, 0xb5, index, 0, 0})
		if err != nil || len(got) != 1 || len(got[0]) != 20 || got[0][4] != index {
			t.Fatalf("B5/%x: %x %v", index, got, err)
		}
		got[0][4] = 0
	}
	for _, id := range []byte{1, 2} {
		for sw := byte(1); sw < 16; sw++ {
			got, err := h.Handle([]byte{0x10, id, 0, 0x10 | sw, 0, 0, 0xab})
			if err != nil || len(got) != 1 || got[0][3] != 0x10|sw || got[0][6] != 0xab {
				t.Fatalf("ping echo %x %v", got, err)
			}
		}
	}
	for _, bad := range [][]byte{nil, {2, 0, 0, 0, 0, 0, 0, 0}, {0x10, 1}, {0x11, 1, 0, 0x11, 0, 0, 0}} {
		if _, err := h.Handle(bad); err == nil {
			t.Fatalf("invalid frame accepted %x", bad)
		}
	}
	if got, err := h.Handle([]byte{0x10, 0xff, 0x80, 0xb2, 0, 0, 0}); err != nil || len(got) != 0 {
		t.Fatal("implemented pairing unexpectedly")
	}
}
func TestConcurrentFlags(t *testing.T) {
	h, _ := New(syntheticReference())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				h.Handle([]byte{0x10, 0xff, 0x80, 0, 0, 9, 0})
				h.Handle([]byte{0x10, 0xff, 0x80, 2, 2, 0, 0})
				h.Reset()
			}
		}()
	}
	wg.Wait()
}

func TestMeasuredLegacyProtocolErrorNotOffline(t *testing.T) {
	r := syntheticReference()
	r.Slots[0].ProtocolPingReplyHex = "10018f00110100"
	h, err := New(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Handle([]byte{0x10, 1, 0, 0x1e, 0, 0, 0xab})
	want := []byte{0x10, 1, 0x8f, 0, 0x1e, 1, 0}
	if err != nil || len(got) != 1 || !bytes.Equal(got[0], want) {
		t.Fatalf("legacy ping %x %v", got, err)
	}
	for _, code := range []byte{8, 9} {
		r.Slots[0].ProtocolPingReplyHex = fmt.Sprintf("10018f0011%02x00", code)
		if _, err := New(r); err == nil {
			t.Fatalf("offline error %d accepted as version", code)
		}
	}
}

func TestMeasuredSetR00AckIsNotFlagsEcho(t *testing.T) {
	h, _ := New(syntheticReference())
	got, err := h.Handle([]byte{0x10, 0xff, 0x80, 0, 0, 9, 0})
	want := []byte{0x10, 0xff, 0x80, 0, 0, 0, 0}
	if err != nil || len(got) != 1 || !bytes.Equal(got[0], want) {
		t.Fatalf("SetR00 ACK=%x err=%v want %x", got, err, want)
	}
	flags, _ := h.Handle([]byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
	if flags[0][5] != 9 {
		t.Fatal("ACK payload reset live flags")
	}
}

func TestMeasuredUnavailableProtocolChannel(t *testing.T) {
	r := syntheticReference()
	r.ProtocolQueryNotificationFlagsHex = "000900"
	for i := range r.Slots {
		r.Slots[i].ProtocolPingReplyHex = fmt.Sprintf("10%02x8f00110800", i+1)
	}
	h, err := New(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []byte{1, 2} {
		got, err := h.Handle([]byte{0x10, slot, 0, 0x1f, 0, 0, 0xab})
		if err != nil || len(got) != 1 || !bytes.Equal(got[0], []byte{0x10, slot, 0x8f, 0, 0x1f, 8, 0}) {
			t.Fatalf("unavailable reply %x %v", got, err)
		}
	}
	r.ProtocolQueryNotificationFlagsHex = ""
	if _, err := New(r); err == nil {
		t.Fatal("error8 missing measured context accepted")
	}
	r.ProtocolQueryNotificationFlagsHex = "000900"
	r.Slots[0].ProtocolPingReplyHex = "10018f00110900"
	if _, err := New(r); err == nil {
		t.Fatal("unmeasured error9 accepted")
	}
}

// Exercise the shipped measured data independently of synthetic mechanism tests.
func TestEmbeddedMeasuredReferenceReadyAndNegativeProtocol(t *testing.T) {
	ref, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []byte{1, 2} {
		replies, err := h.Handle([]byte{0x10, slot, 0, 0x11, 0, 0, 0x5a})
		want := []byte{0x10, slot, 0x8f, 0, 0x11, 8, 0}
		if err != nil || len(replies) != 1 || !bytes.Equal(replies[0], want) {
			t.Fatalf("measured slot%d response=%x err=%v", slot, replies, err)
		}
	}
	replies, err := h.Handle([]byte{0x10, 0xff, 0x83, 0xb5, 0x40, 0, 0})
	if err != nil || len(replies) != 1 || replies[0][5] != 1 || replies[0][6] != ' ' {
		t.Fatal("measured one-space keyboard name was altered")
	}
}
