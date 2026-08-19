package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// minimal returns a configuration document that satisfies every required key,
// with extra appended. Tests that are not about a specific required key use
// this, so that adding the next required key does not mean editing every test.
// withoutAuthFiles is the smallest document that is valid apart from the two
// files authentication needs. Only the tests that are about those files use
// it directly; everything else goes through minimal.
func withoutAuthFiles(extra string) []byte {
	return []byte("public_url: https://connector.example.org\n" +
		"participant_id: urn:participant:example\n" +
		"data_dir: ./data\n" + extra)
}

// minimal is withoutAuthFiles plus the key and roster paths, because
// require_auth defaults to true and a document lacking them no longer loads.
// The paths are never opened during config loading — only startup reads them.
func minimal(extra string) []byte {
	return withoutAuthFiles("participant_key: /etc/dsbox/participant.key\n" +
		"roster: /etc/dsbox/roster.json\n" + extra)
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
	_, err := Load([]byte("public_url: http://connector.example.org\nparticipant_id: urn:participant:example\nparticipant_key: /k\nroster: /r\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for http without dev_mode")
	}
}

func TestLoadAllowsPlainHTTPInDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://dsbox:8080\ndev_mode: true\nparticipant_id: urn:participant:example\ndata_dir: ./data\nparticipant_key: /k\nroster: /r\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsRelativePublicURL(t *testing.T) {
	_, err := Load([]byte("public_url: /dsp\nparticipant_id: urn:participant:example\nparticipant_key: /k\nroster: /r\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a non-absolute public_url")
	}
}

func TestLoadRejectsTrailingSlash(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org/\nparticipant_id: urn:participant:example\nparticipant_key: /k\nroster: /r\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a trailing slash in public_url")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\nparticipant_id: urn:participant:example\ndata_dir: ./data\nparticipant_key: /k\nroster: /r\n"),
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
		"DSBOX_PUBLIC_URL":      "https://from-env.example.org",
		"DSBOX_PARTICIPANT_ID":  "urn:participant:from-env",
		"DSBOX_DATA_DIR":        "./data",
		"DSBOX_PARTICIPANT_KEY": "/k",
		"DSBOX_ROSTER":          "/r",
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
	_, err := Load([]byte("public_url: https://connector.example.org\nparticipant_key: /k\nroster: /r\n"), env(nil))
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

func TestLoadRequiresDataDir(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org\nparticipant_id: urn:participant:example\nparticipant_key: /k\nroster: /r\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when data_dir is absent")
	}
}

func TestDataDirFromEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://connector.example.org\nparticipant_id: urn:participant:example\ndata_dir: ./from-file\nparticipant_key: /k\nroster: /r\n"),
		env(map[string]string{"DSBOX_DATA_DIR": "./from-env"}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "./from-env" {
		t.Errorf("DataDir = %q, want the environment value", cfg.DataDir)
	}
}

func TestValidityUntilIsOptional(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].ValidityUntil != nil {
		t.Errorf("ValidityUntil = %v, want nil when absent", cfg.Datasets[0].ValidityUntil)
	}
}

func TestValidityUntilParses(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    validity_until: 2027-01-01T00:00:00Z\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].ValidityUntil == nil {
		t.Fatal("ValidityUntil = nil, want the parsed timestamp")
	}
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !cfg.Datasets[0].ValidityUntil.Equal(want) {
		t.Errorf("ValidityUntil = %v, want %v", cfg.Datasets[0].ValidityUntil, want)
	}
}

func TestMalformedValidityUntilIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    validity_until: not-a-time\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a malformed validity_until")
	}
}

func TestConsumerPoliciesEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ConsumerPolicies) != 0 {
		t.Errorf("ConsumerPolicies = %v, want empty when the key is absent", cfg.ConsumerPolicies)
	}
}

func TestConsumerPolicyParses(t *testing.T) {
	cfg, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_offer: passive\n    on_agreement: reject\n    on_idle: abandon\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ConsumerPolicies) != 1 {
		t.Fatalf("ConsumerPolicies = %v, want one entry", cfg.ConsumerPolicies)
	}
	p := cfg.ConsumerPolicies[0]
	if p.DatasetID != "urn:dataset:a" || p.OnOffer != "passive" || p.OnAgreement != "reject" || p.OnIdle != "abandon" {
		t.Errorf("ConsumerPolicies[0] = %+v, want the parsed fixture", p)
	}
}

func TestConsumerPolicyOmittedFieldsStayEmpty(t *testing.T) {
	cfg, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.ConsumerPolicies[0]
	if p.OnOffer != "" || p.OnAgreement != "" || p.OnIdle != "" {
		t.Errorf("ConsumerPolicies[0] = %+v, want every unset field to stay empty — defaulting happens where the policy is resolved, not at load time", p)
	}
}

func TestConsumerPolicyRequiresDatasetID(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - on_offer: accept\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a consumer_policies entry with no dataset_id")
	}
}

func TestConsumerPolicyRejectsInvalidOnOffer(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_offer: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_offer value")
	}
}

func TestConsumerPolicyRejectsInvalidOnAgreement(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_agreement: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_agreement value")
	}
}

func TestConsumerPolicyRejectsInvalidOnIdle(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_idle: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_idle value")
	}
}

func TestMgmtTokenEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "" {
		t.Errorf("MgmtToken = %q, want empty when the key is absent", cfg.MgmtToken)
	}
}

func TestMgmtTokenParses(t *testing.T) {
	cfg, err := Load(minimal("mgmt_token: 0123456789abcdef\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "0123456789abcdef" {
		t.Errorf("MgmtToken = %q, want the configured token", cfg.MgmtToken)
	}
}

func TestMgmtTokenFromEnvironment(t *testing.T) {
	cfg, err := Load(minimal(""), env(map[string]string{"DSBOX_MGMT_TOKEN": "fedcba9876543210"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "fedcba9876543210" {
		t.Errorf("MgmtToken = %q, want the environment value", cfg.MgmtToken)
	}
}

func TestMgmtTokenTooShortIsAnError(t *testing.T) {
	_, err := Load(minimal("mgmt_token: short\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a token below the minimum length")
	}
}

func TestMgmtTokenOneCharBelowMinimumIsAnError(t *testing.T) {
	// 15 characters: the boundary itself. The two 16-character fixtures above
	// pin the accepting side, so this is what makes "at least 16" exact
	// rather than "somewhere near 16".
	_, err := Load(minimal("mgmt_token: 0123456789abcde\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a token one character below the minimum")
	}
}

func TestTransferPoliciesEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 0 {
		t.Errorf("TransferPolicies = %v, want empty when the key is absent", cfg.TransferPolicies)
	}
}

func TestTransferPolicyParses(t *testing.T) {
	cfg, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [STARTED, SUSPENDED, TERMINATED]\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 1 {
		t.Fatalf("TransferPolicies = %v, want one entry", cfg.TransferPolicies)
	}
	p := cfg.TransferPolicies[0]
	if p.AgreementID != "urn:uuid:a" || len(p.Sequence) != 3 || p.Sequence[1] != "SUSPENDED" {
		t.Errorf("TransferPolicies[0] = %+v, want the parsed fixture", p)
	}
}

func TestTransferPolicyEmptySequenceIsValid(t *testing.T) {
	// An explicit empty sequence is the only way to say "accept the request and
	// stay in REQUESTED", which four TCK tests assert. It must survive loading
	// as a present-but-empty entry, distinct from having no entry at all —
	// which is why this asserts the entry is there *and* that its sequence is
	// empty, rather than only that loading succeeded.
	cfg, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: []\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 1 || len(cfg.TransferPolicies[0].Sequence) != 0 {
		t.Errorf("TransferPolicies = %+v, want one entry with an empty sequence", cfg.TransferPolicies)
	}
}

func TestTransferPolicyRejectsAnUnknownState(t *testing.T) {
	_, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [STARTED, NONSENSE]\n"), env(nil))
	if err == nil {
		t.Error("Load: expected an error for a sequence naming a state that is not a transfer state")
	}
}

func TestTransferPolicyRejectsRequestedInASequence(t *testing.T) {
	// REQUESTED is the state a transfer starts in, not one it can be driven
	// to, so a sequence naming it configures a step that could never be
	// written. Staying in REQUESTED is spelled `sequence: []`.
	_, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [REQUESTED]\n"), env(nil))
	if err == nil {
		t.Error("Load: expected an error for a sequence naming REQUESTED")
	}
}

func TestTransferPolicyRequiresAnAgreementID(t *testing.T) {
	_, err := Load(minimal("transfer_policies:\n  - sequence: [STARTED]\n"), env(nil))
	if err == nil {
		t.Error("Load: expected an error for a policy with no agreement_id")
	}
}

func TestConsumerTransferPolicyRejectsAnInvalidEntry(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown state in sequence": "consumer_transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [BANANA]\n",
		"unknown after":             "consumer_transfer_policies:\n  - agreement_id: urn:uuid:a\n    after: BANANA\n    sequence: [COMPLETED]\n",
		// An entry with no agreement_id can never be selected, so it is a
		// configuration mistake rather than a harmless no-op.
		"no agreement_id": "consumer_transfer_policies:\n  - sequence: [COMPLETED]\n",
		// A sequence released from a terminal state can never send anything:
		// every legality predicate refuses a terminal from-state. Loading it
		// cleanly would be accepting a constraint that is never enforced.
		"terminal after": "consumer_transfer_policies:\n  - agreement_id: urn:uuid:a\n    after: TERMINATED\n    sequence: [COMPLETED]\n",
	} {
		if _, err := Load(minimal(doc), env(nil)); err == nil {
			t.Errorf("%s: accepted an invalid policy", name)
		}
	}
}

func TestConsumerTransferPolicyLoads(t *testing.T) {
	cfg, err := Load(minimal(
		"consumer_transfer_policies:\n"+
			"  - agreement_id: urn:uuid:a\n"+
			"    after: REQUESTED\n"+
			"    sequence: [TERMINATED]\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ConsumerTransferPolicies) != 1 {
		t.Fatalf("got %d policies, want 1", len(cfg.ConsumerTransferPolicies))
	}
	p := cfg.ConsumerTransferPolicies[0]
	if p.AgreementID != "urn:uuid:a" || p.After != "REQUESTED" ||
		len(p.Sequence) != 1 || p.Sequence[0] != "TERMINATED" {
		t.Errorf("policy = %+v", p)
	}
}

func TestRequireAuthDefaultsToTrue(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AuthRequired() {
		t.Error("authentication is off by default")
	}
}

// Turning authentication off is a development affordance. An operator who has
// not declared this a development instance may not also declare that anyone
// may talk to it.
func TestRequireAuthFalseNeedsDevMode(t *testing.T) {
	if _, err := Load(minimal("require_auth: false\n"), env(nil)); err == nil {
		t.Error("require_auth: false loaded without dev_mode")
	}
	if _, err := Load(minimal("require_auth: false\ndev_mode: true\n"), env(nil)); err != nil {
		t.Errorf("require_auth: false with dev_mode: true failed to load: %v", err)
	}
}

// With authentication on, the two files it needs are required. Loading
// without them would produce a connector that cannot verify or sign, and the
// first symptom would be every request failing at runtime.
func TestAuthRequiresKeyAndRoster(t *testing.T) {
	if _, err := Load(withoutAuthFiles("participant_key: /k\n"), env(nil)); err == nil {
		t.Error("loaded with a key but no roster")
	}
	if _, err := Load(withoutAuthFiles("roster: /r\n"), env(nil)); err == nil {
		t.Error("loaded with a roster but no key")
	}
}

// A source_file that is not there is a typo, and a typo should fail at boot
// rather than on the first pull — by which time a counterparty is waiting.
func TestSourceFileMustExist(t *testing.T) {
	if _, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    source_file: /nope/missing\n"), env(nil)); err == nil {
		t.Error("a missing source_file loaded without error")
	}
	if _, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    source_file: /tmp\n"), env(nil)); err == nil {
		t.Error("a directory loaded as a source_file")
	}

	path := filepath.Join(t.TempDir(), "d.csv")
	if err := os.WriteFile(path, []byte("a,b\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    source_file: "+path+"\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].SourceFile != path {
		t.Errorf("SourceFile = %q", cfg.Datasets[0].SourceFile)
	}
}
