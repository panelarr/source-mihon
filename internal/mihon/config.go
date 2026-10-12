// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/panelarr/source-api/sdk/settings"
)

// DefaultURL is where mihon-source runs in the usual compose setup.
const DefaultURL = "http://mihon-source:8902"

// configSchema is the configuration the plugin asks its host for.
const configSchema = `{
  "type": "object",
  "title": "Mihon extensions",
  "description": "Where mihon-source runs, and the extension stores and extensions to keep there.",
  "properties": {
    "mihon_source_url": {
      "type": "string",
      "format": "uri",
      "title": "mihon-source URL",
      "description": "Where mihon-source serves its API, e.g. http://mihon-source:8902.",
      "default": "` + DefaultURL + `"
    },
    "api_key": {
      "type": "string",
      "title": "mihon-source API key",
      "description": "A mihon-source client key with the sources:read and sources:write scopes, plus admin to manage stores and extensions (or mihon-source's admin token). Kept secret: it is never shown again.",
      "writeOnly": true
    },
    "languages": {
      "type": "array",
      "title": "Languages",
      "description": "Languages to offer sources in, e.g. en, fr. Empty offers every language. \"all\" and \"multi\" are languages too: list them to keep multi-language sources. Each extension can narrow this further.",
      "items": {"type": "string", "title": "Language", "description": "A language tag as sources use it, e.g. en, pt-BR, all."},
      "default": []
    },
    "stores": {
      "type": "array",
      "title": "Extension stores",
      "description": "Stores to add to mihon-source. Stores added there by other means are left alone.",
      "items": {
        "type": "object",
        "title": "Store",
        "description": "An extension store to add to mihon-source.",
        "properties": {
          "url": {"type": "string", "format": "uri", "title": "Store URL", "description": "The store's repo.json URL (or the URL of a legacy index)."},
          "signing_key_fingerprint": {"type": "string", "title": "Signing key fingerprint", "description": "Optional. The SHA-256 of the store's signing certificate, which pins the store's key from the start."}
        },
        "required": ["url"],
        "additionalProperties": false
      },
      "default": []
    },
    "extensions": {
      "type": "array",
      "title": "Extensions",
      "description": "Package names of extensions to install from the stores when they're missing. Updates are installed in mihon-source.",
      "items": {"type": "string", "title": "Extension", "description": "The extension's package name, as the store lists it."},
      "default": []
    },
    "health_checks": {
      "type": "object",
      "title": "Source health checks",
      "description": "How often mihon-source checks its sources on its own. The checks cover the languages above. Turn checks off for one source on the source itself.",
      "properties": {
        "interval": {
          "type": "string",
          "title": "Interval",
          "description": "How often, e.g. 6h or 1d (m, h or d). off: no scheduled checks. default: mihon-source's own setting (its MIHON_HEALTH_CHECK_* variables). Empty leaves mihon-source as it is.",
          "pattern": "^(|off|default|[1-9][0-9]*[mhd])$",
          "default": ""
        },
        "sample": {
          "type": "integer",
          "title": "Sources per round",
          "description": "How many sources each round checks, the least recently checked first, so rounds rotate through all of them. 0 checks every source each round.",
          "minimum": 0,
          "default": 10
        }
      },
      "additionalProperties": false,
      "default": {"interval": "", "sample": 10}
    }
  },
  "required": ["mihon_source_url"],
  "additionalProperties": false
}`

// Settings is the configuration in effect.
type Settings struct {
	URL    string
	APIKey string
	// Langs are the languages to offer sources in; empty means all.
	Langs      []string
	Stores     []StoreSetting
	Extensions []string
	// HealthInterval is health_checks.interval: "", "off", "default" or a
	// number and unit (m, h, d).
	HealthInterval string
	// HealthSample is health_checks.sample; 0 means all.
	HealthSample int
}

// healthEvery is the health-check interval asked for; zero for "off".
func (s *Settings) healthEvery() time.Duration {
	v := s.HealthInterval
	if len(v) < 2 {
		return 0
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil {
		return 0
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[v[len(v)-1]]
	return time.Duration(n) * unit
}

// StoreSetting is one store to keep added.
type StoreSetting struct {
	URL         string
	Fingerprint string
}

func schema() *settings.Schema {
	s, err := settings.Parse(configSchema)
	if err != nil {
		panic(err) // a constant; tests parse it
	}
	return s
}

// parseSettings reads validated configuration values.
func parseSettings(v map[string]any) (Settings, error) {
	s := Settings{URL: strings.TrimRight(str(v["mihon_source_url"]), "/"), APIKey: str(v["api_key"])}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Settings{}, fmt.Errorf("mihon_source_url must be an http:// or https:// URL")
	}
	stores, _ := v["stores"].([]any)
	for _, raw := range stores {
		m, _ := raw.(map[string]any)
		s.Stores = append(s.Stores, StoreSetting{URL: strings.TrimSpace(str(m["url"])), Fingerprint: strings.TrimSpace(str(m["signing_key_fingerprint"]))})
	}
	langs, _ := v["languages"].([]any)
	for _, raw := range langs {
		if l := strings.TrimSpace(str(raw)); l != "" && !slices.Contains(s.Langs, l) {
			s.Langs = append(s.Langs, l)
		}
	}
	hc, _ := v["health_checks"].(map[string]any)
	s.HealthInterval = strings.TrimSpace(str(hc["interval"]))
	if n, ok := hc["sample"].(float64); ok && n > 0 {
		s.HealthSample = int(n)
	}
	exts, _ := v["extensions"].([]any)
	for _, raw := range exts {
		if pkg := strings.TrimSpace(str(raw)); pkg != "" {
			s.Extensions = append(s.Extensions, pkg)
		}
	}
	return s, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// offers says whether a source language is among the plugin's languages.
func (s *Settings) offers(lang string) bool {
	return s == nil || len(s.Langs) == 0 || slices.Contains(s.Langs, lang)
}
