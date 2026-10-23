package tools

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSummarizeCheckout(t *testing.T) {
	body := `{
	  "kassaKoopjes": [{"a":1}],
	  "missingBonus": [{"title":"Bonus box","webshopId":42}],
	  "nonChosen": [{"product":{"webshopId":7,"title":"Melk"}}],
	  "nonDeliverables": [
	    {"product":{"webshopId":1234,"title":"Verse tonijn"},"reason":"Niet bezorgbaar op gekozen dag"},
	    {"unexpected":"shape"}
	  ],
	  "recommendedProducts": [{},{}],
	  "samples": [],
	  "showMakeCompleet": true
	}`
	got, err := summarizeCheckout([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	out, _ := json.Marshal(got)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}

	nd := v["non_deliverables"].([]any)
	if len(nd) != 2 {
		t.Fatalf("want 2 non_deliverables, got %d", len(nd))
	}
	first := nd[0].(map[string]any)
	if first["product_id"] != float64(1234) || first["title"] != "Verse tonijn" || first["reason"] != "Niet bezorgbaar op gekozen dag" {
		t.Errorf("fields not extracted: %v", first)
	}
	if _, ok := first["raw"].(map[string]any)["product"]; !ok {
		t.Errorf("raw element lost: %v", first["raw"])
	}
	second := nd[1].(map[string]any)
	if _, has := second["product_id"]; has {
		t.Errorf("unexpected product_id on unknown shape: %v", second)
	}
	if second["raw"].(map[string]any)["unexpected"] != "shape" {
		t.Errorf("unknown element not passed through: %v", second)
	}

	mb := v["missing_bonus"].([]any)[0].(map[string]any)
	if mb["product_id"] != float64(42) || mb["title"] != "Bonus box" {
		t.Errorf("top-level fields not extracted: %v", mb)
	}
	nc := v["non_chosen"].([]any)[0].(map[string]any)
	if nc["product_id"] != float64(7) || nc["title"] != "Melk" {
		t.Errorf("nested product fields not extracted: %v", nc)
	}

	if v["kassa_koopjes_count"] != float64(1) || v["recommended_products_count"] != float64(2) || v["samples_count"] != float64(0) {
		t.Errorf("counts wrong: %v", v)
	}
	if v["show_make_compleet"] != true || v["non_deliverable_count"] != float64(2) {
		t.Errorf("flags wrong: %v", v)
	}
}

func TestSummarizeCheckoutEmptyListsStayLists(t *testing.T) {
	got, err := summarizeCheckout([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(got)
	var v map[string]any
	_ = json.Unmarshal(out, &v)
	for _, k := range []string{"non_deliverables", "missing_bonus", "non_chosen"} {
		l, ok := v[k].([]any)
		if !ok || len(l) != 0 {
			t.Errorf("%s should be an empty list, got %v", k, v[k])
		}
	}
}

func TestSummarizeCheckoutRejectsGarbage(t *testing.T) {
	if _, err := summarizeCheckout([]byte(`not json`)); err == nil {
		t.Fatal("want error")
	}
}

func TestIsNotInOrderMode(t *testing.T) {
	// The appie client turns the 412 body {"message":"Server not in order mode"} into this error text.
	if !isNotInOrderMode(errors.New("get order failed: Order does not exist: Order should not be null")) {
		t.Error("no active order should count as not in order mode")
	}
	if !isNotInOrderMode(errors.New("Server not in order mode")) {
		t.Error("412 message not recognised")
	}
	if isNotInOrderMode(errors.New("API error: 500 boom")) || isNotInOrderMode(nil) {
		t.Error("unrelated errors misclassified")
	}
}
