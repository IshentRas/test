package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is loaded from YAML (CONFIG_FILE or ./config.yaml) with env overrides.
type Config struct {
	Listen          string
	PublicURL       string
	CoderURL        string
	OwnerToken      string
	DefaultTemplate string
	OIDCIssuer      string
	OIDCAudience    string
	OIDCJWKSURL     string
	EmailClaim      string
	PortalOrigins   []string
}

type fileConfig struct {
	Listen        string   `yaml:"listen"`
	PublicURL     string   `yaml:"public_url"`
	PortalOrigins []string `yaml:"portal_origins"`
	Coder         struct {
		URL             string `yaml:"url"`
		OwnerToken      string `yaml:"owner_token"`
		DefaultTemplate string `yaml:"default_template"`
	} `yaml:"coder"`
	IDP struct {
		Issuer     string `yaml:"issuer"`
		Audience   string `yaml:"audience"`
		JWKSURL    string `yaml:"jwks_url"`
		EmailClaim string `yaml:"email_claim"`
	} `yaml:"idp"`
}

func loadConfig() (Config, error) {
	cfg := Config{
		Listen:          ":8081",
		PublicURL:       "http://localhost:8081",
		CoderURL:        "http://localhost:3000",
		EmailClaim:      "email",
		PortalOrigins:   []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		OIDCIssuer:      "http://localhost:8080/realms/coder-lab",
	}

	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		for _, candidate := range []string{"config.yaml", "../config.yaml"} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		var fc fileConfig
		if err := yaml.Unmarshal(raw, &fc); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		if fc.Listen != "" {
			cfg.Listen = fc.Listen
		}
		if fc.PublicURL != "" {
			cfg.PublicURL = fc.PublicURL
		}
		if len(fc.PortalOrigins) > 0 {
			cfg.PortalOrigins = fc.PortalOrigins
		}
		if fc.Coder.URL != "" {
			cfg.CoderURL = fc.Coder.URL
		}
		if fc.Coder.OwnerToken != "" {
			cfg.OwnerToken = fc.Coder.OwnerToken
		}
		if fc.Coder.DefaultTemplate != "" {
			cfg.DefaultTemplate = fc.Coder.DefaultTemplate
		}
		if fc.IDP.Issuer != "" {
			cfg.OIDCIssuer = fc.IDP.Issuer
		}
		if fc.IDP.Audience != "" {
			cfg.OIDCAudience = fc.IDP.Audience
		}
		if fc.IDP.JWKSURL != "" {
			cfg.OIDCJWKSURL = fc.IDP.JWKSURL
		}
		if fc.IDP.EmailClaim != "" {
			cfg.EmailClaim = fc.IDP.EmailClaim
		}
	}

	// Env overrides (secrets and common ops knobs).
	if v := os.Getenv("LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("MIDDLE_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := os.Getenv("CODER_URL"); v != "" {
		cfg.CoderURL = v
	}
	if v := os.Getenv("CODER_OWNER_TOKEN"); v != "" {
		cfg.OwnerToken = v
	}
	if v := os.Getenv("DEFAULT_TEMPLATE"); v != "" {
		cfg.DefaultTemplate = v
	}
	if v := os.Getenv("OIDC_ISSUER_URL"); v != "" {
		cfg.OIDCIssuer = v
	}
	if v := os.Getenv("OIDC_AUDIENCE"); v != "" {
		cfg.OIDCAudience = v
	}
	if v := os.Getenv("OIDC_JWKS_URL"); v != "" {
		cfg.OIDCJWKSURL = v
	}
	if v := os.Getenv("OIDC_EMAIL_CLAIM"); v != "" {
		cfg.EmailClaim = v
	}

	cfg.CoderURL = strings.TrimRight(cfg.CoderURL, "/")
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	cfg.OIDCIssuer = strings.TrimRight(cfg.OIDCIssuer, "/")
	if cfg.OIDCJWKSURL == "" && cfg.OIDCIssuer != "" {
		cfg.OIDCJWKSURL = cfg.OIDCIssuer + "/protocol/openid-connect/certs"
	}
	if cfg.EmailClaim == "" {
		cfg.EmailClaim = "email"
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	if c.CoderURL == "" {
		return fmt.Errorf("coder.url / CODER_URL is required")
	}
	if c.OwnerToken == "" {
		return fmt.Errorf("coder.owner_token / CODER_OWNER_TOKEN is required")
	}
	if c.OIDCIssuer == "" {
		return fmt.Errorf("idp.issuer / OIDC_ISSUER_URL is required")
	}
	if c.OIDCJWKSURL == "" {
		return fmt.Errorf("idp.jwks_url / OIDC_JWKS_URL is required (or set idp.issuer)")
	}
	if c.PublicURL == "" {
		return fmt.Errorf("public_url / MIDDLE_PUBLIC_URL is required")
	}
	if len(c.PortalOrigins) == 0 {
		return fmt.Errorf("portal_origins must include at least one origin")
	}
	return nil
}

func (c Config) allowedOrigin(origin string) bool {
	for _, o := range c.PortalOrigins {
		if o == origin {
			return true
		}
	}
	return false
}
