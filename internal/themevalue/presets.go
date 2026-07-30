package themevalue

import "slices"

// Preset names. The bundled set is closed in version one.
const (
	// PresetBase is the neutral, venue-agnostic Theme a conference can ship
	// unchanged.
	PresetBase = "Base"
	// PresetDemoscene restores the saturated, decorated presentation the earlier
	// built-in default assumed.
	PresetDemoscene = "Demoscene"
	// PresetConference is a softened variant for a corporate or academic Event.
	PresetConference = "Conference"
)

// Preset is a fixed, reviewed Config carrying colors, background, bundled type
// choice, transition, effect, and motion level.
//
// Selecting a Preset populates a Draft Theme Revision with these values. It
// introduces no new rendering path, no new stylesheet, and no new Theme field, so
// a Revision created from a Preset is indistinguishable from one typed by hand,
// and the Producer may edit any populated value before activating. Presets remain
// subject to every existing Theme rule, including the contrast gating applied at
// activation. See ADR 0057.
type Preset struct {
	Name        string
	Description string
	Config      Config
}

// presets is the bundled set in the order it is offered. Base leads because it
// is the shipped default: Beamers serves demoparties and conferences equally, and
// privileging one visual culture in the default made the other reconfigure before
// its first Event.
var presets = []Preset{
	{
		Name:        PresetBase,
		Description: "Neutral, high-contrast, and low-ornament. Ships unchanged.",
		Config: Config{
			BrandAsset:      BrandAssetWordmark,
			BackgroundColor: "#0d1117",
			SurfaceColor:    "#171c23",
			BorderColor:     "#8b949e",
			TextColor:       "#f0f6fc",
			MutedColor:      "#c9d1d9",
			AccentColor:     "#6cb6ff",
			LinkColor:       "#79c0ff",
			FocusColor:      "#ffd33d",
			LiveColor:       "#ff7b72",
			WarningColor:    "#f5b544",
			DangerColor:     "#ff8fa3",
			SuccessColor:    "#56d364",
			Background:      BackgroundSolid,
			Typeface:        TypefaceClean,
			Transition:      TransitionNone,
			Effect:          EffectNone,
			Motion:          MotionSubtle,
		},
	},
	{
		Name:        PresetDemoscene,
		Description: "Saturated accent, display heading face, decorated background, and starfield.",
		Config: Config{
			BrandAsset:      BrandAssetSignal,
			BackgroundColor: "#080b15",
			SurfaceColor:    "#141a2c",
			BorderColor:     "#7180aa",
			TextColor:       "#f1f5ff",
			MutedColor:      "#c2cbe0",
			AccentColor:     "#62ebcb",
			LinkColor:       "#79d7ff",
			FocusColor:      "#ffdf6e",
			LiveColor:       "#ff6b5e",
			WarningColor:    "#f5b544",
			DangerColor:     "#ff86a2",
			SuccessColor:    "#5ee38b",
			Background:      BackgroundNebula,
			Typeface:        TypefaceDemoscene,
			Transition:      TransitionFade,
			Effect:          EffectStarfield,
			Motion:          MotionSubtle,
		},
	},
	{
		Name:        PresetConference,
		Description: "Softened neutral palette with the bundled page transition.",
		Config: Config{
			BrandAsset:      BrandAssetWordmark,
			BackgroundColor: "#11131a",
			SurfaceColor:    "#1b1e27",
			BorderColor:     "#8e94a3",
			TextColor:       "#f4f5f8",
			MutedColor:      "#cbd0da",
			AccentColor:     "#9ab8ff",
			LinkColor:       "#8fc4ff",
			FocusColor:      "#ffd75e",
			LiveColor:       "#ff8a80",
			WarningColor:    "#f0b73f",
			DangerColor:     "#ff93aa",
			SuccessColor:    "#63d47a",
			Background:      BackgroundSolid,
			Typeface:        TypefaceClean,
			Transition:      TransitionFade,
			Effect:          EffectNone,
			Motion:          MotionSubtle,
		},
	},
}

// Presets returns the bundled Theme Presets in the order they are offered.
func Presets() []Preset {
	return slices.Clone(presets)
}

// PresetConfig returns the named Preset's values. The second result reports
// whether the name identifies a bundled Preset.
func PresetConfig(name string) (Config, bool) {
	for _, preset := range presets {
		if preset.Name == name {
			return preset.Config, true
		}
	}
	return Config{}, false
}
