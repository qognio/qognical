package intake

import (
	"encoding/json"
	"testing"
)

const sampleSchema = `{
  "fields": [
    {"key":"company","label":"Firma","type":"text","required":true,"max_length":200},
    {"key":"topic","label":"Worum","type":"select","required":true,"options":["Erst","Best"]},
    {"key":"tags","label":"Tags","type":"multiselect","options":["a","b","c"]},
    {"key":"agreed","label":"AGB","type":"checkbox","required":true},
    {"key":"email","label":"Mail","type":"email","required":true},
    {"key":"count","label":"Anzahl","type":"number","min":1,"max":50},
    {"key":"day","label":"Datum","type":"date"}
  ]
}`

func mustSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseSchema([]byte(sampleSchema))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidPayload(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": "ACME",
		"topic":   "Erst",
		"tags":    []any{"a", "c"},
		"agreed":  true,
		"email":   "x@example.com",
		"count":   float64(10),
		"day":     "2026-07-04",
	}
	if err := s.Validate(d); err != nil {
		t.Errorf("expected ok, got %v", err)
	}
}

// FRM-1: missing required field rejected.
func TestRequiredMissing(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{"topic": "Erst", "agreed": true, "email": "x@example.com"}
	err := s.Validate(d)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(Errors)["company"]; !ok {
		t.Errorf("missing required field error not surfaced: %v", err)
	}
}

// FRM-3: oversize input rejected.
func TestMaxLength(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": string(make([]byte, 300)),
		"topic":   "Erst",
		"agreed":  true,
		"email":   "x@example.com",
	}
	err := s.Validate(d)
	if err == nil || err.(Errors)["company"] == "" {
		t.Errorf("oversize not rejected: %v", err)
	}
}

// FRM-5: select with value outside options rejected.
func TestSelectNotInOptions(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": "ACME",
		"topic":   "BogusTopic",
		"agreed":  true,
		"email":   "x@example.com",
	}
	err := s.Validate(d)
	if err == nil || err.(Errors)["topic"] == "" {
		t.Errorf("invalid select not rejected: %v", err)
	}
}

// FRM-6: multiselect duplicates rejected.
func TestMultiselectDuplicate(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": "ACME",
		"topic":   "Erst",
		"tags":    []any{"a", "a"},
		"agreed":  true,
		"email":   "x@example.com",
	}
	err := s.Validate(d)
	if err == nil || err.(Errors)["tags"] == "" {
		t.Errorf("duplicate multiselect not rejected")
	}
}

// Unknown field rejected (anti-smuggling).
func TestUnknownFieldRejected(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": "ACME",
		"topic":   "Erst",
		"agreed":  true,
		"email":   "x@example.com",
		"haxx":    "<script>",
	}
	if err := s.Validate(d); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

// FRM-8: date format enforced.
func TestDateFormat(t *testing.T) {
	s := mustSchema(t)
	d := map[string]any{
		"company": "ACME",
		"topic":   "Erst",
		"agreed":  true,
		"email":   "x@example.com",
		"day":     "07/04/2026",
	}
	err := s.Validate(d)
	if err == nil || err.(Errors)["day"] == "" {
		t.Errorf("loose date not rejected: %v", err)
	}
}

func TestSchemaShapeErrors(t *testing.T) {
	cases := []string{
		`{"fields":[{"key":"x","type":"unknown"}]}`,
		`{"fields":[{"key":"X","type":"text"}]}`,   // uppercase key
		`{"fields":[{"key":"x","type":"select"}]}`, // missing options
		`{"fields":[{"key":"x","type":"text","pattern":"["}]}`,
	}
	for _, c := range cases {
		if _, err := ParseSchema([]byte(c)); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestEmptySchemaAllowsAnything(t *testing.T) {
	s, _ := ParseSchema(nil)
	if err := s.Validate(map[string]any{"x": "y"}); err == nil {
		t.Errorf("empty schema should still reject unknown keys; got nil")
	}
}

// Re-marshal round-trip sanity.
func TestRoundTripJSON(t *testing.T) {
	s, _ := ParseSchema([]byte(sampleSchema))
	b, _ := json.Marshal(s)
	s2, err := ParseSchema(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Fields) != len(s.Fields) {
		t.Errorf("field count mismatch")
	}
}
