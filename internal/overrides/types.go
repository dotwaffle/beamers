package overrides

import "github.com/dotwaffle/beamers/internal/store"

// Emphasis is accessible Stage Message emphasis without priority changes.
type Emphasis = store.StageMessageEmphasis

const (
	// Normal is routine information.
	Normal = store.StageMessageNormal
	// Attention requests elevated attention.
	Attention = store.StageMessageAttention
	// Urgent requests immediate attention without emergency semantics.
	Urgent = store.StageMessageUrgent
)

// Preset contains Event-configured Stage Message defaults.
type Preset = store.StageMessagePreset

// StageMessageConfiguration is one Event's Stage Message configuration.
type StageMessageConfiguration = store.StageMessageConfiguration

// Override is one durable activation.
type Override = store.DisplayOverride

// Preview is one currently resolved Override target set.
type Preview = store.DisplayOverridePreview

// ActiveOverride is one active Override with live Display membership.
type ActiveOverride = store.ActiveDisplayOverride

// Target identifies one logical or fixed Override scope.
type Target = store.DisplayOverrideTarget

// TargetType is one logical or fixed Override scope discriminator.
type TargetType = store.DisplayOverrideTargetType

// PriorityInput activates an Urgent Notice or Emergency Alert.
type PriorityInput struct {
	EventID            int    `json:"event_id"`
	Target             Target `json:"target"`
	Text               string `json:"text"`
	Presentation       string `json:"presentation"`
	DurationSeconds    int    `json:"duration_seconds"`
	UntilCleared       bool   `json:"until_cleared"`
	Confirmed          bool   `json:"confirmed"`
	ConfirmationMethod string `json:"confirmation_method"`
	PreviewFingerprint string `json:"preview_fingerprint"`
	CommandID          string `json:"command_id"`
}

// PriorityPreview binds normalized content to the currently resolved Displays.
type PriorityPreview struct {
	Preview
	ConfirmationFingerprint string `json:"confirmation_fingerprint,omitempty"`
	Nondurable              bool   `json:"nondurable,omitempty"`
}

// ConfigureInput replaces Event Stage Message defaults.
type ConfigureInput struct {
	EventID                int      `json:"event_id"`
	DefaultDurationSeconds int      `json:"default_duration_seconds"`
	Presets                []Preset `json:"presets"`
	ExpectedRevision       int      `json:"expected_revision"`
	CommandID              string   `json:"command_id"`
}

// SendStageMessageInput selects a preset or free-form message.
type SendStageMessageInput struct {
	EventID         int      `json:"event_id"`
	PresetKey       string   `json:"preset_key"`
	Text            string   `json:"text"`
	TargetGroupKey  string   `json:"target_group_key"`
	DurationSeconds int      `json:"duration_seconds"`
	Emphasis        Emphasis `json:"emphasis"`
	UntilCleared    bool     `json:"until_cleared"`
	CommandID       string   `json:"command_id"`
}

// TechnicalDifficultiesInput activates a fullscreen wait message.
type TechnicalDifficultiesInput struct {
	EventID         int    `json:"event_id"`
	TargetGroupKey  string `json:"target_group_key"`
	Text            string `json:"text"`
	DurationSeconds int    `json:"duration_seconds"`
	UntilCleared    bool   `json:"until_cleared"`
	CommandID       string `json:"command_id"`
}

// ClearInput clears one exact Override activation.
type ClearInput struct {
	EventID            int    `json:"event_id"`
	OverrideID         int    `json:"override_id"`
	ExpectedRevision   int    `json:"expected_revision"`
	CommandID          string `json:"command_id"`
	Confirmed          bool   `json:"confirmed"`
	ConfirmationMethod string `json:"confirmation_method"`
}
