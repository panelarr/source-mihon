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
