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

	// DevMode permits a plain http PublicURL. The local demo and the TCK
	// harness run without a proxy; nothing else should set this.
	DevMode bool `yaml:"dev_mode"`

	// DSPAddr is the listen address for public DSP endpoints.
	DSPAddr string `yaml:"dsp_addr"`

	// MgmtAddr is the listen address for the management API. It binds to
	// localhost by default so a firewall mistake cannot expose it.
	MgmtAddr string `yaml:"mgmt_addr"`
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
	return nil
}
