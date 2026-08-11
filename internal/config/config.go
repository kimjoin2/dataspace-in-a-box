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
	"strconv"
	"strings"

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
}

// Dataset is one advertised dataset. Only the identifier is configurable: the
// connector synthesizes the offer, the distribution, and the data service.
// Advertising a policy the negotiation code cannot yet enforce would claim
// something untrue, so the configuration grows a policy key when evaluation is
// written, and not before.
type Dataset struct {
	ID string `yaml:"id"`
}

const (
	defaultDSPAddr  = "0.0.0.0:8080"
	defaultMgmtAddr = "127.0.0.1:8081"
)

// Load parses the YAML document, applies environment overrides, and validates
// the result. Environment values take precedence over the file.
func Load(data []byte, getenv func(string) string) (Config, error) {
	cfg := Config{DSPAddr: defaultDSPAddr, MgmtAddr: defaultMgmtAddr}

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
	// datasets has no environment override: a list has no sensible environment
	// representation, and inventing one would be a second configuration syntax.
	if v := getenv("DSBOX_DSP_ADDR"); v != "" {
		cfg.DSPAddr = v
	}
	if v := getenv("DSBOX_MGMT_ADDR"); v != "" {
		cfg.MgmtAddr = v
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
		if seen[d.ID] {
			return fmt.Errorf("datasets[%d]: duplicate id %q", i, d.ID)
		}
		seen[d.ID] = true
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
