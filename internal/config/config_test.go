package config

import "testing"

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load([]byte("public_url: https://connector.example.org\n"), env(nil))
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
	_, err := Load([]byte("dev_mode: true\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when public_url is absent")
	}
}

func TestLoadRejectsPlainHTTPOutsideDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://connector.example.org\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for http without dev_mode")
	}
}

func TestLoadAllowsPlainHTTPInDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://dsbox:8080\ndev_mode: true\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsRelativePublicURL(t *testing.T) {
	_, err := Load([]byte("public_url: /dsp\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a non-absolute public_url")
	}
}

func TestLoadRejectsTrailingSlash(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org/\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a trailing slash in public_url")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\n"),
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
		[]byte("public_url: https://connector.example.org\n"),
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
