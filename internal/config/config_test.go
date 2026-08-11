package config

import (
	"os"
	"testing"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// minimal returns a configuration document that satisfies every required key,
// with extra appended. Tests that are not about a specific required key use
// this, so that adding the next required key does not mean editing every test.
func minimal(extra string) []byte {
	return []byte("public_url: https://connector.example.org\n" +
		"participant_id: urn:participant:example\n" + extra)
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSPAddr != "0.0.0.0:8080" {
		t.Errorf("DSPAddr = %q, want 0.0.0.0:8080", cfg.DSPAddr)
	}
	if cfg.MgmtAddr != "127.0.0.1:8081" {
		t.Errorf("MgmtAddr = %q, want 127.0.0.1:8081", cfg.MgmtAddr)
	}
}

func TestLoadRequiresPublicURL(t *testing.T) {
	_, err := Load([]byte("dev_mode: true\nparticipant_id: urn:participant:example\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when public_url is absent")
	}
}

func TestLoadRejectsPlainHTTPOutsideDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://connector.example.org\nparticipant_id: urn:participant:example\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for http without dev_mode")
	}
}

func TestLoadAllowsPlainHTTPInDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://dsbox:8080\ndev_mode: true\nparticipant_id: urn:participant:example\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsRelativePublicURL(t *testing.T) {
	_, err := Load([]byte("public_url: /dsp\nparticipant_id: urn:participant:example\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a non-absolute public_url")
	}
}

func TestLoadRejectsTrailingSlash(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org/\nparticipant_id: urn:participant:example\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a trailing slash in public_url")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\nparticipant_id: urn:participant:example\n"),
		env(map[string]string{
			"DSBOX_PUBLIC_URL": "https://from-env.example.org",
			"DSBOX_DSP_ADDR":   "0.0.0.0:7777",
		}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://from-env.example.org" {
		t.Errorf("PublicURL = %q, want the environment value", cfg.PublicURL)
	}
	if cfg.DSPAddr != "0.0.0.0:7777" {
		t.Errorf("DSPAddr = %q, want the environment value", cfg.DSPAddr)
	}
}

func TestInvalidDevModeEnvIsAnError(t *testing.T) {
	_, err := Load(
		minimal(""),
		env(map[string]string{"DSBOX_DEV_MODE": "yes-please"}),
	)
	if err == nil {
		t.Fatal("Load: expected an error for an unparsable DSBOX_DEV_MODE")
	}
}

func TestMalformedYAMLIsAnError(t *testing.T) {
	_, err := Load([]byte("public_url: [unclosed\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for malformed YAML")
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := Load(
		minimal("dsp_add: 0.0.0.0:9999\n"),
		env(nil),
	)
	if err == nil {
		t.Fatal("Load: expected an error for an unknown key (e.g. a typo of dsp_addr)")
	}
}

func TestEmptyDocumentWithEnvironmentStillLoads(t *testing.T) {
	cfg, err := Load([]byte(""), env(map[string]string{
		"DSBOX_PUBLIC_URL":     "https://from-env.example.org",
		"DSBOX_PARTICIPANT_ID": "urn:participant:from-env",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://from-env.example.org" {
		t.Errorf("PublicURL = %q, want the environment value", cfg.PublicURL)
	}
	if cfg.ParticipantID != "urn:participant:from-env" {
		t.Errorf("ParticipantID = %q, want the environment value", cfg.ParticipantID)
	}
	if cfg.DSPAddr != "0.0.0.0:8080" {
		t.Errorf("DSPAddr = %q, want the default", cfg.DSPAddr)
	}
}

func TestLoadRequiresParticipantID(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when participant_id is absent")
	}
}

func TestParticipantIDFromEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(minimal(""), env(map[string]string{
		"DSBOX_PARTICIPANT_ID": "urn:participant:from-env",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ParticipantID != "urn:participant:from-env" {
		t.Errorf("ParticipantID = %q, want the environment value", cfg.ParticipantID)
	}
}

func TestDatasetsAreOptional(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Datasets) != 0 {
		t.Errorf("Datasets = %v, want none", cfg.Datasets)
	}
}

func TestDatasetsAreLoadedInOrder(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n  - id: urn:dataset:b\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Datasets) != 2 {
		t.Fatalf("Datasets has %d entries, want 2", len(cfg.Datasets))
	}
	if cfg.Datasets[0].ID != "urn:dataset:a" || cfg.Datasets[1].ID != "urn:dataset:b" {
		t.Errorf("Datasets = %v, want a then b", cfg.Datasets)
	}
}

func TestDuplicateDatasetIDIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n  - id: urn:dataset:a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a duplicate dataset id")
	}
}

func TestRelativeDatasetIDIsAnError(t *testing.T) {
	// An @id is an IRI. A relative one's fate under JSON-LD expansion depends
	// on a document base the TCK never sets.
	_, err := Load(minimal("datasets:\n  - id: dataset-a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a dataset id with no scheme")
	}
}

func TestDatasetIDWithPathSeparatorIsAnError(t *testing.T) {
	// The dataset endpoint routes on the identifier as a single path segment.
	_, err := Load(minimal("datasets:\n  - id: https://example.org/datasets/a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a dataset id containing a slash")
	}
}

func TestEmptyDatasetIDIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: \"\"\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an empty dataset id")
	}
}

// TestExampleConfigLoads guards against config.example.yaml rotting: nothing
// else in the repository loads it, so a future required key could go
// unreflected in the example and only surface at a fresh clone's startup.
// Load itself performs no I/O by design, so the file is read here, not by
// Load.
func TestExampleConfigLoads(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	cfg, err := Load(data, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, d := range cfg.Datasets {
		if d.ID == "urn:dataset:sample" {
			found = true
		}
	}
	if !found {
		t.Errorf("Datasets = %v, want urn:dataset:sample present", cfg.Datasets)
	}
}
