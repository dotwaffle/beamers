package displayviews

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestComposeBuiltInViewsFromNamedRegions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		viewKey    string
		standby    bool
		layoutKey  string
		wantRegion []Region
	}{
		{
			name:      "Standby",
			standby:   true,
			layoutKey: "standby",
			wantRegion: []Region{
				{Name: "branding", Widget: WidgetBranding, Persistent: true},
				{Name: "message", Widget: WidgetStandby, Persistent: true},
			},
		},
		{
			name:      "Event Overview",
			viewKey:   EventOverview,
			layoutKey: "event-overview",
			wantRegion: []Region{
				{Name: "header", Widget: WidgetBranding, Persistent: true},
				{Name: "schedule", Widget: WidgetRotation},
				{Name: "clock", Widget: WidgetClock, Persistent: true},
			},
		},
		{
			name:      "Location Signage",
			viewKey:   LocationSignage,
			layoutKey: "location-signage",
			wantRegion: []Region{
				{Name: "location", Widget: WidgetLocation, Persistent: true},
				{Name: "now-next", Widget: WidgetNowNext, Persistent: true},
				{Name: "event-content", Widget: WidgetRotation},
				{Name: "clock", Widget: WidgetClock, Persistent: true},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			composition, err := Compose(test.viewKey, test.standby, DefaultConfiguration())
			if err != nil {
				t.Fatalf("Compose() error = %v", err)
			}
			if composition.Layout.Key != test.layoutKey {
				t.Errorf("Layout key = %q, want %q", composition.Layout.Key, test.layoutKey)
			}
			if composition.Layout.RotationSeconds != 15 {
				t.Errorf("rotation = %d, want 15", composition.Layout.RotationSeconds)
			}
			if len(composition.Layout.Regions) != len(test.wantRegion) {
				t.Fatalf("regions = %+v, want %+v", composition.Layout.Regions, test.wantRegion)
			}
			for index, want := range test.wantRegion {
				if got := composition.Layout.Regions[index]; got != want {
					t.Errorf("region %d = %+v, want %+v", index, got, want)
				}
			}
		})
	}
}

func TestValidateConfigurationBlocksInaccessibleThemes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		amend func(*Configuration)
		field string
	}{
		{
			name: "foreground against background",
			amend: func(configuration *Configuration) {
				configuration.Theme.ForegroundColor = "#777777"
				configuration.Theme.BackgroundColor = "#ffffff"
			},
			field: "theme.foreground_color",
		},
		{
			name: "foreground against accent panel",
			amend: func(configuration *Configuration) {
				configuration.Theme.ForegroundColor = "#ffffff"
				configuration.Theme.AccentColor = "#aaaaaa"
			},
			field: "theme.accent_color",
		},
		{
			name: "variable media without sufficient scrim",
			amend: func(configuration *Configuration) {
				configuration.Theme.Background = BackgroundVariableMedia
				configuration.Theme.ScrimColor = "#000000"
				configuration.Theme.ScrimOpacity = 50
			},
			field: "theme.scrim_opacity",
		},
		{
			name: "variable media range crosses foreground luminance",
			amend: func(configuration *Configuration) {
				configuration.Theme.ForegroundColor = "#767676"
				configuration.Theme.BackgroundColor = "#000000"
				configuration.Theme.AccentColor = "#000000"
				configuration.Theme.Background = BackgroundVariableMedia
				configuration.Theme.ScrimColor = "#000000"
				configuration.Theme.ScrimOpacity = 0
			},
			field: "theme.scrim_opacity",
		},
		{
			name: "solid background negative scrim opacity",
			amend: func(configuration *Configuration) {
				configuration.Theme.ScrimOpacity = -1
			},
			field: "theme.scrim_opacity",
		},
		{
			name: "solid background excessive scrim opacity",
			amend: func(configuration *Configuration) {
				configuration.Theme.ScrimOpacity = 101
			},
			field: "theme.scrim_opacity",
		},
		{
			name: "arbitrary font",
			amend: func(configuration *Configuration) {
				configuration.Theme.Font = "url(https://example.invalid/font.woff2)"
			},
			field: "theme.font",
		},
		{
			name: "arbitrary transition",
			amend: func(configuration *Configuration) {
				configuration.Theme.Transition = "spin"
			},
			field: "theme.transition",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			configuration := DefaultConfiguration()
			test.amend(&configuration)
			err := ValidateConfiguration(configuration)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateConfiguration() error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Errorf("validation field = %q, want %q", validation.Field, test.field)
			}
		})
	}
}

func TestValidateConfigurationAcceptsControlledTheme(t *testing.T) {
	t.Parallel()

	configuration := DefaultConfiguration()
	configuration.RotationSeconds = 30
	configuration.Theme = Theme{
		Branding:        "FOSDEM",
		ForegroundColor: "#ffffff",
		BackgroundColor: "#101828",
		AccentColor:     "#1d4ed8",
		Background:      BackgroundVariableMedia,
		ScrimColor:      "#000000",
		ScrimOpacity:    85,
		Font:            FontSans,
		Transition:      TransitionFade,
	}
	if err := ValidateConfiguration(configuration); err != nil {
		t.Fatalf("ValidateConfiguration() error = %v", err)
	}
}

func TestConfigurationCarriesTimerThresholdInheritance(t *testing.T) {
	t.Parallel()

	configuration := DefaultConfiguration()
	configuration.TimerThresholds = []TimerThreshold{
		{RemainingSeconds: 300, Emphasis: EmphasisAttention},
	}
	configuration.SessionTypeTimerThresholds = map[string][]TimerThreshold{
		"Presentation": {{RemainingSeconds: 120, Emphasis: EmphasisAttention}},
	}
	configuration.SessionTimerThresholds = map[int][]TimerThreshold{
		42: {{RemainingSeconds: 30, Emphasis: EmphasisUrgent}},
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode configuration: %v", err)
	}
	got, err := ParseConfiguration(string(encoded))
	if err != nil {
		t.Fatalf("ParseConfiguration() error = %v", err)
	}
	if got.SessionTypeTimerThresholds["Presentation"][0].RemainingSeconds != 120 {
		t.Errorf("Session-type thresholds = %+v", got.SessionTypeTimerThresholds)
	}
	if got.SessionTimerThresholds[42][0].Emphasis != EmphasisUrgent {
		t.Errorf("Session thresholds = %+v", got.SessionTimerThresholds)
	}
}

func TestConfigurationPreservesExplicitlyEmptyEventThresholds(t *testing.T) {
	t.Parallel()

	configuration := DefaultConfiguration()
	configuration.TimerThresholds = []TimerThreshold{}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode configuration: %v", err)
	}
	got, err := ParseConfiguration(string(encoded))
	if err != nil {
		t.Fatalf("ParseConfiguration() error = %v", err)
	}
	if got.TimerThresholds == nil || len(got.TimerThresholds) != 0 {
		t.Errorf("Timer thresholds = %+v, want explicit empty override", got.TimerThresholds)
	}
}

func TestNormalizeConfigurationDefaultsOmittedEventThresholds(t *testing.T) {
	t.Parallel()

	configuration := DefaultConfiguration()
	configuration.TimerThresholds = nil
	got := NormalizeConfiguration(configuration)
	if len(got.TimerThresholds) != 2 ||
		got.TimerThresholds[0].RemainingSeconds != 5*60 ||
		got.TimerThresholds[1].RemainingSeconds != 60 {
		t.Errorf("normalized Timer thresholds = %+v, want safe defaults", got.TimerThresholds)
	}

	configuration.TimerThresholds = []TimerThreshold{}
	got = NormalizeConfiguration(configuration)
	if got.TimerThresholds == nil || len(got.TimerThresholds) != 0 {
		t.Errorf("explicitly empty Timer thresholds = %+v, want preserved empty override", got.TimerThresholds)
	}
}

func TestValidateConfigurationRejectsInvalidTimerThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		amend func(*Configuration)
		field string
	}{
		{
			name: "non-positive duration",
			amend: func(configuration *Configuration) {
				configuration.TimerThresholds = []TimerThreshold{
					{Emphasis: EmphasisAttention},
				}
			},
			field: "timer_thresholds",
		},
		{
			name: "unknown emphasis",
			amend: func(configuration *Configuration) {
				configuration.TimerThresholds = []TimerThreshold{
					{RemainingSeconds: 60, Emphasis: "flashing"},
				}
			},
			field: "timer_thresholds",
		},
		{
			name: "unknown Session type",
			amend: func(configuration *Configuration) {
				configuration.SessionTypeTimerThresholds = map[string][]TimerThreshold{
					"Keynote": {{RemainingSeconds: 60, Emphasis: EmphasisAttention}},
				}
			},
			field: "session_type_timer_thresholds",
		},
		{
			name: "invalid Session ID",
			amend: func(configuration *Configuration) {
				configuration.SessionTimerThresholds = map[int][]TimerThreshold{
					0: {{RemainingSeconds: 60, Emphasis: EmphasisAttention}},
				}
			},
			field: "session_timer_thresholds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			configuration := DefaultConfiguration()
			test.amend(&configuration)
			err := ValidateConfiguration(configuration)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateConfiguration() error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Errorf("validation field = %q, want %q", validation.Field, test.field)
			}
		})
	}
}
