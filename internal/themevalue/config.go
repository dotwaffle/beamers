// Package themevalue owns controlled Theme values and rendering.
package themevalue

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// BrandAssetWordmark renders the brand name without a mark.
	BrandAssetWordmark = "wordmark"
	// BrandAssetSignal renders the bundled signal mark.
	BrandAssetSignal = "signal"

	// BackgroundSolid disables the bundled decorative background.
	BackgroundSolid = "solid"
	// BackgroundNebula renders the bundled decorative background.
	BackgroundNebula = "nebula"

	// TypefaceDemoscene uses the bundled display heading font.
	TypefaceDemoscene = "demoscene"
	// TypefaceClean uses the bundled body font throughout.
	TypefaceClean = "clean"

	// TransitionNone disables page transitions.
	TransitionNone = "none"
	// TransitionFade enables the bundled page transition.
	TransitionFade = "fade"

	// EffectNone disables decorative effects.
	EffectNone = "none"
	// EffectStarfield enables the bundled starfield.
	EffectStarfield = "starfield"

	// MotionStill disables Theme motion.
	MotionStill = "still"
	// MotionSubtle uses the default Theme motion.
	MotionSubtle = "subtle"
	// MotionFull uses the strongest Theme motion.
	MotionFull = "full"

	// VariantCompetitionOutput specializes the built-in Competition Output View.
	VariantCompetitionOutput = "competition-output"
	// VariantEventOverview specializes the built-in Event Overview View.
	VariantEventOverview = "event-overview"
	// VariantLocationSignage specializes the built-in Location Signage View.
	VariantLocationSignage = "location-signage"
	// VariantStandby specializes the built-in Standby View.
	VariantStandby = "standby"
)

// Config is one controlled browser presentation.
type Config struct {
	BrandAsset      string `json:"brand_asset"`
	BackgroundColor string `json:"background_color"`
	SurfaceColor    string `json:"surface_color"`
	BorderColor     string `json:"border_color"`
	TextColor       string `json:"text_color"`
	MutedColor      string `json:"muted_color"`
	AccentColor     string `json:"accent_color"`
	LinkColor       string `json:"link_color"`
	FocusColor      string `json:"focus_color"`
	Background      string `json:"background"`
	Typeface        string `json:"typeface"`
	Transition      string `json:"transition"`
	Effect          string `json:"effect"`
	Motion          string `json:"motion"`
}

// EventConfig overrides inherited Installation values and approved Display Views.
type EventConfig struct {
	Overrides Config            `json:"overrides"`
	Variants  map[string]Config `json:"variants,omitempty"`
}

// ValidationError identifies one rejected Theme field.
type ValidationError struct {
	Field   string
	Message string
}

// Finding is one actionable activation failure.
type Finding struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements error.
func (err *ValidationError) Error() string {
	return err.Field + ": " + err.Message
}

// DefaultConfig returns the accessible built-in Installation Theme.
func DefaultConfig() Config {
	return Config{
		BrandAsset:      BrandAssetSignal,
		BackgroundColor: "#080b15",
		SurfaceColor:    "#141a2c",
		BorderColor:     "#7180aa",
		TextColor:       "#f1f5ff",
		MutedColor:      "#c2cbe0",
		AccentColor:     "#62ebcb",
		LinkColor:       "#79d7ff",
		FocusColor:      "#ffdf6e",
		Background:      BackgroundNebula,
		Typeface:        TypefaceDemoscene,
		Transition:      TransitionFade,
		Effect:          EffectStarfield,
		Motion:          MotionSubtle,
	}
}

// ValidateDraft rejects values outside the documented Theme controls.
func ValidateDraft(config Config) error {
	for field, value := range map[string]string{
		"background_color": config.BackgroundColor,
		"surface_color":    config.SurfaceColor,
		"border_color":     config.BorderColor,
		"text_color":       config.TextColor,
		"muted_color":      config.MutedColor,
		"accent_color":     config.AccentColor,
		"link_color":       config.LinkColor,
		"focus_color":      config.FocusColor,
	} {
		if !validColor(value) {
			return invalid(field, "must be a six-digit hexadecimal color")
		}
	}
	if config.BrandAsset != BrandAssetWordmark && config.BrandAsset != BrandAssetSignal {
		return invalid("brand_asset", "must be wordmark or signal")
	}
	if config.Background != BackgroundSolid && config.Background != BackgroundNebula {
		return invalid("background", "must be solid or nebula")
	}
	if config.Typeface != TypefaceDemoscene && config.Typeface != TypefaceClean {
		return invalid("typeface", "must be demoscene or clean")
	}
	if config.Transition != TransitionNone && config.Transition != TransitionFade {
		return invalid("transition", "must be none or fade")
	}
	if config.Effect != EffectNone && config.Effect != EffectStarfield {
		return invalid("effect", "must be none or starfield")
	}
	switch config.Motion {
	case MotionStill, MotionSubtle, MotionFull:
	default:
		return invalid("motion", "must be still, subtle, or full")
	}
	return nil
}

// ValidateEventDraft rejects undocumented Event controls while allowing inheritance.
func ValidateEventDraft(config EventConfig) error {
	if _, err := resolveEvent(DefaultConfig(), config, ""); err != nil {
		return err
	}
	for variant := range config.Variants {
		if !validVariant(variant) {
			return invalid("variants", "must identify an approved Display surface")
		}
		if _, err := resolveEvent(DefaultConfig(), config, variant); err != nil {
			return err
		}
	}
	return nil
}

// ResolveEvent applies inherited Event and approved Display-variant values.
func ResolveEvent(base Config, config EventConfig, variant string) (Config, error) {
	if variant != "" && !validVariant(variant) {
		return Config{}, invalid("variant", "must identify an approved Display surface")
	}
	return resolveEvent(base, config, variant)
}

// EventActivationFindings returns failures across the base and configured variants.
func EventActivationFindings(base Config, config EventConfig) []Finding {
	var findings []Finding
	for _, variant := range []string{
		"",
		VariantCompetitionOutput,
		VariantEventOverview,
		VariantLocationSignage,
		VariantStandby,
	} {
		if variant != "" {
			if _, configured := config.Variants[variant]; !configured {
				continue
			}
		}
		resolved, err := ResolveEvent(base, config, variant)
		if err != nil {
			return []Finding{{Field: "config", Message: err.Error()}}
		}
		findings = append(findings, ActivationFindings(resolved)...)
	}
	return findings
}

func resolveEvent(base Config, config EventConfig, variant string) (Config, error) {
	if err := ValidateDraft(base); err != nil {
		return Config{}, err
	}
	resolved := applyOverrides(base, config.Overrides)
	if variant != "" {
		resolved = applyOverrides(resolved, config.Variants[variant])
	}
	if err := ValidateDraft(resolved); err != nil {
		return Config{}, err
	}
	return resolved, nil
}

func applyOverrides(base, overrides Config) Config {
	fields := []*string{
		&base.BrandAsset,
		&base.BackgroundColor,
		&base.SurfaceColor,
		&base.BorderColor,
		&base.TextColor,
		&base.MutedColor,
		&base.AccentColor,
		&base.LinkColor,
		&base.FocusColor,
		&base.Background,
		&base.Typeface,
		&base.Transition,
		&base.Effect,
		&base.Motion,
	}
	values := []string{
		overrides.BrandAsset,
		overrides.BackgroundColor,
		overrides.SurfaceColor,
		overrides.BorderColor,
		overrides.TextColor,
		overrides.MutedColor,
		overrides.AccentColor,
		overrides.LinkColor,
		overrides.FocusColor,
		overrides.Background,
		overrides.Typeface,
		overrides.Transition,
		overrides.Effect,
		overrides.Motion,
	}
	for index, value := range values {
		if value != "" {
			*fields[index] = value
		}
	}
	return base
}

func validVariant(value string) bool {
	switch value {
	case VariantCompetitionOutput,
		VariantEventOverview,
		VariantLocationSignage,
		VariantStandby:
		return true
	default:
		return false
	}
}

// ActivationFindings returns known contrast and legibility failures.
func ActivationFindings(config Config) []Finding {
	if err := ValidateDraft(config); err != nil {
		var validation *ValidationError
		if !errors.As(err, &validation) {
			return []Finding{{Field: "config", Message: err.Error()}}
		}
		return []Finding{{Field: validation.Field, Message: validation.Message}}
	}
	pairs := []struct {
		field      string
		foreground string
		background string
		minimum    float64
		message    string
	}{
		{"text_color", config.TextColor, config.BackgroundColor, 4.5, "must have at least 4.5:1 contrast against the background"},
		{"text_color", config.TextColor, config.SurfaceColor, 4.5, "must have at least 4.5:1 contrast against surfaces"},
		{"muted_color", config.MutedColor, config.BackgroundColor, 4.5, "must have at least 4.5:1 contrast against the background"},
		{"muted_color", config.MutedColor, config.SurfaceColor, 4.5, "must have at least 4.5:1 contrast against surfaces"},
		{"accent_color", config.AccentColor, config.BackgroundColor, 4.5, "must have at least 4.5:1 contrast against the background"},
		{"accent_color", config.AccentColor, config.SurfaceColor, 4.5, "must have at least 4.5:1 contrast against surfaces"},
		{"accent_color", "#07130f", config.AccentColor, 4.5, "must preserve at least 4.5:1 button text contrast"},
		{"link_color", config.LinkColor, config.BackgroundColor, 4.5, "must have at least 4.5:1 contrast against the background"},
		{"link_color", config.LinkColor, config.SurfaceColor, 4.5, "must have at least 4.5:1 contrast against surfaces"},
		{"focus_color", config.FocusColor, config.BackgroundColor, 3, "must have at least 3:1 contrast against the background"},
		{"focus_color", config.FocusColor, config.SurfaceColor, 3, "must have at least 3:1 contrast against surfaces"},
		{"border_color", config.BorderColor, config.BackgroundColor, 3, "must have at least 3:1 contrast against the background"},
		{"border_color", config.BorderColor, config.SurfaceColor, 3, "must have at least 3:1 contrast against surfaces"},
	}
	var findings []Finding
	for _, pair := range pairs {
		if contrastRatio(parseColor(pair.foreground), parseColor(pair.background)) < pair.minimum {
			findings = append(findings, Finding{Field: pair.field, Message: pair.message})
		}
	}
	return findings
}

// Stylesheet renders a Config through fixed CSS declarations only.
func Stylesheet(config Config) (string, error) {
	if err := ValidateDraft(config); err != nil {
		return "", err
	}
	headingFont := `"Chakra Petch", sans-serif`
	if config.Typeface == TypefaceClean {
		headingFont = `"Open Sans", system-ui, sans-serif`
	}
	backgroundImage := "radial-gradient(circle at 15% 10%, #172755 0, transparent 32rem)"
	if config.Background == BackgroundSolid {
		backgroundImage = "none"
	}
	effectDisplay := "block"
	if config.Effect == EffectNone {
		effectDisplay = "none"
	}
	starfieldDuration, arrivalDuration := "24s", "350ms"
	switch config.Motion {
	case MotionStill:
		starfieldDuration, arrivalDuration = "0s", "0ms"
	case MotionFull:
		starfieldDuration, arrivalDuration = "12s", "500ms"
	}
	transition := "arrive " + arrivalDuration + " ease-out"
	if config.Transition == TransitionNone || config.Motion == MotionStill {
		transition = "none"
	}
	brandMark := ""
	if config.BrandAsset == BrandAssetSignal {
		brandMark = `nav > a:first-child::before { content: "◆"; margin-inline-end: 0.35rem; }` + "\n"
	}

	var css strings.Builder
	_, _ = fmt.Fprintf(&css, `:root {
  --background: %s;
  --surface: %s;
  --surface-raised: %s;
  --border: %s;
  --text: %s;
  --muted: %s;
  --accent: %s;
  --link: %s;
  --focus: %s;
  --heading-font: %s;
  --body-font: "Open Sans", system-ui, sans-serif;
}
body { background-image: %s; font-family: var(--body-font); }
h1, h2, h3, nav > a:first-child { font-family: var(--heading-font); }
body::before { display: %s; }
body[data-reduced-effects="false"]::before { animation: starfield %s linear infinite; }
body[data-reduced-effects="false"] main { animation: %s; }
%sbody[data-reduced-effects="true"]::before { animation: none; opacity: 0.08; }
body[data-reduced-effects="true"] main { animation: none; }
@media (prefers-reduced-motion: reduce) {
  body[data-reduced-effects]::before,
  body[data-reduced-effects] main { animation: none; }
}
@media (forced-colors: active) {
  body { background: Canvas; }
  body::before { display: none; }
  main { background: Canvas; border-color: CanvasText; box-shadow: none; }
}
`,
		config.BackgroundColor,
		config.SurfaceColor,
		config.SurfaceColor,
		config.BorderColor,
		config.TextColor,
		config.MutedColor,
		config.AccentColor,
		config.LinkColor,
		config.FocusColor,
		headingFont,
		backgroundImage,
		effectDisplay,
		starfieldDuration,
		transition,
		brandMark,
	)
	return css.String(), nil
}

func validColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 16, 24)
	return err == nil
}

type color struct {
	red   float64
	green float64
	blue  float64
}

func parseColor(value string) color {
	component := func(start int) float64 {
		parsed, _ := strconv.ParseUint(value[start:start+2], 16, 8)
		return float64(parsed) / 255
	}
	return color{red: component(1), green: component(3), blue: component(5)}
}

func contrastRatio(first, second color) float64 {
	firstLuminance := relativeLuminance(first)
	secondLuminance := relativeLuminance(second)
	lighter := math.Max(firstLuminance, secondLuminance)
	darker := math.Min(firstLuminance, secondLuminance)
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(value color) float64 {
	linear := func(component float64) float64 {
		if component <= 0.04045 {
			return component / 12.92
		}
		return math.Pow((component+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(value.red) +
		0.7152*linear(value.green) +
		0.0722*linear(value.blue)
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
