package displayviews

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
)

const (
	// WidgetBranding identifies persistent Event branding.
	WidgetBranding = "branding"
	// WidgetStandby identifies the Standby message.
	WidgetStandby = "standby"
	// WidgetLocation identifies the assigned Location name.
	WidgetLocation = "location"
	// WidgetNowNext identifies persistent current and next Session content.
	WidgetNowNext = "now-next"
	// WidgetRotation identifies autonomous rotating Event content.
	WidgetRotation = "rotation"
	// WidgetClock identifies a server-synchronized digital clock.
	WidgetClock = "clock"
	// WidgetStageTimer identifies live Stage Timer content.
	WidgetStageTimer = "stage-timer"
	// WidgetProgramOutput identifies Competition Program Output.
	WidgetProgramOutput = "program-output"

	// BackgroundSolid uses only the configured background color.
	BackgroundSolid = "solid"
	// BackgroundVariableMedia places content over varying imagery protected by a scrim.
	BackgroundVariableMedia = "variable-media"

	// FontSans selects the built-in sans-serif font stack.
	FontSans = "sans"
	// FontSerif selects the built-in serif font stack.
	FontSerif = "serif"
	// FontMono selects the built-in monospace font stack.
	FontMono = "mono"

	// TransitionNone changes rotating pages without animation.
	TransitionNone = "none"
	// TransitionFade uses a restrained opacity transition.
	TransitionFade = "fade"
)

const minimumTextContrast = 4.5
const maximumTimerThresholdSeconds = 24 * 60 * 60

const (
	// EmphasisAttention identifies an approaching timer target.
	EmphasisAttention = "attention"
	// EmphasisUrgent identifies an imminent or exceeded timer target.
	EmphasisUrgent = "urgent"
)

// ValidationError identifies one invalid Display configuration field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (err *ValidationError) Error() string {
	return err.Field + ": " + err.Message
}

// Theme is the controlled Event appearance accepted by Display renderers.
type Theme struct {
	Branding        string `json:"branding"`
	ForegroundColor string `json:"foreground_color"`
	BackgroundColor string `json:"background_color"`
	AccentColor     string `json:"accent_color"`
	Background      string `json:"background"`
	ScrimColor      string `json:"scrim_color"`
	ScrimOpacity    int    `json:"scrim_opacity"`
	Font            string `json:"font"`
	Transition      string `json:"transition"`
}

// TimerThreshold changes Stage Timer emphasis at one remaining duration.
type TimerThreshold struct {
	RemainingSeconds int    `json:"remaining_seconds"`
	Emphasis         string `json:"emphasis"`
}

// Configuration controls the shared appearance and autonomous rotation interval.
type Configuration struct {
	RotationSeconds            int                         `json:"rotation_seconds"`
	Theme                      Theme                       `json:"theme"`
	TimerThresholds            []TimerThreshold            `json:"timer_thresholds"`
	SessionTypeTimerThresholds map[string][]TimerThreshold `json:"session_type_timer_thresholds,omitempty"`
	SessionTimerThresholds     map[int][]TimerThreshold    `json:"session_timer_thresholds,omitempty"`
}

// Region binds one named Layout region to a built-in Widget.
type Region struct {
	Name       string `json:"name"`
	Widget     string `json:"widget"`
	Persistent bool   `json:"persistent"`
}

// Layout is one responsive built-in arrangement.
type Layout struct {
	Key             string   `json:"key"`
	RotationSeconds int      `json:"rotation_seconds"`
	Regions         []Region `json:"regions"`
}

// Composition is the complete renderer-neutral View description.
type Composition struct {
	Layout Layout `json:"layout"`
	Theme  Theme  `json:"theme"`
}

// DefaultConfiguration returns the accessible built-in Event presentation.
func DefaultConfiguration() Configuration {
	return Configuration{
		RotationSeconds: 15,
		TimerThresholds: []TimerThreshold{
			{RemainingSeconds: 5 * 60, Emphasis: EmphasisAttention},
			{RemainingSeconds: 60, Emphasis: EmphasisUrgent},
		},
		Theme: Theme{
			ForegroundColor: "#ffffff",
			BackgroundColor: "#101828",
			AccentColor:     "#1d4ed8",
			Background:      BackgroundSolid,
			ScrimColor:      "#000000",
			ScrimOpacity:    85,
			Font:            FontSans,
			Transition:      TransitionFade,
		},
	}
}

// NormalizeConfiguration applies safe defaults to omitted optional fields.
func NormalizeConfiguration(configuration Configuration) Configuration {
	if configuration.TimerThresholds == nil {
		configuration.TimerThresholds = slices.Clone(DefaultConfiguration().TimerThresholds)
	}
	return configuration
}

// ParseConfiguration decodes and validates one durable Event configuration.
func ParseConfiguration(encoded string) (Configuration, error) {
	if encoded == "" {
		return DefaultConfiguration(), nil
	}
	configuration := DefaultConfiguration()
	if err := json.Unmarshal([]byte(encoded), &configuration); err != nil {
		return Configuration{}, errors.New("decode Display configuration")
	}
	configuration = NormalizeConfiguration(configuration)
	if err := ValidateConfiguration(configuration); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

// Compose returns a validated built-in View composition.
func Compose(viewKey string, standby bool, configuration Configuration) (Composition, error) {
	if err := ValidateConfiguration(configuration); err != nil {
		return Composition{}, err
	}
	layout, ok := builtInLayout(viewKey, standby)
	if !ok {
		return Composition{}, &ValidationError{
			Field: "view_key", Message: "must identify a built-in View",
		}
	}
	layout.RotationSeconds = configuration.RotationSeconds
	return Composition{Layout: layout, Theme: configuration.Theme}, nil
}

// ValidateConfiguration rejects inaccessible or renderer-defined Event presentation.
func ValidateConfiguration(configuration Configuration) error {
	if configuration.RotationSeconds < 5 || configuration.RotationSeconds > 300 {
		return invalid("rotation_seconds", "must be between 5 and 300")
	}
	if len(configuration.Theme.Branding) > 200 {
		return invalid("theme.branding", "must not exceed 200 characters")
	}
	foreground, err := parseColor("theme.foreground_color", configuration.Theme.ForegroundColor)
	if err != nil {
		return err
	}
	background, err := parseColor("theme.background_color", configuration.Theme.BackgroundColor)
	if err != nil {
		return err
	}
	accent, err := parseColor("theme.accent_color", configuration.Theme.AccentColor)
	if err != nil {
		return err
	}
	if contrastRatio(foreground, background) < minimumTextContrast {
		return invalid("theme.foreground_color", "must have at least 4.5:1 contrast against the background")
	}
	if contrastRatio(foreground, accent) < minimumTextContrast {
		return invalid("theme.accent_color", "must have at least 4.5:1 contrast with foreground text")
	}
	if configuration.Theme.ScrimOpacity < 0 || configuration.Theme.ScrimOpacity > 100 {
		return invalid("theme.scrim_opacity", "must be between 0 and 100")
	}
	switch configuration.Theme.Background {
	case BackgroundSolid:
	case BackgroundVariableMedia:
		if err := validateVariableBackground(configuration.Theme, foreground); err != nil {
			return err
		}
	default:
		return invalid("theme.background", "must be solid or variable-media")
	}
	switch configuration.Theme.Font {
	case FontSans, FontSerif, FontMono:
	default:
		return invalid("theme.font", "must select a built-in font")
	}
	switch configuration.Theme.Transition {
	case TransitionNone, TransitionFade:
	default:
		return invalid("theme.transition", "must be none or fade")
	}
	if err := validateTimerThresholds("timer_thresholds", configuration.TimerThresholds); err != nil {
		return err
	}
	for sessionType, thresholds := range configuration.SessionTypeTimerThresholds {
		if !validSessionType(sessionType) {
			return invalid("session_type_timer_thresholds", "keys must identify a supported Session type")
		}
		if err := validateTimerThresholds("session_type_timer_thresholds", thresholds); err != nil {
			return err
		}
	}
	for sessionID, thresholds := range configuration.SessionTimerThresholds {
		if sessionID <= 0 {
			return invalid("session_timer_thresholds", "keys must be positive Session IDs")
		}
		if err := validateTimerThresholds("session_timer_thresholds", thresholds); err != nil {
			return err
		}
	}
	return nil
}

func validateTimerThresholds(field string, thresholds []TimerThreshold) error {
	if len(thresholds) > 16 {
		return invalid(field, "must contain at most 16 thresholds")
	}
	remaining := make(map[int]struct{}, len(thresholds))
	for _, threshold := range thresholds {
		if threshold.RemainingSeconds <= 0 ||
			threshold.RemainingSeconds > maximumTimerThresholdSeconds {
			return invalid(field, "remaining seconds must be between 1 and 86400")
		}
		if threshold.Emphasis != EmphasisAttention && threshold.Emphasis != EmphasisUrgent {
			return invalid(field, "emphasis must be attention or urgent")
		}
		if _, duplicate := remaining[threshold.RemainingSeconds]; duplicate {
			return invalid(field, "must not repeat remaining seconds")
		}
		remaining[threshold.RemainingSeconds] = struct{}{}
	}
	return nil
}

func validSessionType(value string) bool {
	switch value {
	case "Presentation", "Competition", "Break", "Activity", "Ceremony", "Performance", "Hold":
		return true
	default:
		return false
	}
}

func builtInLayout(viewKey string, standby bool) (Layout, bool) {
	if standby {
		return Layout{
			Key: "standby",
			Regions: []Region{
				{Name: "branding", Widget: WidgetBranding, Persistent: true},
				{Name: "message", Widget: WidgetStandby, Persistent: true},
			},
		}, true
	}
	switch viewKey {
	case EventOverview:
		return Layout{
			Key: EventOverview,
			Regions: []Region{
				{Name: "header", Widget: WidgetBranding, Persistent: true},
				{Name: "schedule", Widget: WidgetRotation},
				{Name: "clock", Widget: WidgetClock, Persistent: true},
			},
		}, true
	case LocationSignage:
		return Layout{
			Key: LocationSignage,
			Regions: []Region{
				{Name: "location", Widget: WidgetLocation, Persistent: true},
				{Name: "now-next", Widget: WidgetNowNext, Persistent: true},
				{Name: "event-content", Widget: WidgetRotation},
				{Name: "clock", Widget: WidgetClock, Persistent: true},
			},
		}, true
	case StageTimer:
		return Layout{
			Key: StageTimer,
			Regions: []Region{
				{Name: "header", Widget: WidgetBranding, Persistent: true},
				{Name: "timer", Widget: WidgetStageTimer, Persistent: true},
			},
		}, true
	case CompetitionOutput:
		return Layout{
			Key: CompetitionOutput,
			Regions: []Region{
				{Name: "header", Widget: WidgetBranding, Persistent: true},
				{Name: "program", Widget: WidgetProgramOutput, Persistent: true},
			},
		}, true
	default:
		return Layout{}, false
	}
}

func validateVariableBackground(theme Theme, foreground color) error {
	scrim, err := parseColor("theme.scrim_color", theme.ScrimColor)
	if err != nil {
		return err
	}
	opacity := float64(theme.ScrimOpacity) / 100
	darkest := composite(scrim, color{}, opacity)
	lightest := composite(scrim, color{red: 1, green: 1, blue: 1}, opacity)
	foregroundLuminance := relativeLuminance(foreground)
	darkestLuminance := relativeLuminance(darkest)
	lightestLuminance := relativeLuminance(lightest)
	if foregroundLuminance >= darkestLuminance &&
		foregroundLuminance <= lightestLuminance ||
		contrastRatio(foreground, darkest) < minimumTextContrast ||
		contrastRatio(foreground, lightest) < minimumTextContrast {
		return invalid(
			"theme.scrim_opacity",
			"must preserve at least 4.5:1 foreground contrast over variable media",
		)
	}
	return nil
}

type color struct {
	red   float64
	green float64
	blue  float64
}

func parseColor(field, value string) (color, error) {
	if len(value) != 7 || value[0] != '#' {
		return color{}, invalid(field, "must be a six-digit hexadecimal color")
	}
	component := func(start int) (float64, error) {
		parsed, err := strconv.ParseUint(value[start:start+2], 16, 8)
		return float64(parsed) / 255, err
	}
	red, redErr := component(1)
	green, greenErr := component(3)
	blue, blueErr := component(5)
	if redErr != nil || greenErr != nil || blueErr != nil {
		return color{}, invalid(field, "must be a six-digit hexadecimal color")
	}
	return color{red: red, green: green, blue: blue}, nil
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

func composite(foreground, background color, opacity float64) color {
	return color{
		red:   foreground.red*opacity + background.red*(1-opacity),
		green: foreground.green*opacity + background.green*(1-opacity),
		blue:  foreground.blue*opacity + background.blue*(1-opacity),
	}
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
