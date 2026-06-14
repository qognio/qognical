// Package intake is the deliberately small JSON-schema-lite validator for the
// event_types.intake_schema field. We don't use a full JSON-Schema library
// because the planning docs reject it as scope creep — instead we support
// only the 9 field types in Doc 04.
//
// The schema lives as a JSON document with shape:
//
//	{ "fields": [
//	    { "key": "...", "label": "...", "type": "...",
//	      "required": true, ... type-specific options },
//	    ...
//	  ] }
package intake

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FieldType enumerates the supported intake field types from Doc 04.
type FieldType string

const (
	TypeText        FieldType = "text"
	TypeTextarea    FieldType = "textarea"
	TypeSelect      FieldType = "select"
	TypeMultiselect FieldType = "multiselect"
	TypeCheckbox    FieldType = "checkbox"
	TypeTel         FieldType = "tel"
	TypeEmail       FieldType = "email"
	TypeNumber      FieldType = "number"
	TypeDate        FieldType = "date"
)

// Field is a single intake input definition.
type Field struct {
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	Type      FieldType `json:"type"`
	Required  bool      `json:"required,omitempty"`
	MaxLength int       `json:"max_length,omitempty"`
	Pattern   string    `json:"pattern,omitempty"`
	Options   []string  `json:"options,omitempty"` // for select/multiselect
	Min       *float64  `json:"min,omitempty"`     // for number
	Max       *float64  `json:"max,omitempty"`     // for number
}

// Schema is the top-level shape of intake_schema.
type Schema struct {
	Fields []Field `json:"fields"`
}

// ParseSchema validates and returns a Schema from a JSON document. We also
// catch shape errors (unknown type, missing options on a select, etc.) so
// the host UI can refuse to save a broken schema rather than producing it
// at booking time.
func ParseSchema(raw []byte) (*Schema, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return &Schema{}, nil
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("intake schema: %w", err)
	}
	seen := map[string]bool{}
	keyRe := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for i, f := range s.Fields {
		if !keyRe.MatchString(f.Key) {
			return nil, fmt.Errorf("field %d: key %q must match [a-z][a-z0-9_]*", i, f.Key)
		}
		if seen[f.Key] {
			return nil, fmt.Errorf("field %d: duplicate key %q", i, f.Key)
		}
		seen[f.Key] = true
		switch f.Type {
		case TypeText, TypeTextarea, TypeTel, TypeEmail, TypeNumber, TypeDate, TypeCheckbox:
		case TypeSelect, TypeMultiselect:
			if len(f.Options) == 0 {
				return nil, fmt.Errorf("field %q: %s needs options", f.Key, f.Type)
			}
		default:
			return nil, fmt.Errorf("field %q: unknown type %q", f.Key, f.Type)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				return nil, fmt.Errorf("field %q: bad regex: %w", f.Key, err)
			}
		}
	}
	return &s, nil
}

// Errors is a multi-field validation result, structured for the public API.
type Errors map[string]string

func (e Errors) Error() string {
	parts := make([]string, 0, len(e))
	for k, v := range e {
		parts = append(parts, fmt.Sprintf("%s: %s", k, v))
	}
	return strings.Join(parts, "; ")
}

// Validate checks the user-submitted intake against the schema. Returns a
// non-nil Errors map (len > 0) if any field failed.
func (s *Schema) Validate(data map[string]any) error {
	errs := Errors{}
	for _, f := range s.Fields {
		raw, present := data[f.Key]
		if !present || isZero(raw) {
			if f.Required {
				errs[f.Key] = "required"
			}
			continue
		}
		if err := f.validateValue(raw); err != nil {
			errs[f.Key] = err.Error()
		}
	}
	// Reject unknown fields to discourage payload smuggling.
	known := map[string]bool{}
	for _, f := range s.Fields {
		known[f.Key] = true
	}
	for k := range data {
		if !known[k] {
			errs[k] = "unknown field"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func (f Field) validateValue(v any) error {
	switch f.Type {
	case TypeText, TypeTextarea, TypeTel:
		s, ok := v.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if f.MaxLength > 0 && len(s) > f.MaxLength {
			return fmt.Errorf("max length %d", f.MaxLength)
		}
		if f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(s) {
			return errors.New("pattern mismatch")
		}
	case TypeEmail:
		s, ok := v.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if _, err := mail.ParseAddress(s); err != nil {
			return errors.New("not a valid email")
		}
	case TypeNumber:
		n, err := toFloat(v)
		if err != nil {
			return err
		}
		if f.Min != nil && n < *f.Min {
			return fmt.Errorf("min %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return fmt.Errorf("max %v", *f.Max)
		}
	case TypeDate:
		s, ok := v.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return errors.New("expected YYYY-MM-DD")
		}
	case TypeCheckbox:
		if _, ok := v.(bool); !ok {
			return errors.New("must be true or false")
		}
	case TypeSelect:
		s, ok := v.(string)
		if !ok {
			return errors.New("must be a string")
		}
		for _, o := range f.Options {
			if s == o {
				return nil
			}
		}
		return errors.New("not in options")
	case TypeMultiselect:
		arr, ok := v.([]any)
		if !ok {
			return errors.New("must be a list")
		}
		seen := map[string]bool{}
		for _, item := range arr {
			s, ok := item.(string)
			if !ok {
				return errors.New("items must be strings")
			}
			if seen[s] {
				return errors.New("duplicate selection")
			}
			seen[s] = true
			found := false
			for _, o := range f.Options {
				if s == o {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%q not in options", s)
			}
		}
	}
	return nil
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, errors.New("must be a number")
		}
		return f, nil
	}
	return 0, errors.New("must be a number")
}
