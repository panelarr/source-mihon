// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import "testing"

// Hosts render the schema as a form, so every field, and every item of a list, needs a
// human title and description.
func TestConfigSchemaLabelsEveryField(t *testing.T) {
	var walk func(path string, node map[string]any)
	walk = func(path string, node map[string]any) {
		if s, _ := node["title"].(string); s == "" {
			t.Errorf("%s has no title", path)
		}
		if s, _ := node["description"].(string); s == "" {
			t.Errorf("%s has no description", path)
		}
		props, _ := node["properties"].(map[string]any)
		for name, p := range props {
			walk(path+"."+name, p.(map[string]any))
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(path+"[]", items)
		}
	}
	walk("config", schema().Doc())
	if title := schema().Doc()["properties"].(map[string]any)["stores"].(map[string]any)["items"].(map[string]any)["title"]; title != "Store" {
		t.Fatalf("store items are titled %v", title)
	}
}

// The form shows the fields in the order they're written here, which the SDK
// stamps as x-order; the schema reaches the host as a Struct, which sorts keys.
func TestConfigSchemaKeepsTheWrittenOrder(t *testing.T) {
	doc := schema().Doc()
	props := doc["properties"].(map[string]any)
	order := func(m map[string]any, name string) any { return m[name].(map[string]any)["x-order"] }
	for i, name := range []string{"mihon_source_url", "api_key", "languages", "stores", "extensions"} {
		if got := order(props, name); got != float64(i+1) {
			t.Errorf("%s: x-order %v, want %d", name, got, i+1)
		}
	}
	store := props["stores"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if order(store, "url") != float64(1) || order(store, "signing_key_fingerprint") != float64(2) {
		t.Errorf("store fields: %v", store)
	}
}
