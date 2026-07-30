package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestEncryptionConfig_KeychainServiceDefaultsToOmnia (v0.4 memory-at-rest-security,
// PR4, design ADR-3: "Service=omnia, account=db-key-v1"): an operator who
// opts in with only `encryption: {enabled: true}` must still get a sane
// keychain service name, matching every other v0.4 capability's "only
// tuning params get applyDefaults values" convention (ADR-5) — mirrors
// Consolidation.MinScore/Ranker.MinTrainExamples/Cartridge.TopMemories'
// existing defaults, which apply unconditionally in applyDefaults
// regardless of Enabled.
func TestEncryptionConfig_KeychainServiceDefaultsToOmnia(t *testing.T) {
	path := writeTempConfig(t, "encryption: {enabled: true}\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Encryption.KeychainService != "omnia" {
		t.Errorf("Encryption.KeychainService = %q, want %q", cfg.Encryption.KeychainService, "omnia")
	}
}

// TestEncryptionConfig_ExplicitKeychainServiceIsPreserved confirms the
// default never clobbers an operator-supplied override.
func TestEncryptionConfig_ExplicitKeychainServiceIsPreserved(t *testing.T) {
	path := writeTempConfig(t, "encryption: {enabled: true, keychain_service: custom-service}\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Encryption.KeychainService != "custom-service" {
		t.Errorf("Encryption.KeychainService = %q, want %q", cfg.Encryption.KeychainService, "custom-service")
	}
}
