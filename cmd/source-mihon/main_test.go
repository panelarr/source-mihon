// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFromEnvironment(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(secret, []byte("msk_from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIHON_SOURCE_URL", "http://mihon:8902")
	t.Setenv("MIHON_SOURCE_API_KEY", "")
	t.Setenv("MIHON_SOURCE_API_KEY_FILE", secret)
	got := fromEnv()
	if got["mihon_source_url"] != "http://mihon:8902" || got["api_key"] != "msk_from-file" {
		t.Fatalf("from the environment: %v", got)
	}
	t.Setenv("MIHON_SOURCE_API_KEY", "direct")
	if fromEnv()["api_key"] != "direct" {
		t.Fatal("the variable should win over the file")
	}
}
