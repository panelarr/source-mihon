// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"fmt"
	"net/url"
	"strings"

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
    }
  },
  "required": ["mihon_source_url"],
  "additionalProperties": false
}`

// Settings is the configuration in effect.
type Settings struct {
	URL        string
	APIKey     string
	Stores     []StoreSetting
	Extensions []string
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
