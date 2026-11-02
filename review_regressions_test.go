package kvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/edid"
	"github.com/jetkvm/kvm/internal/hidrpc"
	"github.com/jetkvm/kvm/internal/native"
)

// Literal from internal/native/video.go at pinned upstream 36c008fb (0.5.9).
const pinnedPreviousDefaultEDID = "00FFFFFFFFFFFF0028B4010001EEFFC0302301038047287856EE91A3544C99260F5054000000D1C081C0318001010101010101010101023A801871382D40582C4500C48E2100001E773300A050D02B2030203500122C2100001A000000FD00174C0F5111000A202020202020000000FC004A65744B564D2076310A20202001D5020322D1431004012309070783010000E200CFE40D100401E305000065030C001000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000CF"

func TestSavedConfigMigratesPinnedPreviousEDIDAndPreservesCustom(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		migrate     bool
	}{
		{"pinned uppercase", pinnedPreviousDefaultEDID, true},
		{"pinned lowercase", strings.ToLower(pinnedPreviousDefaultEDID), true},
		{"custom", strings.Replace(strings.Replace(pinnedPreviousDefaultEDID, "28B40100", "29B40100", 1), "01D50203", "01D40203", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := edid.ValidateHex(tc.value); err != nil {
				t.Fatal("external EDID fixture:", err)
			}
			oldConfig, oldError := config, configLoadError
			config, configLoadError = nil, nil
			t.Cleanup(func() { config, configLoadError = oldConfig, oldError })
			payload, err := json.Marshal(map[string]any{"hdmi_edid_string": tc.value, "auto_update_enabled": true})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "saved-config.json")
			if err = os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			loadConfigFromPath(path)
			if configLoadError != nil {
				t.Fatal(configLoadError)
			}
			want := tc.value
			if tc.migrate {
				want = native.DefaultEDID
			}
			if config.EdidString != want {
				t.Fatalf("saved config EDID was not migrated/preserved: got %q, want %q", config.EdidString, want)
			}
		})
	}
}

func TestEDIDTransactionsDoNotRollbackAcknowledgedSuccess(t *testing.T) {
	n := setupEDIDRPC(t, false)
	aPaused, aFail := make(chan struct{}), make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go func() {
		aDone <- setEDIDWithSave("", func() error { close(aPaused); <-aFail; return errors.New("A save failed") })
	}()
	<-aPaused
	bStarted := make(chan struct{})
	go func() {
		close(bStarted)
		bDone <- setEDIDWithSave(pinnedPreviousDefaultEDID, func() error { return nil })
	}()
	<-bStarted
	// Let B reach the transaction while A is parked at its save boundary. The
	// serialized implementation must delay B's acknowledgement until A rolls back.
	var bErr error
	bAcknowledged := false
	select {
	case bErr = <-bDone:
		bAcknowledged = true
	case <-time.After(100 * time.Millisecond):
	}
	close(aFail)
	if err := <-aDone; err == nil {
		t.Fatal("A failure disappeared")
	}
	if !bAcknowledged {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatal(bErr)
	}
	if config.EdidString != pinnedPreviousDefaultEDID || n.writes[len(n.writes)-1] != pinnedPreviousDefaultEDID {
		t.Fatal("A rollback undid B's acknowledged successful EDID")
	}
}

func TestMacroCompletionReportsToOriginalSessionAfterHandoff(t *testing.T) {
	oldSession := currentSession
	a, b := &Session{done: make(chan struct{})}, &Session{done: make(chan struct{})}
	currentSession = a
	t.Cleanup(func() { currentSession = oldSession; setKeyboardMacroCancel(nil) })
	started, complete := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var targets []*Session
	var states []bool
	report := func(target *Session, state hidrpc.KeyboardMacroState) {
		targets = append(targets, target)
		states = append(states, state.State)
	}
	run := func(context.Context, []hidrpc.KeyboardMacroStep, *Session) error {
		close(started)
		<-complete
		return nil
	}
	go func() { done <- executeKeyboardMacroRun(nil, a, run, report) }()
	<-started
	currentSession = b // Handoff can precede the replacement's first macro.
	close(complete)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != a || targets[1] != a || !states[0] || states[1] {
		t.Fatal("macro state was delivered to the replacement session")
	}
}

func TestLateMacroCompletionCannotDisownOrCompleteReplacement(t *testing.T) {
	oldSession := currentSession
	a, b := &Session{done: make(chan struct{})}, &Session{done: make(chan struct{})}
	currentSession = a
	aRunning, bRunning := make(chan context.Context, 1), make(chan context.Context, 1)
	aRelease, bRelease := make(chan struct{}), make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	var releaseAOnce, releaseBOnce gosync.Once
	releaseA := func() { releaseAOnce.Do(func() { close(aRelease) }) }
	releaseB := func() { releaseBOnce.Do(func() { close(bRelease) }) }
	var reportLock gosync.Mutex
	type event struct {
		session *Session
		running bool
	}
	var events []event
	report := func(s *Session, state hidrpc.KeyboardMacroState) {
		reportLock.Lock()
		events = append(events, event{s, state.State})
		reportLock.Unlock()
	}
	runA := func(ctx context.Context, _ []hidrpc.KeyboardMacroStep, _ *Session) error {
		aRunning <- ctx
		<-aRelease
		return ctx.Err()
	}
	runB := func(ctx context.Context, _ []hidrpc.KeyboardMacroStep, _ *Session) error {
		bRunning <- ctx
		<-bRelease
		return ctx.Err()
	}
	go func() { aDone <- executeKeyboardMacroRun(nil, a, runA, report) }()
	aContext := <-aRunning
	currentSession = b
	go func() { bDone <- executeKeyboardMacroRun(nil, b, runB, report) }()
	bContext := <-bRunning
	t.Cleanup(func() {
		cancelKeyboardMacro()
		releaseA()
		releaseB()
		currentSession = oldSession
		setKeyboardMacroCancel(nil)
	})
	if aContext.Err() != context.Canceled {
		t.Error("replacement did not cancel A")
	}
	releaseA()
	if err := <-aDone; !errors.Is(err, context.Canceled) {
		t.Errorf("late A result: %v", err)
	}
	reportLock.Lock()
	for _, event := range events {
		if event.session == b && !event.running {
			t.Error("late A completion falsely completed B")
		}
	}
	reportLock.Unlock()
	cancelKeyboardMacro()
	if bContext.Err() != context.Canceled {
		t.Error("late A completion removed B's cancel function")
	}
	releaseB()
	<-bDone
}
