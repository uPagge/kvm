package native

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jetkvm/kvm/internal/edid"
)

// Invalid user data must fail before streaming-status or device calls. On a
// host runner those calls panic, so removing early validation also fails here.
func TestVideoSetEDIDRejectsMalformedBeforeDeviceAccess(t *testing.T) {
	for i, value := range []string{"not hex", strings.Repeat("00", 128), "__other_control_value__"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			n := &Native{}
			if err := n.VideoSetEDID(value); err == nil {
				t.Error("invalid EDID accepted")
			}
		})
	}
}

func TestDefaultEDIDHasCoherentTimings(t *testing.T) {
	if err := edid.ValidateSemantics(DefaultEDID); err != nil {
		t.Fatalf("default EDID is contradictory: %v", err)
	}
}
