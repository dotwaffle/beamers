package themevalue

import (
	"slices"
	"testing"
)

// TestEveryBundledPresetActivates keeps a Preset from shipping a palette the
// Theme rules would reject. ADR 0057 makes Presets subject to every existing
// Theme rule, so a Producer who selects one and activates it unchanged must not
// hit a contrast failure the bundled set should have caught.
func TestEveryBundledPresetActivates(t *testing.T) {
	t.Parallel()

	presets := Presets()
	if len(presets) == 0 {
		t.Fatal("no Theme Presets are bundled")
	}
	for _, preset := range presets {
		t.Run(preset.Name, func(t *testing.T) {
			t.Parallel()

			if preset.Description == "" {
				t.Errorf("Preset %q has no description", preset.Name)
			}
			if err := ValidateDraft(preset.Config); err != nil {
				t.Fatalf("Preset %q is not a valid Draft: %v", preset.Name, err)
			}
			if findings := ActivationFindings(preset.Config); len(findings) != 0 {
				t.Errorf("Preset %q blocks activation: %+v", preset.Name, findings)
			}
			if _, err := Stylesheet(preset.Config); err != nil {
				t.Errorf("Preset %q does not render: %v", preset.Name, err)
			}
		})
	}
}

// TestTheBaseThemeIsTheBasePreset pins the shipped default to the neutral
// Preset. ADR 0057 chose a venue-agnostic default because privileging one visual
// culture made the other reconfigure before its first Event.
func TestTheBaseThemeIsTheBasePreset(t *testing.T) {
	t.Parallel()

	base, ok := PresetConfig(PresetBase)
	if !ok {
		t.Fatal("the Base Preset is missing from the bundled set")
	}
	if DefaultConfig() != base {
		t.Errorf("DefaultConfig = %+v, want the Base Preset %+v", DefaultConfig(), base)
	}
	// Low-ornament is the point of the neutral default, not an accident of the
	// palette: a conference ships this unchanged.
	if base.Background != BackgroundSolid {
		t.Errorf("Base background = %q, want the undecorated %q", base.Background, BackgroundSolid)
	}
	if base.Effect != EffectNone {
		t.Errorf("Base effect = %q, want %q", base.Effect, EffectNone)
	}
	if base.Typeface != TypefaceClean {
		t.Errorf("Base typeface = %q, want %q", base.Typeface, TypefaceClean)
	}
}

// TestTheDemoscenePresetRestoresTheDecoratedPresentation keeps the demoparty
// path intact. ADR 0057 moves that presentation out of the default rather than
// removing it.
func TestTheDemoscenePresetRestoresTheDecoratedPresentation(t *testing.T) {
	t.Parallel()

	demoscene, ok := PresetConfig(PresetDemoscene)
	if !ok {
		t.Fatal("the Demoscene Preset is missing from the bundled set")
	}
	if demoscene.Background != BackgroundNebula {
		t.Errorf("Demoscene background = %q, want %q", demoscene.Background, BackgroundNebula)
	}
	if demoscene.Effect != EffectStarfield {
		t.Errorf("Demoscene effect = %q, want %q", demoscene.Effect, EffectStarfield)
	}
	if demoscene.Typeface != TypefaceDemoscene {
		t.Errorf("Demoscene typeface = %q, want %q", demoscene.Typeface, TypefaceDemoscene)
	}
	if demoscene.BrandAsset != BrandAssetSignal {
		t.Errorf("Demoscene brand asset = %q, want %q", demoscene.BrandAsset, BrandAssetSignal)
	}
}

// TestPresetConfigRejectsAnUnknownName keeps the bundled set closed.
func TestPresetConfigRejectsAnUnknownName(t *testing.T) {
	t.Parallel()

	if _, ok := PresetConfig("Bespoke"); ok {
		t.Error("PresetConfig accepted a name outside the bundled set")
	}
}

func TestEventVariantsReturnTheClosedCatalog(t *testing.T) {
	t.Parallel()

	want := []string{
		VariantCompetitionOutput,
		VariantEventOverview,
		VariantLocationSignage,
		VariantStandby,
		VariantTimeline,
		VariantCrewOverview,
	}
	got := EventVariants()
	if !slices.Equal(got, want) {
		t.Fatalf("Event Theme variants = %q, want %q", got, want)
	}
	got[0] = "changed"
	if EventVariants()[0] != VariantCompetitionOutput {
		t.Error("caller changed the Event Theme variant catalog")
	}
}

// TestPresetsIsACopy keeps a caller from editing the bundled set in place.
func TestPresetsIsACopy(t *testing.T) {
	t.Parallel()

	first := Presets()
	first[0].Config.BackgroundColor = "#ffffff"
	if Presets()[0].Config.BackgroundColor == "#ffffff" {
		t.Error("Presets returned the bundled set by reference")
	}
}
