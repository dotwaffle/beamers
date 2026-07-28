package themevalue

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDraftRejectsUndocumentedThemeControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		amend func(*Config)
		field string
	}{
		{
			name: "CSS color",
			amend: func(config *Config) {
				config.BackgroundColor = "url(https://example.invalid/payload.css)"
			},
			field: "background_color",
		},
		{
			name: "font upload",
			amend: func(config *Config) {
				config.Typeface = "url(font.woff2)"
			},
			field: "typeface",
		},
		{
			name: "scripted transition",
			amend: func(config *Config) {
				config.Transition = "javascript:alert(1)"
			},
			field: "transition",
		},
		{
			name: "unknown effect",
			amend: func(config *Config) {
				config.Effect = "fireworks"
			},
			field: "effect",
		},
		{
			name: "unknown motion",
			amend: func(config *Config) {
				config.Motion = "unbounded"
			},
			field: "motion",
		},
		{
			name: "unknown branding asset",
			amend: func(config *Config) {
				config.BrandAsset = "uploaded-logo.svg"
			},
			field: "brand_asset",
		},
		{
			name: "unknown background",
			amend: func(config *Config) {
				config.Background = "uploaded-image"
			},
			field: "background",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := DefaultConfig()
			test.amend(&config)
			err := ValidateDraft(config)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateDraft() error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Errorf("validation field = %q, want %q", validation.Field, test.field)
			}
		})
	}
}

func TestActivationFindingsBlockKnownContrastFailures(t *testing.T) {
	t.Parallel()

	if findings := ActivationFindings(DefaultConfig()); len(findings) != 0 {
		t.Fatalf("default activation findings = %+v, want none", findings)
	}

	config := DefaultConfig()
	config.TextColor = "#777777"
	config.BackgroundColor = "#ffffff"
	config.SurfaceColor = "#ffffff"
	findings := ActivationFindings(config)
	if len(findings) == 0 || findings[0].Field != "text_color" ||
		!strings.Contains(findings[0].Message, "4.5:1") {
		t.Fatalf("activation findings = %+v, want actionable text contrast failure", findings)
	}

	config = DefaultConfig()
	config.BackgroundColor = "#ffffff"
	config.AccentColor = "#ffffff"
	config.BorderColor = "#ffffff"
	findings = ActivationFindings(config)
	var accentBlocked, borderBlocked bool
	for _, finding := range findings {
		accentBlocked = accentBlocked || finding.Field == "accent_color"
		borderBlocked = borderBlocked || finding.Field == "border_color"
	}
	if !accentBlocked || !borderBlocked {
		t.Fatalf("activation findings = %+v, want background contrast failures", findings)
	}
}

func TestStylesheetUsesControlledTokensAndAccessibilityOverrides(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	config.BackgroundColor = "#112233"
	config.BrandAsset = BrandAssetWordmark
	config.Background = BackgroundSolid
	config.Typeface = TypefaceClean
	config.Transition = TransitionNone
	config.Effect = EffectNone
	config.Motion = MotionStill

	stylesheet, err := Stylesheet(config)
	if err != nil {
		t.Fatalf("render stylesheet: %v", err)
	}
	for _, want := range []string{
		"--background: #112233",
		`--heading-font: "Open Sans", system-ui, sans-serif`,
		"background-image: none",
		"body::before { display: none; }",
		"body[data-reduced-effects=\"true\"]",
		"@media (prefers-reduced-motion: reduce)",
		"@media (forced-colors: active)",
	} {
		if !strings.Contains(stylesheet, want) {
			t.Errorf("stylesheet missing %q:\n%s", want, stylesheet)
		}
	}
}
