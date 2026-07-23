package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displayoverride"
	"github.com/dotwaffle/beamers/ent/displayoverridestate"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrDisplayOverrideScope means the actor cannot operate a target Display Group.
	ErrDisplayOverrideScope = errors.New("display override scope denied")
	// ErrDisplayOverrideInput means an Override command is malformed.
	ErrDisplayOverrideInput = errors.New("invalid display override")
	// ErrDisplayOverrideNotFound hides unknown and cross-Event Overrides.
	ErrDisplayOverrideNotFound = errors.New("display override not found")
	// ErrDisplayOverrideRevision means an Override changed after observation.
	ErrDisplayOverrideRevision = errors.New("display override revision conflict")
	// ErrStageMessageConfigurationRevision means preset configuration changed after observation.
	ErrStageMessageConfigurationRevision = errors.New("stage message configuration revision conflict")
)

// DisplayOverrideKind is the closed initial Override vocabulary.
type DisplayOverrideKind string

const (
	// DisplayOverrideStageMessage is a crew-only Overlay.
	DisplayOverrideStageMessage DisplayOverrideKind = "StageMessage"
	// DisplayOverrideTechnicalDifficulties is a fullscreen Replace Override.
	DisplayOverrideTechnicalDifficulties DisplayOverrideKind = "TechnicalDifficulties"
)

// StageMessageEmphasis changes accessible styling without changing priority.
type StageMessageEmphasis string

const (
	// StageMessageNormal is routine information.
	StageMessageNormal StageMessageEmphasis = "Normal"
	// StageMessageAttention requests elevated attention.
	StageMessageAttention StageMessageEmphasis = "Attention"
	// StageMessageUrgent requests immediate attention without emergency semantics.
	StageMessageUrgent StageMessageEmphasis = "Urgent"
)

// StageMessagePreset contains Event-configured activation defaults.
type StageMessagePreset struct {
	Key             string               `json:"key"`
	Text            string               `json:"text"`
	TargetGroupKey  string               `json:"target_group_key"`
	DurationSeconds int                  `json:"duration_seconds,omitempty"`
	Emphasis        StageMessageEmphasis `json:"emphasis"`
}

// StageMessageConfiguration is one Event's preset and duration configuration.
type StageMessageConfiguration struct {
	EventID                int                  `json:"event_id"`
	DefaultDurationSeconds int                  `json:"default_duration_seconds"`
	Presets                []StageMessagePreset `json:"presets"`
	Revision               int                  `json:"revision"`
}

// DisplayOverride is one durable Override activation.
type DisplayOverride struct {
	ID                 int                  `json:"id"`
	EventID            int                  `json:"event_id"`
	TargetGroupKey     string               `json:"target_group_key"`
	Kind               DisplayOverrideKind  `json:"kind"`
	Text               string               `json:"text"`
	Emphasis           StageMessageEmphasis `json:"emphasis"`
	PresetKey          string               `json:"preset_key,omitempty"`
	UntilCleared       bool                 `json:"until_cleared"`
	ExpiresAt          time.Time            `json:"expires_at,omitzero"`
	ClearedAt          time.Time            `json:"cleared_at,omitzero"`
	Revision           int                  `json:"revision"`
	CreatedByAccountID int                  `json:"created_by_account_id"`
	CreatedAt          time.Time            `json:"created_at"`
}

// DisplayOverrideTarget is one currently resolved Display Group member.
type DisplayOverrideTarget struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	ViewKey string `json:"view_key"`
}

// DisplayOverridePreview resolves an Override without activating it.
type DisplayOverridePreview struct {
	Kind           DisplayOverrideKind     `json:"kind"`
	TargetGroupKey string                  `json:"target_group_key"`
	Text           string                  `json:"text"`
	Emphasis       StageMessageEmphasis    `json:"emphasis"`
	UntilCleared   bool                    `json:"until_cleared"`
	ExpiresAt      time.Time               `json:"expires_at,omitzero"`
	Displays       []DisplayOverrideTarget `json:"displays"`
}

// ActiveDisplayOverride is one active Override with its currently resolved targets.
type ActiveDisplayOverride struct {
	DisplayOverride
	Displays []DisplayOverrideTarget `json:"displays"`
}

// ConfigureStageMessagesParams replaces Event preset defaults.
type ConfigureStageMessagesParams struct {
	EventID, ExpectedRevision, DefaultDurationSeconds int
	Presets                                           []StageMessagePreset
}

// ActivateStageMessageParams selects a preset or free-form message.
type ActivateStageMessageParams struct {
	EventID, DurationSeconds int
	PresetKey, Text          string
	TargetGroupKey           string
	Emphasis                 StageMessageEmphasis
	UntilCleared             bool
	Now                      time.Time
}

// ActivateTechnicalDifficultiesParams creates a Replace Override.
type ActivateTechnicalDifficultiesParams struct {
	EventID        int
	TargetGroupKey string
	Text           string
	UntilCleared   bool
	Duration       time.Duration
	Now            time.Time
}

// PreviewStageMessage resolves current Stage Message content and targets.
func (installationStore *SQLite) PreviewStageMessage(
	ctx context.Context,
	params ActivateStageMessageParams,
) (DisplayOverridePreview, error) {
	found, err := installationStore.activeOverrideEvent(ctx, params.EventID)
	if err != nil {
		return DisplayOverridePreview{}, err
	}
	resolved, err := resolveStageMessage(found, params)
	if err != nil {
		return DisplayOverridePreview{}, err
	}
	if !canOperateDisplayGroup(ctx, params.EventID, resolved.TargetGroupKey) {
		return DisplayOverridePreview{}, ErrDisplayOverrideScope
	}
	return installationStore.previewDisplayOverride(
		ctx, resolved.EventID, resolved.TargetGroupKey, DisplayOverrideStageMessage,
		resolved.Text, resolved.Emphasis, resolved.UntilCleared,
		time.Duration(resolved.DurationSeconds)*time.Second, resolved.Now, true,
	)
}

// ListActiveDisplayOverrides returns active Overrides and live target membership.
func (installationStore *SQLite) ListActiveDisplayOverrides(
	ctx context.Context,
	eventID int,
	now time.Time,
) ([]ActiveDisplayOverride, error) {
	if _, err := installationStore.activeOverrideEvent(ctx, eventID); err != nil {
		return nil, err
	}
	found, err := installationStore.client.DisplayOverride.Query().
		Where(
			displayoverride.EventIDEQ(eventID),
			displayoverride.ClearedAtIsNil(),
			displayoverride.Or(
				displayoverride.UntilClearedEQ(true),
				displayoverride.ExpiresAtGT(now),
			),
		).
		Order(ent.Desc(displayoverride.FieldCreatedAt), ent.Desc(displayoverride.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("list active Display Overrides", err)
	}
	result := make([]ActiveDisplayOverride, 0, len(found))
	for _, item := range found {
		if !canOperateDisplayGroup(ctx, eventID, item.TargetGroupKey) {
			continue
		}
		kind := DisplayOverrideKind(item.Kind.String())
		preview, previewErr := installationStore.previewDisplayOverride(
			ctx, eventID, item.TargetGroupKey, kind, item.Text,
			StageMessageEmphasis(item.Emphasis.String()), item.UntilCleared,
			0, now, kind == DisplayOverrideStageMessage,
		)
		if previewErr != nil {
			return nil, previewErr
		}
		result = append(result, ActiveDisplayOverride{
			DisplayOverride: displayOverride(item),
			Displays:        preview.Displays,
		})
	}
	return result, nil
}

// PreviewTechnicalDifficulties resolves current Replace Override targets.
func (installationStore *SQLite) PreviewTechnicalDifficulties(
	ctx context.Context,
	params ActivateTechnicalDifficultiesParams,
) (DisplayOverridePreview, error) {
	if _, err := installationStore.activeOverrideEvent(ctx, params.EventID); err != nil {
		return DisplayOverridePreview{}, err
	}
	params, err := normalizeTechnicalDifficulties(params)
	if err != nil {
		return DisplayOverridePreview{}, err
	}
	if !canOperateDisplayGroup(ctx, params.EventID, params.TargetGroupKey) {
		return DisplayOverridePreview{}, ErrDisplayOverrideScope
	}
	return installationStore.previewDisplayOverride(
		ctx, params.EventID, params.TargetGroupKey, DisplayOverrideTechnicalDifficulties,
		params.Text, StageMessageNormal, params.UntilCleared, params.Duration, params.Now, false,
	)
}

func (installationStore *SQLite) activeOverrideEvent(
	ctx context.Context,
	eventID int,
) (*ent.Event, error) {
	internalContext := systemContext(ctx)
	active, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDEQ(eventID)).
		Exist(internalContext)
	if err != nil {
		return nil, opaqueError("load active Display Override Event", err)
	}
	if !active {
		return nil, ErrEventNotActive
	}
	found, err := installationStore.client.Event.Get(internalContext, eventID)
	if ent.IsNotFound(err) {
		return nil, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return nil, opaqueError("load Display Override Event", err)
	}
	return found, nil
}

func (installationStore *SQLite) previewDisplayOverride(
	ctx context.Context,
	eventID int,
	targetGroupKey string,
	kind DisplayOverrideKind,
	text string,
	emphasis StageMessageEmphasis,
	untilCleared bool,
	duration time.Duration,
	now time.Time,
	crewOnly bool,
) (DisplayOverridePreview, error) {
	assignments, err := installationStore.client.DisplayAssignment.Query().
		Where(displayassignment.EventIDEQ(eventID)).
		WithDisplay().
		All(systemContext(ctx))
	if err != nil {
		return DisplayOverridePreview{}, opaqueError("resolve Display Override preview", err)
	}
	result := DisplayOverridePreview{
		Kind: kind, TargetGroupKey: targetGroupKey, Text: text, Emphasis: emphasis,
		UntilCleared: untilCleared, Displays: make([]DisplayOverrideTarget, 0, len(assignments)),
	}
	if !untilCleared {
		result.ExpiresAt = now.Add(duration)
	}
	for _, assignment := range assignments {
		if !assignmentInDisplayGroup(assignment, targetGroupKey) ||
			(crewOnly && assignment.ViewKey != "stage-timer") {
			continue
		}
		foundDisplay, edgeErr := assignment.Edges.DisplayOrErr()
		if edgeErr != nil {
			return DisplayOverridePreview{}, opaqueError("load Display Override preview target", edgeErr)
		}
		result.Displays = append(result.Displays, DisplayOverrideTarget{
			ID: foundDisplay.ID, Name: foundDisplay.Name, ViewKey: assignment.ViewKey,
		})
	}
	sort.Slice(result.Displays, func(first, second int) bool {
		return result.Displays[first].ID < result.Displays[second].ID
	})
	return result, nil
}

// ConfigureStageMessages replaces Event presets and the default expiry duration.
func (transaction *CommandTx) ConfigureStageMessages(
	ctx context.Context,
	params ConfigureStageMessagesParams,
) (StageMessageConfiguration, error) {
	if params.DefaultDurationSeconds <= 0 || params.DefaultDurationSeconds > 24*60*60 {
		return StageMessageConfiguration{}, ErrDisplayOverrideInput
	}
	presets := slices.Clone(params.Presets)
	sort.Slice(presets, func(first, second int) bool {
		return presets[first].Key < presets[second].Key
	})
	for index, preset := range presets {
		presets[index] = normalizeStageMessagePreset(preset)
		if !validStageMessagePreset(presets[index]) ||
			(index > 0 && presets[index-1].Key == presets[index].Key) {
			return StageMessageConfiguration{}, ErrDisplayOverrideInput
		}
	}
	found, err := transaction.transaction.Event.Query().
		Where(event.IDEQ(params.EventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return StageMessageConfiguration{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return StageMessageConfiguration{}, opaqueError("load Stage Message configuration", err)
	}
	if found.StageMessageConfigurationRevision != params.ExpectedRevision {
		configuration, decodeErr := stageMessageConfiguration(found)
		if decodeErr != nil {
			return StageMessageConfiguration{}, decodeErr
		}
		return configuration, ErrStageMessageConfigurationRevision
	}
	encoded, err := json.Marshal(presets)
	if err != nil {
		return StageMessageConfiguration{}, opaqueError("encode Stage Message presets", err)
	}
	updated, err := found.Update().
		SetStageMessagePresets(string(encoded)).
		SetStageMessageDefaultDurationSeconds(params.DefaultDurationSeconds).
		AddStageMessageConfigurationRevision(1).
		Save(ctx)
	if err != nil {
		return StageMessageConfiguration{}, opaqueError("configure Stage Messages", err)
	}
	return stageMessageConfiguration(updated)
}

// ActivateStageMessage resolves one preset or free-form crew Overlay.
func (transaction *CommandTx) ActivateStageMessage(
	ctx context.Context,
	params ActivateStageMessageParams,
) (DisplayOverride, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return DisplayOverride{}, err
	}
	foundEvent, err := transaction.transaction.Event.Get(ctx, params.EventID)
	if ent.IsNotFound(err) {
		return DisplayOverride{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return DisplayOverride{}, opaqueError("load Stage Message Event", err)
	}
	resolved, err := resolveStageMessage(foundEvent, params)
	if err != nil {
		return DisplayOverride{}, err
	}
	if !canOperateDisplayGroup(ctx, params.EventID, resolved.TargetGroupKey) {
		return DisplayOverride{}, ErrDisplayOverrideScope
	}
	return transaction.activateDisplayOverride(
		systemContext(ctx),
		params.EventID,
		resolved.TargetGroupKey,
		DisplayOverrideStageMessage,
		resolved.Text,
		resolved.Emphasis,
		resolved.PresetKey,
		resolved.UntilCleared,
		time.Duration(resolved.DurationSeconds)*time.Second,
		params.Now,
		true,
	)
}

// ActivateTechnicalDifficulties creates one display-only Replace Override.
func (transaction *CommandTx) ActivateTechnicalDifficulties(
	ctx context.Context,
	params ActivateTechnicalDifficultiesParams,
) (DisplayOverride, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return DisplayOverride{}, err
	}
	params, err := normalizeTechnicalDifficulties(params)
	if err != nil {
		return DisplayOverride{}, err
	}
	if !canOperateDisplayGroup(ctx, params.EventID, params.TargetGroupKey) {
		return DisplayOverride{}, ErrDisplayOverrideScope
	}
	return transaction.activateDisplayOverride(
		systemContext(ctx),
		params.EventID,
		params.TargetGroupKey,
		DisplayOverrideTechnicalDifficulties,
		params.Text,
		StageMessageNormal,
		"",
		params.UntilCleared,
		params.Duration,
		params.Now,
		false,
	)
}

func normalizeTechnicalDifficulties(
	params ActivateTechnicalDifficultiesParams,
) (ActivateTechnicalDifficultiesParams, error) {
	params.TargetGroupKey = strings.TrimSpace(params.TargetGroupKey)
	params.Text = strings.TrimSpace(params.Text)
	if params.Text == "" {
		params.Text = "Technical Difficulties\nPlease wait while the issue is resolved."
	}
	if !validDisplayGroupKey(params.TargetGroupKey) || len(params.Text) > 2000 ||
		(!params.UntilCleared && (params.Duration <= 0 || params.Duration > 24*time.Hour)) {
		return ActivateTechnicalDifficultiesParams{}, ErrDisplayOverrideInput
	}
	return params, nil
}

func (transaction *CommandTx) activateDisplayOverride(
	ctx context.Context,
	eventID int,
	targetGroupKey string,
	kind DisplayOverrideKind,
	text string,
	emphasis StageMessageEmphasis,
	presetKey string,
	untilCleared bool,
	duration time.Duration,
	now time.Time,
	crewOnly bool,
) (DisplayOverride, error) {
	if kind == DisplayOverrideStageMessage {
		replaced, err := transaction.transaction.DisplayOverride.Query().
			Where(
				displayoverride.EventIDEQ(eventID),
				displayoverride.KindEQ(displayoverride.KindStageMessage),
				displayoverride.TargetGroupKeyEQ(targetGroupKey),
				displayoverride.ClearedAtIsNil(),
			).
			All(ctx)
		if err != nil {
			return DisplayOverride{}, opaqueError("load replaced Stage Messages", err)
		}
		for _, previous := range replaced {
			if _, err = previous.Update().
				SetClearedAt(now).
				AddRevision(1).
				Save(ctx); err != nil {
				return DisplayOverride{}, opaqueError("replace current Stage Message", err)
			}
		}
	}
	assignments, err := transaction.transaction.DisplayAssignment.Query().
		Where(displayassignment.EventIDEQ(eventID)).
		All(ctx)
	if err != nil {
		return DisplayOverride{}, opaqueError("resolve Display Override target", err)
	}
	displayIDs := make([]int, 0, len(assignments))
	for _, assignment := range assignments {
		if assignmentInDisplayGroup(assignment, targetGroupKey) &&
			(!crewOnly || assignment.ViewKey == "stage-timer") {
			displayIDs = append(displayIDs, assignment.DisplayID)
		}
	}
	create := transaction.transaction.DisplayOverride.Create().
		SetEventID(eventID).
		SetTargetGroupKey(targetGroupKey).
		SetKind(displayoverride.Kind(kind)).
		SetText(text).
		SetEmphasis(displayoverride.Emphasis(emphasis)).
		SetPresetKey(presetKey).
		SetUntilCleared(untilCleared).
		SetCreatedByAccountID(viewerAccountID(ctx)).
		SetCreatedAt(now)
	if !untilCleared {
		create.SetExpiresAt(now.Add(duration))
	}
	created, err := create.Save(ctx)
	if err != nil {
		return DisplayOverride{}, opaqueError("activate Display Override", err)
	}
	for _, displayID := range displayIDs {
		if selectErr := transaction.selectDisplayOverride(
			ctx, eventID, displayID, kind, created.ID, now,
		); selectErr != nil {
			return DisplayOverride{}, selectErr
		}
	}
	return displayOverride(created), nil
}

func (transaction *CommandTx) selectDisplayOverride(
	ctx context.Context,
	eventID, displayID int,
	kind DisplayOverrideKind,
	overrideID int,
	now time.Time,
) error {
	found, err := transaction.transaction.DisplayOverrideState.Query().
		Where(
			displayoverridestate.EventIDEQ(eventID),
			displayoverridestate.DisplayIDEQ(displayID),
			displayoverridestate.KindEQ(displayoverridestate.Kind(kind)),
		).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, err = transaction.transaction.DisplayOverrideState.Create().
			SetEventID(eventID).
			SetDisplayID(displayID).
			SetOverrideID(overrideID).
			SetKind(displayoverridestate.Kind(kind)).
			SetUpdatedAt(now).
			Save(ctx)
	case err == nil:
		_, err = found.Update().
			SetOverrideID(overrideID).
			AddRevision(1).
			SetUpdatedAt(now).
			Save(ctx)
	}
	if err != nil {
		return opaqueError("select current Display Override", err)
	}
	return nil
}

// ClearDisplayOverride clears one exact activation without mutating normal Display state.
func (transaction *CommandTx) ClearDisplayOverride(
	ctx context.Context,
	eventID, overrideID, expectedRevision int,
	now time.Time,
) (DisplayOverride, error) {
	internalContext := systemContext(ctx)
	found, err := transaction.transaction.DisplayOverride.Query().
		Where(
			displayoverride.IDEQ(overrideID),
			displayoverride.EventIDEQ(eventID),
		).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return DisplayOverride{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return DisplayOverride{}, opaqueError("load Display Override", err)
	}
	if !canOperateDisplayGroup(ctx, eventID, found.TargetGroupKey) {
		return DisplayOverride{}, ErrDisplayOverrideScope
	}
	if found.Revision != expectedRevision {
		return displayOverride(found), ErrDisplayOverrideRevision
	}
	updated, err := found.Update().
		SetClearedAt(now).
		AddRevision(1).
		Save(internalContext)
	if err != nil {
		return DisplayOverride{}, opaqueError("clear Display Override", err)
	}
	if DisplayOverrideKind(found.Kind.String()) != DisplayOverrideStageMessage {
		if _, err = transaction.transaction.DisplayOverrideState.Delete().
			Where(displayoverridestate.OverrideIDEQ(found.ID)).
			Exec(internalContext); err != nil {
			return DisplayOverride{}, opaqueError("clear current Display Override selections", err)
		}
	}
	return displayOverride(updated), nil
}

func (transaction *CommandTx) syncDisplayOverridesForAssignment(
	ctx context.Context,
	assignment DisplayAssignment,
	now time.Time,
) error {
	ctx = systemContext(ctx)
	for _, kind := range []DisplayOverrideKind{
		DisplayOverrideStageMessage,
		DisplayOverrideTechnicalDifficulties,
	} {
		query := transaction.transaction.DisplayOverride.Query().
			Where(
				displayoverride.EventIDEQ(assignment.EventID),
				displayoverride.KindEQ(displayoverride.Kind(kind)),
				displayoverride.ClearedAtIsNil(),
				displayoverride.Or(
					displayoverride.UntilClearedEQ(true),
					displayoverride.ExpiresAtGT(now),
				),
			).
			Order(ent.Desc(displayoverride.FieldCreatedAt), ent.Desc(displayoverride.FieldID))
		found, err := query.All(ctx)
		if err != nil {
			return opaqueError("load current Display Group Overrides", err)
		}
		state, stateErr := transaction.transaction.DisplayOverrideState.Query().
			Where(
				displayoverridestate.EventIDEQ(assignment.EventID),
				displayoverridestate.DisplayIDEQ(assignment.DisplayID),
				displayoverridestate.KindEQ(displayoverridestate.Kind(kind)),
			).
			Only(ctx)
		if stateErr != nil && !ent.IsNotFound(stateErr) {
			return opaqueError("load reassigned Display Override", stateErr)
		}
		floor := 0
		if kind == DisplayOverrideStageMessage && stateErr == nil {
			floor = state.OverrideID
		}
		selected := 0
		for _, candidate := range found {
			if candidate.ID >= floor &&
				assignmentInDisplayGroupValue(assignment, candidate.TargetGroupKey) &&
				(kind != DisplayOverrideStageMessage || assignment.ViewKey == "stage-timer") {
				selected = candidate.ID
				break
			}
		}
		if selected == 0 {
			if stateErr == nil && kind != DisplayOverrideStageMessage {
				if deleteErr := transaction.transaction.DisplayOverrideState.DeleteOne(state).
					Exec(ctx); deleteErr != nil {
					return opaqueError("clear reassigned Display Override", deleteErr)
				}
			}
			continue
		}
		if stateErr == nil && state.OverrideID == selected {
			continue
		}
		if selectErr := transaction.selectDisplayOverride(
			ctx, assignment.EventID, assignment.DisplayID, kind, selected, now,
		); selectErr != nil {
			return selectErr
		}
	}
	return nil
}

func resolveStageMessage(
	found *ent.Event,
	params ActivateStageMessageParams,
) (ActivateStageMessageParams, error) {
	params.PresetKey = strings.TrimSpace(params.PresetKey)
	params.Text = strings.TrimSpace(params.Text)
	params.TargetGroupKey = strings.TrimSpace(params.TargetGroupKey)
	if params.PresetKey != "" {
		var presets []StageMessagePreset
		if err := json.Unmarshal([]byte(found.StageMessagePresets), &presets); err != nil {
			return ActivateStageMessageParams{}, opaqueError("decode Stage Message presets", err)
		}
		var selected *StageMessagePreset
		for index := range presets {
			if presets[index].Key == params.PresetKey {
				selected = &presets[index]
				break
			}
		}
		if selected == nil {
			return ActivateStageMessageParams{}, ErrDisplayOverrideInput
		}
		if params.Text == "" {
			params.Text = selected.Text
		}
		if params.TargetGroupKey == "" {
			params.TargetGroupKey = selected.TargetGroupKey
		}
		if params.DurationSeconds == 0 {
			params.DurationSeconds = selected.DurationSeconds
		}
		if params.Emphasis == "" {
			params.Emphasis = selected.Emphasis
		}
	}
	if params.DurationSeconds == 0 {
		params.DurationSeconds = found.StageMessageDefaultDurationSeconds
	}
	if params.Emphasis == "" {
		params.Emphasis = StageMessageNormal
	}
	if params.Text == "" || len(params.Text) > 2000 ||
		!validDisplayGroupKey(params.TargetGroupKey) ||
		!validStageMessageEmphasis(params.Emphasis) ||
		(!params.UntilCleared &&
			(params.DurationSeconds <= 0 || params.DurationSeconds > 24*60*60)) {
		return ActivateStageMessageParams{}, ErrDisplayOverrideInput
	}
	return params, nil
}

func stageMessageConfiguration(found *ent.Event) (StageMessageConfiguration, error) {
	var presets []StageMessagePreset
	if err := json.Unmarshal([]byte(found.StageMessagePresets), &presets); err != nil {
		return StageMessageConfiguration{}, opaqueError("decode Stage Message presets", err)
	}
	return StageMessageConfiguration{
		EventID: found.ID, DefaultDurationSeconds: found.StageMessageDefaultDurationSeconds,
		Presets: presets, Revision: found.StageMessageConfigurationRevision,
	}, nil
}

func normalizeStageMessagePreset(preset StageMessagePreset) StageMessagePreset {
	preset.Key = strings.TrimSpace(preset.Key)
	preset.Text = strings.TrimSpace(preset.Text)
	preset.TargetGroupKey = strings.TrimSpace(preset.TargetGroupKey)
	if preset.Emphasis == "" {
		preset.Emphasis = StageMessageNormal
	}
	return preset
}

func validStageMessagePreset(preset StageMessagePreset) bool {
	return validDisplayGroupKey(preset.Key) &&
		preset.Text != "" && len(preset.Text) <= 2000 &&
		validDisplayGroupKey(preset.TargetGroupKey) &&
		preset.DurationSeconds >= 0 && preset.DurationSeconds <= 24*60*60 &&
		validStageMessageEmphasis(preset.Emphasis)
}

func validStageMessageEmphasis(emphasis StageMessageEmphasis) bool {
	return emphasis == StageMessageNormal ||
		emphasis == StageMessageAttention ||
		emphasis == StageMessageUrgent
}

func validDisplayGroupKey(key string) bool {
	if key == "" || len(key) > 100 {
		return false
	}
	for _, character := range key {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func canOperateDisplayGroup(ctx context.Context, eventID int, key string) bool {
	identity, ok := viewer.FromContext(ctx)
	return ok && identity.CanOperateDisplayGroup(eventID, key)
}

func viewerAccountID(ctx context.Context) int {
	identity, _ := viewer.FromContext(ctx)
	return identity.AccountID
}

func assignmentInDisplayGroup(
	assignment *ent.DisplayAssignment,
	key string,
) bool {
	return assignmentInDisplayGroupValue(
		DisplayAssignment{
			DisplayID: assignment.DisplayID, EventID: assignment.EventID,
			LocationID: assignment.LocationID, ViewKey: assignment.ViewKey,
			DisplayGroupKeys: assignment.DisplayGroupKeys,
		},
		key,
	)
}

func assignmentInDisplayGroupValue(assignment DisplayAssignment, key string) bool {
	if key == "crew" && assignment.ViewKey == "stage-timer" {
		return true
	}
	if key == "public" && assignment.ViewKey != "stage-timer" {
		return true
	}
	return slices.Contains(assignment.DisplayGroupKeys, key)
}

func displayOverride(found *ent.DisplayOverride) DisplayOverride {
	result := DisplayOverride{
		ID: found.ID, EventID: found.EventID, TargetGroupKey: found.TargetGroupKey,
		Kind: DisplayOverrideKind(found.Kind.String()), Text: found.Text,
		Emphasis: StageMessageEmphasis(found.Emphasis.String()), PresetKey: found.PresetKey,
		UntilCleared: found.UntilCleared, Revision: found.Revision,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
	if found.ExpiresAt != nil {
		result.ExpiresAt = *found.ExpiresAt
	}
	if found.ClearedAt != nil {
		result.ClearedAt = *found.ClearedAt
	}
	return result
}
