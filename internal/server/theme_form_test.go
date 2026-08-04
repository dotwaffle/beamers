package server

import (
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

// TestThemeFormFieldsCoverConfig fails when a Config field gains no form
// field, or two form fields write the same Config field.
func TestThemeFormFieldsCoverConfig(t *testing.T) {
	t.Parallel()
	var config themevalue.Config
	names := map[string]bool{}
	for index, field := range themeFormFields {
		if names[field.name] {
			t.Errorf("form field %q appears twice", field.name)
		}
		names[field.name] = true
		target := field.access(&config)
		if *target != "" {
			t.Errorf("form field %q writes an already claimed Config field", field.name)
		}
		*target = fmt.Sprintf("value-%d", index)
	}
	value := reflect.ValueOf(config)
	for index := range value.NumField() {
		field := value.Field(index)
		if field.Kind() != reflect.String {
			t.Errorf("Config field %s is not a string; extend the codec table and this test",
				value.Type().Field(index).Name)
			continue
		}
		if field.String() == "" {
			t.Errorf("Config field %s has no form field", value.Type().Field(index).Name)
		}
	}
}

// TestThemeFormRoundTrip proves every codec field is accepted by both
// validators and survives decode plus re-encode unchanged.
func TestThemeFormRoundTrip(t *testing.T) {
	t.Parallel()
	form := url.Values{}
	for index, field := range themeFormFields {
		form.Set(field.name, fmt.Sprintf("value-%d", index))
	}
	if err := validateThemeForm(form); err != nil {
		t.Fatalf("validateThemeForm: %v", err)
	}
	if err := validateEventThemeForm(form); err != nil {
		t.Fatalf("validateEventThemeForm: %v", err)
	}
	applied := url.Values{}
	applyThemeConfig(applied, themeConfig(form))
	if !reflect.DeepEqual(form, applied) {
		t.Errorf("round trip mismatch:\nform:    %v\napplied: %v", form, applied)
	}
}

func TestValidateThemeFormRejections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		form url.Values
	}{
		{"unknown field", url.Values{"unexpected": {"value"}}},
		{"repeated value", url.Values{"background_color": {"#000000", "#ffffff"}}},
		{"variant field on Installation form", url.Values{
			themevalue.VariantTimeline + "_accent_color": {"#123456"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateThemeForm(test.form); err == nil {
				t.Error("validateThemeForm accepted an invalid form")
			}
		})
	}
}
