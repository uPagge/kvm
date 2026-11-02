package kvm

import (
	"encoding/json"
	"testing"
)

func TestForkConfigDisablesStockAutoUpdate(t *testing.T) {
	loaded := getDefaultConfig()
	if loaded.AutoUpdateEnabled {
		t.Fatal("new fork config permits stock OTA")
	}
	if err := json.Unmarshal([]byte(`{"auto_update_enabled":true}`), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.AutoUpdateEnabled {
		t.Fatal("old config re-enabled stock OTA")
	}
}

func TestForkRejectsStockOTAEnableWithoutConfigMutation(t *testing.T) {
	old := config
	config = &Config{}
	t.Cleanup(func() { config = old })
	if enabled, err := rpcSetAutoUpdateState(true); err == nil || enabled {
		t.Fatalf("stock OTA enable accepted: %t %v", enabled, err)
	}
	if config.AutoUpdateEnabled {
		t.Fatal("rejected OTA enable mutated config")
	}
}

func TestForkMQTTRejectsBeforeProgressOrNetwork(t *testing.T) {
	old := config
	config = &Config{MqttConfig: &MQTTConfig{EnableActions: true}}
	t.Cleanup(func() { config = old })
	m := &MQTTManager{}
	m.connected.Store(true)
	// Nil client would panic on a publish. Denial must precede that boundary.
	m.handleUpdateInstallCommand(nil, nil)
	if m.updateRequested.Load() {
		t.Fatal("denied OTA changed progress state")
	}
}

func TestForkRejectsManualStockOTABeforeBackend(t *testing.T) {
	oldConfig, oldOTA := config, otaState
	config = &Config{}
	otaState = nil
	t.Cleanup(func() { config, otaState = oldConfig, oldOTA })
	if err := rpcTryUpdate(); err == nil {
		t.Fatal("stock OTA request was accepted")
	}
	if err := rpcTryUpdateComponents(updateParams{Components: map[string]string{"system": "0.2.8"}}, false, true); err == nil {
		t.Fatal("unreviewed system OTA was accepted")
	}
}
