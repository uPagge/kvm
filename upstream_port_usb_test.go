package kvm

import (
	"testing"
	"time"
)

// The new upstream enumeration-recovery timer must be postponed before a
// configuration rebind starts, also through App9's transactional audio path.
func TestUpstreamPortConfigUpdateDefersRecoveryBeforeRebind(t *testing.T) {
	setupIdentity3Audio(t, false)
	usbStateLock.Lock()
	old := lastUSBRecoveryTry
	lastUSBRecoveryTry = time.Unix(1, 0)
	usbStateLock.Unlock()
	t.Cleanup(func() { setUSBRecoveryTimer(old) })
	devices := *config.UsbDevices
	devices.Audio = true
	started := time.Now()
	calls := 0
	err := setUsbDevicesWithSaveAndUpdate(devices, func() error { return nil }, func() error {
		calls++
		usbStateLock.Lock()
		postponed := !lastUSBRecoveryTry.Before(started)
		usbStateLock.Unlock()
		if !postponed {
			t.Error("configuration entered rebind before postponing USB recovery")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("update err=%v calls=%d", err, calls)
	}
}
