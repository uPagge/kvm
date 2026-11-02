package kvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/usbgadget"
	"github.com/pion/webrtc/v4"
)

// Missing migration consent must not expose an old raw upstream audio=true.
// Conversely, decoding must not discard a later explicit persisted choice.
func TestIdentity3AudioMigrationAndReload(t *testing.T) {
	for _, tc := range []struct {
		name, raw      string
		audio, capture bool
	}{
		{"old standard", `{"usb_devices":{"hid_profile":"standard-relative","audio":true},"audio_enabled":true}`, false, false},
		{"old implicit profile", `{"usb_devices":{"audio":true},"audio_enabled":true}`, false, false},
		{"explicit on", `{"standard_relative_audio_config_version":1,"usb_devices":{"hid_profile":"standard-relative","audio":true},"audio_enabled":true}`, true, true},
		{"explicit off", `{"standard_relative_audio_config_version":1,"usb_devices":{"hid_profile":"standard-relative","audio":false},"audio_enabled":true}`, false, false},
		{"legacy unchanged", `{"usb_devices":{"hid_profile":"","audio":true},"audio_enabled":true}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded := getDefaultConfig()
			if err := json.Unmarshal([]byte(tc.raw), &loaded); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if loaded.UsbDevices.Audio != tc.audio || loaded.AudioEnabled != tc.capture {
					t.Fatalf("audio/capture = %v/%v, want %v/%v", loaded.UsbDevices.Audio, loaded.AudioEnabled, tc.audio, tc.capture)
				}
				raw, err := json.Marshal(loaded)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if loaded.UsbDevices.Profile == "standard-relative" && fields["standard_relative_audio_config_version"] != float64(1) {
					t.Fatalf("migration marker not persisted: %s", raw)
				}
				loaded = getDefaultConfig()
				if err := json.Unmarshal(raw, &loaded); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func setupIdentity3Audio(t *testing.T, enabled bool) {
	t.Helper()
	oldConfig, oldGadget := config, gadget
	c := getDefaultConfig()
	c.UsbDevices.Audio, c.AudioEnabled = enabled, enabled
	config = &c
	gadget = usbgadget.NewUsbGadget("audio-test", config.UsbDevices, nil, nil)
	if gadget == nil {
		t.Fatal("test gadget construction failed")
	}
	t.Cleanup(func() { _ = gadget.Close(); config, gadget = oldConfig, oldGadget })
}

func TestIdentity3DisabledAudioRejectsPreviouslyNegotiatedTrack(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		usb, playback, capture bool
	}{
		{"USB disabled", false, true, false},
		{"playback disabled", true, false, false},
		{"both enabled", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupIdentity3Audio(t, tc.usb)
			config.AudioEnabled = tc.playback
			track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000}, "audio", "kvm")
			if err != nil {
				t.Fatal(err)
			}
			defer stopAudio()
			startAudio(track)
			if (audioCancel != nil && audioTrack == track) != tc.capture {
				t.Fatalf("previously negotiated track capture=%v, want %v", audioCancel != nil, tc.capture)
			}
		})
	}
}

// A failed capture-setting save must leave both the acknowledged config and
// the active capture intact, rather than stopping audio before persistence.
func TestIdentity3AudioCaptureSaveRollback(t *testing.T) {
	setupIdentity3Audio(t, true)
	oldCancel, oldStopped := audioCancel, audioStopped
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	audioCancel, audioStopped = cancel, stopped
	go func() { <-ctx.Done(); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped; audioCancel, audioStopped = oldCancel, oldStopped })
	want := errors.New("disk full")
	err := setAudioConfigWithSave(AudioConfig{Enabled: false}, func() error { return want })
	if !errors.Is(err, want) || !config.AudioEnabled || ctx.Err() != nil {
		t.Fatalf("failed save changed capture: err=%v enabled=%v canceled=%v", err, config.AudioEnabled, ctx.Err())
	}
}

// Real Config serialization, real profile setters and a temp-file reload are
// exercised; only hardware rebind is replaced at its external boundary.
func TestIdentity3AudioExplicitTogglePersists(t *testing.T) {
	setupIdentity3Audio(t, false)
	path := filepath.Join(t.TempDir(), "config.json")
	for _, enabled := range []bool{true, false, true} {
		devices := *config.UsbDevices
		devices.Audio = enabled
		if err := setUsbDevicesWithSaveAndUpdate(devices, func() error { return saveConfig(path) }, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if config.AudioEnabled {
			t.Fatal("USB function toggle implicitly enabled playback")
		}
		config = nil
		loadConfigFromPath(path)
		if config.UsbDevices.Audio != enabled || gadget.GetGadgetDevices().Audio != enabled {
			t.Fatalf("toggle/reload lost %v", enabled)
		}
		if enabled {
			if err := setAudioConfigWithSave(AudioConfig{Enabled: true}, func() error { return saveConfig(path) }); err != nil {
				t.Fatal(err)
			}
			config = nil
			loadConfigFromPath(path)
			if !effectiveAudioEnabled() {
				t.Fatal("playback choice lost on reload")
			}
		}
		// Next iteration starts with playback disabled, independently of USB.
		if err := setAudioConfigWithSave(AudioConfig{Enabled: false}, func() error { return saveConfig(path) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestIdentity3AudioUSBFailuresRestoreState(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "rebind", true: "save"}[failSave], func(t *testing.T) {
			setupIdentity3Audio(t, true)
			// Prove that a failed attempt also rolls back its version stamp.
			config.StandardRelativeAudioConfigVersion = 0
			before := *config.UsbDevices
			marker := config.StandardRelativeAudioConfigVersion
			devices := before
			devices.Audio = false
			want, rollback := errors.New("operation failed"), errors.New("rollback rebind failed")
			updates := 0
			err := setUsbDevicesWithSaveAndUpdate(devices, func() error {
				if failSave {
					return want
				}
				t.Fatal("save after failed rebind")
				return nil
			}, func() error {
				updates++
				if updates == 1 {
					if !failSave {
						return want
					}
					return nil
				}
				return rollback
			})
			if !errors.Is(err, want) || !errors.Is(err, rollback) || updates != 2 {
				t.Fatalf("lost failure/rollback: %v updates=%d", err, updates)
			}
			if *config.UsbDevices != before || !config.AudioEnabled || config.StandardRelativeAudioConfigVersion != marker || gadget.GetGadgetDevices() != before {
				t.Fatal("failed USB transaction changed previous state")
			}
		})
	}
}

// Without shared USB/audio admission B sees A's uncommitted disable and rejects
// playback, although A ultimately rolls back to audio enabled.
func TestIdentity3AudioMutationsSerialize(t *testing.T) {
	setupIdentity3Audio(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	a, b := make(chan error, 1), make(chan error, 1)
	devices := *config.UsbDevices
	devices.Audio = false
	want := errors.New("failed save")
	go func() {
		a <- setUsbDevicesWithSaveAndUpdate(devices, func() error { close(entered); <-release; return want }, func() error { return nil })
	}()
	<-entered
	go func() { b <- setAudioConfigWithSave(AudioConfig{Enabled: true}, func() error { return nil }) }()
	select {
	case err := <-b:
		close(release)
		<-a
		t.Fatalf("audio mutation observed uncommitted USB state: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-a; !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := <-b; err != nil {
		t.Fatal(err)
	}
	if !effectiveAudioEnabled() {
		t.Fatal("acknowledged playback undone by older rollback")
	}
}
