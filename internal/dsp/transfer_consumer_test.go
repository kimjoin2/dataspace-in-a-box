package dsp

import (
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// The consumer default is passive, which is the opposite of the provider
// default of [STARTED]. Eleven of the fifteen TP_C tests fail if this
// connector volunteers a message, so "no entry" must mean "send nothing".
func TestResolveConsumerTransferPolicyDefaultsToPassive(t *testing.T) {
	after, sequence := resolveConsumerTransferPolicy(config.Config{}, "urn:uuid:unknown")
	if len(sequence) != 0 {
		t.Errorf("sequence = %v, want empty", sequence)
	}
	if after != TransferStarted {
		t.Errorf("after = %q, want %q", after, TransferStarted)
	}
}

func TestResolveConsumerTransferPolicyUsesTheMatchingEntry(t *testing.T) {
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", Sequence: []string{TransferCompleted}},
		{AgreementID: "urn:uuid:b", After: TransferRequested, Sequence: []string{TransferTerminated}},
	}}
	if after, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:b"); after != TransferRequested ||
		len(seq) != 1 || seq[0] != TransferTerminated {
		t.Errorf("b: after=%q seq=%v", after, seq)
	}
	// after is omitted on entry a, so it takes the default rather than empty.
	if after, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:a"); after != TransferStarted ||
		len(seq) != 1 || seq[0] != TransferCompleted {
		t.Errorf("a: after=%q seq=%v", after, seq)
	}
}

// An entry with an explicitly empty sequence is a deliberate "stay passive"
// and must not fall through to the default, which is the distinction the
// provider-side resolver also had to make.
func TestResolveConsumerTransferPolicyEmptySequenceIsNotTheDefault(t *testing.T) {
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", Sequence: []string{}},
	}}
	if _, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:a"); len(seq) != 0 {
		t.Errorf("sequence = %v, want empty", seq)
	}
}
