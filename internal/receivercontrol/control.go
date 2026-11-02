// Package receivercontrol implements only measured receiver startup exchanges.
// Virtual peripherals are always connected; raw physical flags remain in Reference.
package receivercontrol

import (
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed reference.json
var referenceFS embed.FS

type Slot struct {
	Slot                 byte   `json:"slot"`
	WirelessPID          uint16 `json:"wireless_pid"`
	RFProtocol           byte   `json:"rf_protocol"`
	RawConnectionFlags   byte   `json:"raw_connection_flags"`
	BasicReplyHex        string `json:"basic_reply_hex"`
	ExtendedReplyHex     string `json:"extended_reply_hex"`
	NameReplyHex         string `json:"name_reply_hex"`
	ProtocolPingReplyHex string `json:"protocol_ping_reply_hex"`
}
type Reference struct {
	ProtocolQueryNotificationFlagsHex string `json:"protocol_query_notification_flags_hex"`
	SchemaVersion                     int    `json:"schema_version"`
	Status                            string `json:"status"`
	Source                            string `json:"source"`
	InitialNotificationFlagsHex       string `json:"initial_notification_flags_hex"`
	ConnectionCountReplyHex           string `json:"connection_count_reply_hex"`
	Slots                             []Slot `json:"slots"`
}

func Load(data []byte) (*Reference, error) {
	var r Reference
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if err := r.Ready(); err != nil {
		return nil, err
	}
	return &r, nil
}
func LoadEmbedded() (*Reference, error) {
	b, err := referenceFS.ReadFile("reference.json")
	if err != nil {
		return nil, err
	}
	return Load(b)
}
func frame(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if !validFrame(b) {
		return nil, fmt.Errorf("invalid complete vendor frame")
	}
	return b, nil
}
func validFrame(b []byte) bool { return len(b) == 7 && b[0] == 0x10 || len(b) == 20 && b[0] == 0x11 }
func (r *Reference) Ready() error {
	if r == nil || r.SchemaVersion != 1 || r.Status != "measured" || r.Source == "" || len(r.Slots) != 2 {
		return fmt.Errorf("receiver startup reference is not ready: measured schema1 with exactly two slots required")
	}
	flags, err := hex.DecodeString(r.InitialNotificationFlagsHex)
	if err != nil || len(flags) != 3 {
		return fmt.Errorf("invalid measured notification flags")
	}
	for i, s := range r.Slots {
		if s.Slot != byte(i+1) || s.WirelessPID == 0 || s.RFProtocol != 0x0a || s.RawConnectionFlags&0xf != byte(i+1) {
			return fmt.Errorf("invalid slot %d physical reference", i+1)
		}
		for n, raw := range []string{s.BasicReplyHex, s.ExtendedReplyHex, s.NameReplyHex} {
			b, err := frame(raw)
			if err != nil || len(b) != 20 || b[1] != 0xff || b[2] != 0x83 || b[3] != 0xb5 || b[4] != byte(0x20+0x10*n+i) {
				return fmt.Errorf("missing or invalid slot%d B5/%x reply", i+1, 0x20+0x10*n+i)
			}
		}
		ping, err := frame(s.ProtocolPingReplyHex)
		if err != nil || ping[1] != s.Slot || !(successfulPing(ping) || legacyPing(ping) || measuredUnavailablePing(ping, r.ProtocolQueryNotificationFlagsHex)) {
			return fmt.Errorf("missing measured slot%d protocol ping (success, legacy invalid-subid1 or measured context9 error8 required)", s.Slot)
		}
	}
	if r.ConnectionCountReplyHex != "" {
		b, err := frame(r.ConnectionCountReplyHex)
		if err != nil || len(b) != 7 || b[1] != 0xff || b[2] != 0x81 || b[3] != 2 {
			return fmt.Errorf("invalid measured connection count reply")
		}
	}
	return nil
}

// Linux recognizes RAP invalid-subid1 for RootGetProtocol as HID++1.
// Transport/offline errors 8/9 do not establish a protocol version.
func legacyPing(b []byte) bool {
	return len(b) == 7 && b[0] == 0x10 && b[2] == 0x8f && b[3] == 0 && b[4]&0xf0 == 0x10 && b[5] == 1
}

// The captured receiver returns RAP error8 even with notifications 000900.
// This is an observed unavailable vendor channel, not an inferred version.
func measuredUnavailablePing(b []byte, notificationFlags string) bool {
	return notificationFlags == "000900" && len(b) == 7 && b[0] == 0x10 && b[2] == 0x8f && b[3] == 0 && b[4]&0xf0 == 0x10 && b[5] == 8 && b[6] == 0
}

func successfulPing(b []byte) bool {
	return b[2] == 0 && b[3]&0xf0 == 0x10 && b[4] > 0
}

type Handler struct {
	mu        sync.Mutex
	reference Reference
	flags     [3]byte
}

func New(r *Reference) (*Handler, error) {
	if err := r.Ready(); err != nil {
		return nil, err
	}
	copyRef := *r
	copyRef.Slots = append([]Slot(nil), r.Slots...)
	h := &Handler{reference: copyRef}
	h.Reset()
	return h, nil
}
func (h *Handler) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, _ := hex.DecodeString(h.reference.InitialNotificationFlagsHex)
	copy(h.flags[:], b)
}

// Handle returns fresh complete vendor frames. Unsupported operations receive no
// fabricated reply. No pairing, unpairing, reset or general HID++ features exist.
func (h *Handler) Handle(request []byte) ([][]byte, error) {
	if !validFrame(request) {
		return nil, fmt.Errorf("invalid receiver control frame")
	}
	b := append([]byte(nil), request...)
	h.mu.Lock()
	defer h.mu.Unlock()
	if b[1] == 0xff {
		if b[0] != 0x10 {
			return nil, nil
		}
		switch {
		case b[2] == 0x81 && b[3] == 0:
			return [][]byte{{0x10, 0xff, 0x81, 0, h.flags[0], h.flags[1], h.flags[2]}}, nil
		case b[2] == 0x80 && b[3] == 0:
			copy(h.flags[:], b[4:7])
			return [][]byte{{0x10, 0xff, 0x80, 0, 0, 0, 0}}, nil
		case b[2] == 0x81 && b[3] == 2 && h.reference.ConnectionCountReplyHex != "":
			reply, _ := frame(h.reference.ConnectionCountReplyHex)
			return [][]byte{reply}, nil
		case b[2] == 0x83 && b[3] == 0xb5:
			for _, s := range h.reference.Slots {
				var raw string
				switch b[4] {
				case 0x20 + s.Slot - 1:
					raw = s.BasicReplyHex
				case 0x30 + s.Slot - 1:
					raw = s.ExtendedReplyHex
				case 0x40 + s.Slot - 1:
					raw = s.NameReplyHex
				}
				if raw != "" {
					reply, _ := frame(raw)
					return [][]byte{reply}, nil
				}
			}
		case b[2] == 0x80 && b[3] == 2 && b[4] == 2:
			if h.flags[1]&1 == 0 {
				return [][]byte{{0x10, 0xff, 0x80, 2, 0, 0, 0}}, nil
			}
			replies := make([][]byte, 0, 3)
			replies = append(replies, []byte{0x10, 0xff, 0x80, 2, 0, 0, 0})
			for _, s := range h.reference.Slots {
				flags := s.RawConnectionFlags &^ byte(0x40|0x10)
				if h.flags[1]&8 != 0 {
					flags |= 0x10
				}
				replies = append(replies, []byte{0x10, s.Slot, 0x41, s.RFProtocol, flags, byte(s.WirelessPID), byte(s.WirelessPID >> 8)})
			}
			return replies, nil
		}
	} else if b[1] >= 1 && b[1] <= 2 && b[2] == 0 && b[3]&0xf0 == 0x10 && b[4] == 0 && b[5] == 0 {
		s := h.reference.Slots[b[1]-1]
		reply, _ := frame(s.ProtocolPingReplyHex)
		if legacyPing(reply) || measuredUnavailablePing(reply, h.reference.ProtocolQueryNotificationFlagsHex) {
			reply[4] = b[3] // RAP error echoes request function/software ID.
		} else {
			reply[3] = b[3]
			reply[6] = b[6] // Only a successful ping carries the nonce.
		}
		return [][]byte{reply}, nil
	}
	return nil, nil
}
