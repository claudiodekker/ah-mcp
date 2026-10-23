package tools

import (
	"encoding/json"
	"testing"
)

func marshalled(t *testing.T, it listPatchItem) map[string]any {
	t.Helper()
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRemovalItemFillsEverythingTheEndpointRequires(t *testing.T) {
	m := marshalled(t, removalItem(42, "", "Halfvolle melk", "", ""))
	want := map[string]any{
		"description": "Halfvolle melk", "productId": float64(42), "quantity": float64(0),
		"type": "SHOPPABLE", "originCode": "PRD", "strikeThrough": false,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
}

func TestRemovalItemKeepsServerValues(t *testing.T) {
	m := marshalled(t, removalItem(0, "bier", "", "TXT", "TXT"))
	if m["description"] != "bier" || m["type"] != "TXT" || m["originCode"] != "TXT" {
		t.Errorf("server values overwritten: %v", m)
	}
	if _, has := m["productId"]; has {
		t.Errorf("productId sent for a free-text item: %v", m)
	}
}

func TestRemovalItemSendsEmptyDescriptionRatherThanDroppingIt(t *testing.T) {
	m := marshalled(t, removalItem(1, "", "", "", ""))
	if d, ok := m["description"]; !ok || d != "" {
		t.Errorf("description must be present: %v", m)
	}
}
