package kvm

import (
	"errors"
	"testing"

	"github.com/jetkvm/kvm/internal/native"
)

type edidRPCNative struct {
	native.EmptyNativeInterface
	writes []string
	fail   error
}

func (n *edidRPCNative) VideoSetEDID(value string) error {
	n.writes = append(n.writes, value)
	return n.fail
}

func setupEDIDRPC(t *testing.T, hidden bool) *edidRPCNative {
	t.Helper()
	oldConfig, oldNative, oldAdvertised := config, nativeInstance, hostDisplayAdvertised
	oldSessions := actionSessions
	config = &Config{EdidString: native.DefaultEDID, HideDisplayWhenIdle: hidden}
	actionSessions = 0
	hostDisplayAdvertised = !hidden
	n := &edidRPCNative{}
	nativeInstance = n
	t.Cleanup(func() {
		config, nativeInstance, hostDisplayAdvertised, actionSessions = oldConfig, oldNative, oldAdvertised, oldSessions
	})
	return n
}

func TestMalformedEDIDRejectedWhileDisplayHidden(t *testing.T) {
	n := setupEDIDRPC(t, true)
	for _, value := range []string{"not hex", "00ffffffffffff00", native.DisabledEDID} {
		if err := rpcSetEDID(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
		if config.EdidString != native.DefaultEDID || len(n.writes) != 0 {
			t.Fatalf("invalid input changed config/advertisement: %v", n.writes)
		}
	}
}

func TestEDIDApplyFailureRestoresPreviousAdvertisement(t *testing.T) {
	n := setupEDIDRPC(t, false)
	n.fail = errors.New("ioctl failed")
	if err := rpcSetEDID(""); !errors.Is(err, n.fail) {
		t.Fatalf("apply error lost: %v", err)
	}
	if config.EdidString != native.DefaultEDID || len(n.writes) != 2 {
		t.Fatalf("rollback missing: config=%q writes=%v", config.EdidString, n.writes)
	}
}

func TestEDIDSaveFailureJoinsRollbackError(t *testing.T) {
	n := setupEDIDRPC(t, false)
	saveErr, rollbackErr := errors.New("disk full"), errors.New("rollback ioctl failed")
	err := setEDIDWithSave("", func() error {
		if config.EdidString != "" || len(n.writes) != 1 {
			t.Fatal("save occurred before apply")
		}
		n.fail = rollbackErr
		return saveErr
	})
	if !errors.Is(err, saveErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("lost transaction error: %v", err)
	}
	if config.EdidString != native.DefaultEDID || len(n.writes) != 2 || n.writes[1] != native.DefaultEDID {
		t.Fatalf("previous advertisement not restored: %v", n.writes)
	}
}

func TestEDIDResetAppliesDefaultAndSavesEmptyConfig(t *testing.T) {
	n := setupEDIDRPC(t, false)
	saved := false
	if err := setEDIDWithSave("", func() error { saved = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !saved || config.EdidString != "" || len(n.writes) != 1 || n.writes[0] != native.DefaultEDID {
		t.Fatalf("reset transaction failed: %v", n.writes)
	}
}
