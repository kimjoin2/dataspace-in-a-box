// Package config resolves the connector configuration from a YAML document and
// environment overrides.
//
// Load performs no I/O: the caller supplies the document bytes and an
// environment lookup. That keeps every validation rule testable without a
// filesystem, and keeps the precedence between file and environment in one
// readable place.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved connector configuration.
type Config struct {
	// PublicURL is the connector's external address. It is required: behind a
	// reverse proxy the connector cannot determine this itself, and inferring
	// it from the Host header is never acceptable.
	PublicURL string `yaml:"public_url"`

	// ParticipantID identifies this participant in every catalog it serves. It
	// is required and never inferred. Section 9 will eventually make this a
	// did:web identifier; only the value changes when that day comes, because
	// deriving one now would mint DIDs nothing can resolve.
	ParticipantID string `yaml:"participant_id"`

	// Datasets are the identifiers this connector advertises. Advertised
	// datasets are an operator declaration rather than connector runtime state,
	// so they belong in configuration rather than in storage — see
	// DECISIONS.md section 8. Changing them means editing this file and
	// restarting.
	Datasets []Dataset `yaml:"datasets"`

	// DevMode permits a plain http PublicURL. The local demo and the TCK
	// harness run without a proxy; nothing else should set this.
	DevMode bool `yaml:"dev_mode"`

	// DSPAddr is the listen address for public DSP endpoints.
	DSPAddr string `yaml:"dsp_addr"`

	// MgmtAddr is the listen address for the management API. It binds to
	// localhost by default so a firewall mistake cannot expose it.
	MgmtAddr string `yaml:"mgmt_addr"`

	// MgmtToken is the single static bearer token the management API accepts
	// (DECISIONS.md section 11). Optional, and absent means the management
	// API refuses every authenticated request rather than allowing them: a
	// missing token must never read as "no auth required", or the localhost
	// default becomes an open write endpoint the moment mgmt_addr changes.
	// /health is unauthenticated either way.
	MgmtToken string `yaml:"mgmt_token"`

	// DataDir is where the connector's SQLite database file lives, at
	// {DataDir}/dsbox.db. Required: the catalog milestone did not need
	// storage, but negotiation state is the connector's first runtime state
	// that must survive a restart (DECISIONS.md section 8).
	DataDir string `yaml:"data_dir"`

	// DataIdleTimeout bounds how long a data transfer may go without
	// progress, on both sides: the provider rolls its write deadline by it,
	// and the consumer's pull cancels when no bytes arrive within it.
	// Optional; defaultDataIdleTimeout when unset.
	DataIdleTimeout time.Duration `yaml:"data_idle_timeout"`

	// MaxDownloadBytes caps a single data pull. With no overall client
	// timeout, a counterparty that states no length is bounded by nothing
	// else — and it is also the only backstop against a transfer that never
	// goes idle because it dribbles. Optional; defaultMaxDownloadBytes when
	// unset.
	MaxDownloadBytes int64 `yaml:"max_download_bytes"`

	// ConsumerPolicies configures this connector's autonomous behavior when
	// it is negotiating as consumer, keyed by the dataset_id this connector
	// itself requests via POST /negotiations/initiate. A dataset_id with no
	// matching entry gets every field's default (accept, verify, wait) —
	// see the design spec's "Why a policy configuration, not a content
	// rule".
	ConsumerPolicies []ConsumerPolicy `yaml:"consumer_policies"`

	// TransferPolicies configures this connector's autonomous behavior as
	// transfer provider, keyed by the agreement a transfer is requested
	// under. An agreement with no matching entry starts the transfer and
	// stops there — see TransferPolicy.
	TransferPolicies []TransferPolicy `yaml:"transfer_policies"`

	// ConsumerTransferPolicies configures this connector's autonomous
	// behavior as transfer consumer, keyed by the agreement a transfer runs
	// under. An agreement with no matching entry sends nothing — see
	// ConsumerTransferPolicy.
	ConsumerTransferPolicies []ConsumerTransferPolicy `yaml:"consumer_transfer_policies"`

	// ParticipantKey is the path to this connector's Ed25519 private key, in
	// PEM. It signs every credential this connector presents to a
	// counterparty (DECISIONS.md section 10). Required when authentication is
	// on. Generate one with `dsops keygen`.
	ParticipantKey string `yaml:"participant_key"`

	// RosterPath is the path to the participant roster — the file that says
	// whose signatures this connector accepts (DECISIONS.md section 9).
	// Required when authentication is on.
	RosterPath string `yaml:"roster"`

	// RosterSigner is the operator's Ed25519 public key (base64url), the one
	// this connector's roster must carry a valid signature from
	// (DECISIONS.md section 9's trust anchor). Not a participant's key: the
	// roster is the registry itself, and signing it with an entry's own key
	// would let that entry vouch for itself. Required when authentication is
	// on. Generate the signing key with `dsops keygen`, sign a roster with
	// `dsops roster sign`.
	RosterSigner string `yaml:"roster_signer"`

	// RequireAuth turns connector-to-connector authentication on. A pointer
	// so an omitted key is distinguishable from an explicit false; nil means
	// the default, which is on — see AuthRequired.
	//
	// It exists for migration, not convenience: switching a running connector
	// from anonymous to authenticated is otherwise a flag day for every
	// counterparty at once. Turning it off is permitted only in dev_mode, so
	// an operator who has not declared this a development instance cannot
	// also declare that anyone may talk to it.
	RequireAuth *bool `yaml:"require_auth"`
}

// AuthRequired reports whether connector-to-connector authentication is on.
// Absent means on: a missing key must never read as "no auth required", the
// same rule MgmtToken already follows.
func (c Config) AuthRequired() bool {
	return c.RequireAuth == nil || *c.RequireAuth
}

// Dataset is one advertised dataset. Only the identifier is configurable: the
// connector synthesizes the offer, the distribution, and the data service.
// Advertising a policy the negotiation code cannot yet enforce would claim
// something untrue, so the configuration grows a policy key when evaluation is
// written, and not before.
type Dataset struct {
	ID string `yaml:"id"`

	// ValidityUntil is the point after which this dataset's offer is no
	// longer valid. Optional: absent means the offer never expires, which is
	// every dataset's behavior before this milestone. This is the second of
	// the two policy shapes DECISIONS.md section 14 permits in v1.
	ValidityUntil *time.Time `yaml:"validity_until"`

	// SourceFile is the file this dataset's bytes come from. Optional: a
	// dataset without one is advertisable, negotiable, and transferable as a
	// control-plane exercise, and its data endpoint answers 409 — the
	// agreement is real and there is simply nothing configured behind it.
	//
	// Checked for existence at load, so a typo fails at boot rather than on
	// the first pull, and read at request time rather than held open, because
	// a file swapped underneath the connector is the operator's business.
	//
	// Note what a transfer_policies sequence does to this. That table is a
	// test affordance whose default, [STARTED], is the configuration that
	// serves data; a sequence naming COMPLETED or TERMINATED ends the
	// transfer on a timer, and ending a transfer ends access to its bytes.
	// A terminal step and a source_file are mutually exclusive in practice.
	SourceFile string `yaml:"source_file"`

	// TerminateOnVerify chooses this dataset's autonomous outcome once a
	// negotiated agreement over it reaches VERIFIED: false (default)
	// finalizes, the behavior every dataset had before this field existed;
	// true terminates instead. DSP 2025-1's own contract negotiation state
	// machine names both VERIFIED -> FINALIZED and VERIFIED -> TERMINATED as
	// legal provider-initiated transitions and gives no wire-observable rule
	// for choosing between them — nothing in a verification message
	// distinguishes one case from the other — so the choice has to be an
	// operator declaration, the same shape ValidityUntil and SourceFile
	// already are for this type. A test affordance in the same sense
	// transfer_policies is: a real deployment has no reason to advertise a
	// dataset whose agreements it always terminates.
	TerminateOnVerify bool `yaml:"terminate_on_verify"`

	// TransferSequence configures this dataset's provider-role autonomous
	// transfer behavior when no transfer_policies entry names the specific
	// agreement — the fallback DECISIONS.md section 25.7 said did not exist:
	// an agreement's dataset_id, unlike its own id, is known before it is
	// negotiated. Same shape and same legality rules as
	// TransferPolicy.Sequence: nil means no fallback (every dataset's
	// behavior before this field existed), an agreement_id-keyed entry in
	// transfer_policies always wins when both match, and an explicit empty
	// sequence means accept and stay in REQUESTED, distinct from nil.
	TransferSequence []string `yaml:"transfer_sequence"`

	// SimulateInterruptAfterBytes truncates a non-Range data pull at this
	// many bytes and severs the connection, for exercising resumption
	// against a real interrupted transfer rather than a mocked one. Zero
	// (default) disables it. A real deployment has no reason to configure
	// this — the same "test affordance" category TerminateOnVerify and
	// transfer_policies are already in. A request that carries Range is
	// never truncated, regardless of this value.
	SimulateInterruptAfterBytes int64 `yaml:"simulate_interrupt_after_bytes"`
}

// ConsumerPolicy selects this connector's autonomous reaction to what a
// provider sends back for a given requested dataset, when this connector is
// negotiating as consumer. Every field left empty here is filled in with
// its default where the policy is looked up (dsp.resolvePolicy), not here —
// this type only validates that a *present* value is one of the values that
// field supports.
type ConsumerPolicy struct {
	DatasetID string `yaml:"dataset_id"`

	// OnOffer: "accept" (default), "passive", "reject", or "counter".
	OnOffer string `yaml:"on_offer"`

	// OnAgreement: "verify" (default) or "reject".
	OnAgreement string `yaml:"on_agreement"`

	// OnIdle: "wait" (default) or "abandon".
	OnIdle string `yaml:"on_idle"`
}

// TransferPolicy configures this connector's autonomous behavior as transfer
// provider, keyed by the agreement a transfer is requested under. Sequence is
// the states it walks on its own after accepting the request, pushing the
// matching message to the consumer's callback address at each step.
//
// An agreement with no entry gets [STARTED]: accept, then start. An entry
// with an empty sequence means accept and stay in REQUESTED — a different
// thing from having no entry, and the only way to say it. An entry always
// overrides the default, whether or not it carries a sequence key: a []string
// cannot distinguish an absent key from an empty list, so an entry written
// without one is read as empty rather than as "use the default".
//
// Validation here checks only that each element names a known state. That the
// *walk* is legal — a completion needs a started transfer, a terminated one
// cannot be restarted — is checked at runtime instead, in dsp.pushTransferStep
// against the same table an inbound message is judged by. It has to be:
// whether a step is legal depends on where the previous step left the
// transfer. An illegal step is refused and ends the sequence.
//
// This is the transfer analogue of ConsumerPolicy, and it exists for the same
// reason: v1 has none of the operational inputs a real provider would use to
// decide to suspend or complete a transfer, so the decision comes from
// configuration instead. See the design spec's "Autonomous provider behavior,
// keyed by agreement".
type TransferPolicy struct {
	AgreementID string   `yaml:"agreement_id"`
	Sequence    []string `yaml:"sequence"`
}

// ConsumerTransferPolicy configures this connector's autonomous behavior as
// transfer consumer, keyed by the agreement the transfer runs under.
// Sequence is the states it drives to on its own once the transfer reaches
// After, pushing the matching message to the provider at each step.
//
// An agreement with no entry sends nothing. That default is the opposite of
// TransferPolicy's [STARTED], and the TCK forces it: eleven of the fifteen
// TP_C tests require this connector to stay passive after its initial
// request, and any volunteered message fails them.
//
// After exists because legality cannot supply the timing. TP_C:02-01 and
// TP_C:02-05 both send exactly one TransferTerminationMessage and differ
// only in when — 02-01 after the provider starts the transfer, 02-05
// straight from REQUESTED — and termination is legal from both states, so a
// driver that fired as soon as its step became legal would send 02-01's
// termination before the provider's start and fail a test meant to pass.
type ConsumerTransferPolicy struct {
	AgreementID string `yaml:"agreement_id"`

	// After is the state whose arrival releases Sequence. Empty means
	// STARTED, filled in at lookup rather than here.
	After string `yaml:"after"`

	Sequence []string `yaml:"sequence"`
}

const (
	defaultDSPAddr  = "0.0.0.0:8080"
	defaultMgmtAddr = "127.0.0.1:8081"
)

const defaultDataIdleTimeout = 60 * time.Second

const defaultMaxDownloadBytes = 8 << 30

// minMgmtTokenLen is the shortest management token accepted. It exists to
// fail an obviously placeholder value at load rather than in production.
const minMgmtTokenLen = 16

// Load parses the YAML document, applies environment overrides, and validates
// the result. Environment values take precedence over the file.
func Load(data []byte, getenv func(string) string) (Config, error) {
	cfg := Config{
		DSPAddr:          defaultDSPAddr,
		MgmtAddr:         defaultMgmtAddr,
		DataIdleTimeout:  defaultDataIdleTimeout,
		MaxDownloadBytes: defaultMaxDownloadBytes,
	}

	// KnownFields rejects a document with an unrecognized key instead of
	// silently dropping it — a typo like "dsp_add" would otherwise load with
	// no error and the connector would listen on the default address instead
	// of the one requested. An empty document is legitimate (every value can
	// come from the environment), and Decode reports that as io.EOF rather
	// than leaving cfg untouched, so it must be excluded from the error path.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if v := getenv("DSBOX_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := getenv("DSBOX_PARTICIPANT_ID"); v != "" {
		cfg.ParticipantID = v
	}
	if v := getenv("DSBOX_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	// datasets has no environment override: a list has no sensible environment
	// representation, and inventing one would be a second configuration syntax.
	if v := getenv("DSBOX_DSP_ADDR"); v != "" {
		cfg.DSPAddr = v
	}
	if v := getenv("DSBOX_MGMT_ADDR"); v != "" {
		cfg.MgmtAddr = v
	}
	if v := getenv("DSBOX_PARTICIPANT_KEY"); v != "" {
		cfg.ParticipantKey = v
	}
	if v := getenv("DSBOX_ROSTER"); v != "" {
		cfg.RosterPath = v
	}
	if v := getenv("DSBOX_ROSTER_SIGNER"); v != "" {
		cfg.RosterSigner = v
	}
	if v := getenv("DSBOX_MGMT_TOKEN"); v != "" {
		cfg.MgmtToken = v
	}
	if v := getenv("DSBOX_DEV_MODE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("DSBOX_DEV_MODE: %w", err)
		}
		cfg.DevMode = b
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.PublicURL == "" {
		return fmt.Errorf("public_url is required: behind a proxy the connector cannot infer its own external address")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return fmt.Errorf("public_url: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("public_url must be absolute, got %q", c.PublicURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !c.DevMode {
			return fmt.Errorf("public_url must use https unless dev_mode is true, got %q", c.PublicURL)
		}
	default:
		return fmt.Errorf("public_url scheme must be http or https, got %q", u.Scheme)
	}
	if strings.HasSuffix(c.PublicURL, "/") {
		return fmt.Errorf("public_url must not end with a slash, got %q", c.PublicURL)
	}
	if c.ParticipantID == "" {
		return fmt.Errorf("participant_id is required: it identifies this participant in every catalog served")
	}
	seen := make(map[string]bool, len(c.Datasets))
	for i, d := range c.Datasets {
		if err := validateDatasetID(d.ID); err != nil {
			return fmt.Errorf("datasets[%d]: %w", i, err)
		}
		if d.SourceFile != "" {
			// Checked here so a typo fails at boot. Not held open: the file
			// is read per request, and a directory is never a dataset.
			info, err := os.Stat(d.SourceFile)
			if err != nil {
				return fmt.Errorf("datasets[%d]: source_file %q: %w", i, d.SourceFile, err)
			}
			if info.IsDir() {
				return fmt.Errorf("datasets[%d]: source_file %q is a directory", i, d.SourceFile)
			}
		}
		if seen[d.ID] {
			return fmt.Errorf("datasets[%d]: duplicate id %q", i, d.ID)
		}
		seen[d.ID] = true
	}
	validOnOffer := map[string]bool{"accept": true, "passive": true, "reject": true, "counter": true}
	validOnAgreement := map[string]bool{"verify": true, "reject": true}
	validOnIdle := map[string]bool{"wait": true, "abandon": true}
	for i, p := range c.ConsumerPolicies {
		if p.DatasetID == "" {
			return fmt.Errorf("consumer_policies[%d]: dataset_id is required", i)
		}
		if p.OnOffer != "" && !validOnOffer[p.OnOffer] {
			return fmt.Errorf("consumer_policies[%d]: on_offer %q is not one of accept, passive, reject, counter", i, p.OnOffer)
		}
		if p.OnAgreement != "" && !validOnAgreement[p.OnAgreement] {
			return fmt.Errorf("consumer_policies[%d]: on_agreement %q is not one of verify, reject", i, p.OnAgreement)
		}
		if p.OnIdle != "" && !validOnIdle[p.OnIdle] {
			return fmt.Errorf("consumer_policies[%d]: on_idle %q is not one of wait, abandon", i, p.OnIdle)
		}
	}
	// The state names are literals rather than the dsp package's constants:
	// dsp imports config, so importing it back would be a cycle. REQUESTED is
	// deliberately absent — it is the state a transfer starts in, not one it
	// can be driven to, and staying there is spelled `sequence: []`.
	validTransferState := map[string]bool{"STARTED": true, "SUSPENDED": true, "COMPLETED": true, "TERMINATED": true}
	for i, p := range c.TransferPolicies {
		if p.AgreementID == "" {
			return fmt.Errorf("transfer_policies[%d]: agreement_id is required", i)
		}
		for j, s := range p.Sequence {
			if !validTransferState[s] {
				return fmt.Errorf("transfer_policies[%d]: sequence[%d] %q is not one of STARTED, SUSPENDED, COMPLETED, TERMINATED", i, j, s)
			}
		}
	}
	for i, d := range c.Datasets {
		if d.SimulateInterruptAfterBytes < 0 {
			return fmt.Errorf("datasets[%d]: simulate_interrupt_after_bytes must not be negative", i)
		}
		for j, s := range d.TransferSequence {
			if !validTransferState[s] {
				return fmt.Errorf("datasets[%d]: transfer_sequence[%d] %q is not one of STARTED, SUSPENDED, COMPLETED, TERMINATED", i, j, s)
			}
		}
	}
	// After may name REQUESTED, which validTransferState omits because a
	// transfer cannot be *driven* there. It may not name a terminal state:
	// every legality predicate refuses a terminal from-state, so a sequence
	// released from one could never send anything, and loading it cleanly
	// would accept a constraint that is never enforced.
	validAfterState := map[string]bool{"REQUESTED": true, "STARTED": true, "SUSPENDED": true}
	for i, p := range c.ConsumerTransferPolicies {
		if p.AgreementID == "" {
			return fmt.Errorf("consumer_transfer_policies[%d]: agreement_id is required", i)
		}
		if p.After != "" && !validAfterState[p.After] {
			return fmt.Errorf("consumer_transfer_policies[%d]: after %q is not one of REQUESTED, STARTED, SUSPENDED", i, p.After)
		}
		for j, s := range p.Sequence {
			if !validTransferState[s] {
				return fmt.Errorf("consumer_transfer_policies[%d]: sequence[%d] %q is not one of STARTED, SUSPENDED, COMPLETED, TERMINATED", i, j, s)
			}
		}
	}
	if !c.AuthRequired() && !c.DevMode {
		return fmt.Errorf("require_auth: false is only permitted with dev_mode: true — " +
			"a connector that is not declared a development instance may not accept unauthenticated counterparties")
	}
	if c.AuthRequired() {
		if c.ParticipantKey == "" {
			return fmt.Errorf("participant_key is required when authentication is on: it signs every credential this connector presents")
		}
		if c.RosterPath == "" {
			return fmt.Errorf("roster is required when authentication is on: it says whose signatures this connector accepts")
		}
		if c.RosterSigner == "" {
			return fmt.Errorf("roster_signer is required when authentication is on: it is what the roster's signature must verify against")
		}
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required: it is where the negotiation state database lives")
	}
	if c.MgmtToken != "" && len(c.MgmtToken) < minMgmtTokenLen {
		return fmt.Errorf("mgmt_token must be at least %d characters", minMgmtTokenLen)
	}
	return nil
}

// validateDatasetID enforces the two properties a dataset identifier must have.
//
// It is an @id, so it must be an absolute IRI: a relative identifier's fate
// under JSON-LD expansion depends on a document base the TCK never sets. It is
// also routed on directly as a single path segment, so any character that would
// split or truncate a path is rejected. A urn: name satisfies both; an http URL
// identifier does not, and is rejected deliberately.
func validateDatasetID(id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	u, err := url.Parse(id)
	if err != nil {
		return fmt.Errorf("id %q: %w", id, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("id must be an absolute IRI, got %q", id)
	}
	if strings.ContainsAny(id, "/?# \t\n") {
		return fmt.Errorf("id must be a single URL path segment with no /, ?, # or whitespace, got %q", id)
	}
	return nil
}
