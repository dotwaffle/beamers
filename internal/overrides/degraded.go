package overrides

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

type degradedReceiptKey struct {
	commandID string
}

type degradedConflictKey struct {
	degradedReceiptKey
	actorID     int
	action      string
	payloadHash string
}

type degradedReceipt struct {
	actorID     int
	action      string
	payloadHash string
	outcome     store.DisplayOverride
	rejection   string
}

type degradedCommandKind uint8

const (
	degradedActivate degradedCommandKind = iota + 1
	degradedClear
)

type degradedCommand struct {
	kind      degradedCommandKind
	actor     auth.Account
	identity  store.CommandIdentity
	outcome   store.DisplayOverride
	rejection store.CommandRejection
	conflict  bool
}

// PrepareEmergencyStorage detects loss of the command evidence boundary before
// authentication or confirmation can expand degraded authority.
func (service *Service) PrepareEmergencyStorage(ctx context.Context) error {
	service.degradedMu.Lock()
	if service.degraded {
		cause := service.degradedCause
		service.degradedMu.Unlock()
		return cause
	}
	service.degradedMu.Unlock()

	err := service.storage.ProbeCommandEvidence(ctx, service.now().UTC())
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		service.markDegraded(err)
	}
	return err
}

// DegradedCause returns the storage failure that opened degraded operation.
func (service *Service) DegradedCause() error {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	return service.degradedCause
}

// ProjectDisplaySnapshot retains healthy Display routing and overlays the
// process-owned Emergency Alert while authoritative storage is unavailable.
func (service *Service) ProjectDisplaySnapshot(
	credentialHash string,
	current store.DisplaySnapshotState,
	loadErr error,
) (store.DisplaySnapshotState, error) {
	if credentialHash == "" {
		return store.DisplaySnapshotState{}, errors.New("display credential hash is required")
	}
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()

	if loadErr == nil && !service.degraded {
		service.displaySnapshots[credentialHash] = cloneDisplaySnapshot(current)
	} else {
		if !service.degraded {
			return store.DisplaySnapshotState{}, loadErr
		}
		cached, ok := service.displaySnapshots[credentialHash]
		if !ok {
			if loadErr == nil {
				loadErr = errors.New("display snapshot was not validated before storage degraded")
			}
			return store.DisplaySnapshotState{}, loadErr
		}
		current = cloneDisplaySnapshot(cached)
	}
	if service.degradedCurrent == nil {
		return current, nil
	}
	emergency := *service.degradedCurrent
	if !emergency.ClearedAt.IsZero() {
		if current.EmergencyAlert != nil && current.EmergencyAlert.ID == emergency.ID {
			current.EmergencyAlert = nil
		}
		return current, nil
	}
	if degradedTargetMatches(current, emergency.EventID, emergency.Target) {
		current.EmergencyAlert = &emergency
	}
	return current, nil
}

// Recover persists process-owned Emergency Alert state and evidence in original
// command order after authoritative storage becomes writable again.
func (service *Service) Recover(ctx context.Context) (bool, error) {
	service.recoveryMu.Lock()
	defer service.recoveryMu.Unlock()

	if service.isDegraded() {
		if err := service.storage.ProbeCommandEvidence(ctx, service.now().UTC()); err != nil {
			return false, err
		}
	}

	recovered := false
	for {
		service.degradedMu.Lock()
		if len(service.degradedPending) == 0 {
			if recovered {
				service.degraded = false
				service.degradedCause = nil
				service.degradedCurrent = nil
				clear(service.degradedReceipts)
				clear(service.degradedConflicts)
			} else if service.degradedCurrent == nil && len(service.degradedReceipts) == 0 {
				service.degraded = false
				service.degradedCause = nil
			}
			service.degradedMu.Unlock()
			return recovered, nil
		}
		pending := service.degradedPending[0]
		service.degradedMu.Unlock()

		if err := service.persistDegradedCommand(ctx, pending); err != nil {
			return recovered, err
		}
		service.degradedMu.Lock()
		service.degradedPending = service.degradedPending[1:]
		service.degradedMu.Unlock()
		recovered = true
	}
}

func (service *Service) persistDegradedCommand(
	ctx context.Context,
	pending degradedCommand,
) error {
	_, err := command.Execute(
		pending.actor.Context(ctx),
		command.Plan[store.DisplayOverride]{
			Storage:  service.storage,
			Identity: pending.identity,
			// The degraded path decided this command's authority in process
			// memory at capture time and is persisting that decision, so the
			// table is not applied to it again.
			Authorization: command.Authorization{
				Facts:    authz.Replayed(),
				Refusals: overrideRejections,
			},
			Replay: func(outcome string) (store.DisplayOverride, error) {
				var replayed store.DisplayOverride
				err := store.DecodeCommandReceipt(outcome, &replayed)
				return replayed, restoreOverrideRejection(err)
			},
			Apply: func(
				transaction *store.CommandTx,
			) (command.Execution[store.DisplayOverride], error) {
				if pending.conflict {
					return command.Execution[store.DisplayOverride]{},
						errors.New("degraded Command conflict lost its original receipt")
				}
				if pending.rejection.Code != "" {
					return command.Reject(
						store.DisplayOverride{},
						pending.rejection,
						overrideRejectionError(pending.rejection.Code),
					), nil
				}
				if _, persistErr := transaction.PersistDegradedEmergencyAlert(
					pending.actor.Context(ctx),
					pending.outcome,
				); persistErr != nil {
					return command.Execution[store.DisplayOverride]{}, persistErr
				}
				encoded, encodeErr := json.Marshal(pending.outcome)
				if encodeErr != nil {
					return command.Execution[store.DisplayOverride]{},
						errors.New("encode recovered Emergency outcome")
				}
				return command.Success(pending.outcome, string(encoded)), nil
			},
		},
	)
	if errors.Is(err, ErrCommandConflict) {
		return nil
	}
	if pending.rejection.Code != "" &&
		errors.Is(err, overrideRejectionError(pending.rejection.Code)) {
		return nil
	}
	return err
}

func (service *Service) previewDegradedEmergency(
	actor auth.Account,
	input PriorityInput,
	storageErr error,
) (PriorityPreview, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	preview, err := service.degradedEmergencyPreviewLocked(actor, input)
	if err != nil {
		if errors.Is(err, ErrEventNotActive) && storageErr != nil {
			return PriorityPreview{}, storageErr
		}
		return PriorityPreview{}, err
	}
	service.degraded = true
	if storageErr != nil && service.degradedCause == nil {
		service.degradedCause = storageErr
	}
	return preview, nil
}

func (service *Service) degradedEmergencyPreviewLocked(
	actor auth.Account,
	input PriorityInput,
) (PriorityPreview, error) {
	input.Text = strings.TrimSpace(input.Text)
	if input.EventID <= 0 || input.Text == "" || len(input.Text) > 2000 ||
		!validPriorityTarget(input.Target) {
		return PriorityPreview{}, ErrInvalidInput
	}
	if !canOperateDegradedEmergency(actor, input.EventID, input.Target) {
		return PriorityPreview{}, ErrScopeDenied
	}
	displays, foundEvent := service.resolveDegradedDisplaysLocked(
		input.EventID,
		input.Target,
	)
	if !foundEvent {
		return PriorityPreview{}, ErrEventNotActive
	}
	preview := store.DisplayOverridePreview{
		Kind:           store.DisplayOverrideEmergencyAlert,
		Target:         input.Target,
		TargetGroupKey: degradedTargetKey(input.Target),
		Text:           input.Text,
		Emphasis:       store.StageMessageNormal,
		Presentation:   store.DisplayOverrideReplace,
		UntilCleared:   true,
		Displays:       displays,
	}
	fingerprint, err := store.DisplayOverridePreviewFingerprint(preview)
	if err != nil {
		return PriorityPreview{}, err
	}
	return PriorityPreview{
		Preview:                 preview,
		ConfirmationFingerprint: command.PayloadHash("nondurable", fingerprint),
		Nondurable:              true,
	}, nil
}

func (service *Service) resolveDegradedDisplaysLocked(
	eventID int,
	target Target,
) ([]store.DisplayOverrideResolvedDisplay, bool) {
	byID := make(map[int]store.DisplayOverrideResolvedDisplay)
	foundEvent := false
	for _, snapshot := range service.displaySnapshots {
		if snapshot.ActiveEventID != eventID {
			continue
		}
		foundEvent = true
		if !degradedTargetMatches(snapshot, eventID, target) {
			continue
		}
		byID[snapshot.Display.ID] = store.DisplayOverrideResolvedDisplay{
			ID: snapshot.Display.ID, Name: snapshot.Display.Name, ViewKey: snapshot.ViewKey,
		}
	}
	result := make([]store.DisplayOverrideResolvedDisplay, 0, len(byID))
	for _, display := range byID {
		result = append(result, display)
	}
	sort.Slice(result, func(first, second int) bool {
		return result[first].ID < result[second].ID
	})
	return result, foundEvent
}

func (service *Service) activateDegradedEmergency(
	actor auth.Account,
	input PriorityInput,
) (Override, error) {
	identity, err := service.degradedCommandIdentity(
		actor,
		input.CommandID,
		"ActivateEmergencyAlert",
		string(input.Target.Type),
		displayTargetID(input.Target),
		input,
	)
	if err != nil {
		return Override{}, err
	}
	key := degradedReceiptKey{commandID: input.CommandID}

	notify := false
	service.degradedMu.Lock()
	defer func() {
		service.degradedMu.Unlock()
		if notify && service.notifyDisplays != nil {
			service.notifyDisplays()
		}
	}()
	if replayed, ok, replayErr := service.degradedReplayLocked(key, actor, identity); ok {
		notify = replayErr == nil
		return replayed, replayErr
	}
	if input.EventID <= 0 || input.DurationSeconds != 0 ||
		input.Presentation != string(store.DisplayOverrideReplace) ||
		!input.UntilCleared || !validEmergencyInput(input) {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrInvalidInput,
		)
	}
	preview, err := service.degradedEmergencyPreviewLocked(actor, input)
	if err != nil {
		if _, rejected := overrideRejection(err); rejected {
			return Override{}, service.retainDegradedRejectionLocked(
				key, actor, identity, err,
			)
		}
		return Override{}, err
	}
	if preview.ConfirmationFingerprint != input.PreviewFingerprint {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrRevision,
		)
	}
	if service.degradedCurrent != nil && service.degradedCurrent.ClearedAt.IsZero() {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrRevision,
		)
	}
	if service.nextDegradedID == math.MaxInt {
		return Override{}, errors.New("degraded Emergency Alert identity space is exhausted")
	}
	service.nextDegradedID++
	activated := store.DisplayOverride{
		ID: service.nextDegradedID, EventID: input.EventID,
		TargetGroupKey: degradedTargetKey(input.Target), Target: input.Target,
		Kind: store.DisplayOverrideEmergencyAlert, Presentation: store.DisplayOverrideReplace,
		Text: input.Text, Emphasis: store.StageMessageNormal, UntilCleared: true,
		Revision: 1, CreatedByAccountID: actor.ID, CreatedAt: identity.Now,
		Nondurable: true,
	}
	service.degraded = true
	service.degradedCurrent = cloneDisplayOverride(&activated)
	service.degradedReceipts[key] = degradedReceipt{
		actorID: identity.ActorAccountID,
		action:  identity.Action, payloadHash: identity.PayloadHash, outcome: activated,
	}
	service.degradedPending = append(service.degradedPending, degradedCommand{
		kind: degradedActivate, actor: cloneActor(actor), identity: identity, outcome: activated,
	})
	notify = true
	return activated, nil
}

func (service *Service) clearDegradedEmergency(
	actor auth.Account,
	input ClearInput,
) (Override, error) {
	identity, err := service.degradedCommandIdentity(
		actor,
		input.CommandID,
		"ClearDisplayOverride",
		"DisplayOverride",
		strconv.Itoa(input.OverrideID),
		input,
	)
	if err != nil {
		return Override{}, err
	}
	key := degradedReceiptKey{commandID: input.CommandID}

	notify := false
	service.degradedMu.Lock()
	defer func() {
		service.degradedMu.Unlock()
		if notify && service.notifyDisplays != nil {
			service.notifyDisplays()
		}
	}()
	if replayed, ok, replayErr := service.degradedReplayLocked(key, actor, identity); ok {
		notify = replayErr == nil
		return replayed, replayErr
	}
	if input.EventID <= 0 || input.OverrideID <= 0 || input.ExpectedRevision <= 0 ||
		!input.Confirmed || !validEmergencyConfirmation(input.ConfirmationMethod) {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrInvalidInput,
		)
	}
	current := service.degradedCurrent
	if current == nil || current.ID != input.OverrideID {
		current = service.cachedEmergencyLocked(input.EventID, input.OverrideID)
	}
	if current == nil || current.EventID != input.EventID {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrNotFound,
		)
	}
	if !current.ClearedAt.IsZero() || current.Revision != input.ExpectedRevision {
		return *current, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrRevision,
		)
	}
	if !canOperateDegradedEmergency(actor, input.EventID, current.Target) {
		return Override{}, service.retainDegradedRejectionLocked(
			key, actor, identity, ErrScopeDenied,
		)
	}
	cleared := *current
	cleared.Revision++
	cleared.ClearedAt = identity.Now
	cleared.Nondurable = true
	service.degraded = true
	service.degradedCurrent = cloneDisplayOverride(&cleared)
	service.degradedReceipts[key] = degradedReceipt{
		actorID: identity.ActorAccountID,
		action:  identity.Action, payloadHash: identity.PayloadHash, outcome: cleared,
	}
	service.degradedPending = append(service.degradedPending, degradedCommand{
		kind: degradedClear, actor: cloneActor(actor), identity: identity, outcome: cleared,
	})
	notify = true
	return cleared, nil
}

func (service *Service) cachedEmergencyLocked(
	eventID int,
	overrideID int,
) *store.DisplayOverride {
	for _, snapshot := range service.displaySnapshots {
		if snapshot.ActiveEventID == eventID && snapshot.EmergencyAlert != nil &&
			snapshot.EmergencyAlert.ID == overrideID {
			return cloneDisplayOverride(snapshot.EmergencyAlert)
		}
	}
	return nil
}

func (service *Service) listDegradedEmergency(
	actor auth.Account,
	eventID int,
	storageErr error,
) ([]ActiveOverride, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	current := service.degradedCurrent
	if current == nil {
		for _, snapshot := range service.displaySnapshots {
			if snapshot.ActiveEventID != eventID || snapshot.EmergencyAlert == nil {
				continue
			}
			if current == nil ||
				snapshot.EmergencyAlert.CreatedAt.After(current.CreatedAt) ||
				snapshot.EmergencyAlert.CreatedAt.Equal(current.CreatedAt) &&
					snapshot.EmergencyAlert.ID > current.ID {
				current = snapshot.EmergencyAlert
			}
		}
	}
	if current == nil {
		if storageErr != nil {
			return nil, storageErr
		}
		return []ActiveOverride{}, nil
	}
	if !canOperateDegradedEmergency(actor, eventID, current.Target) {
		return nil, ErrScopeDenied
	}
	service.degraded = true
	if storageErr != nil && service.degradedCause == nil {
		service.degradedCause = storageErr
	}
	if !current.ClearedAt.IsZero() {
		return []ActiveOverride{}, nil
	}
	displays, _ := service.resolveDegradedDisplaysLocked(eventID, current.Target)
	projected := *current
	projected.Nondurable = true
	return []ActiveOverride{{
		DisplayOverride: projected,
		Displays:        displays,
	}}, nil
}

func (service *Service) degradedReplayLocked(
	key degradedReceiptKey,
	actor auth.Account,
	identity store.CommandIdentity,
) (store.DisplayOverride, bool, error) {
	receipt, ok := service.degradedReceipts[key]
	if !ok {
		return store.DisplayOverride{}, false, nil
	}
	if receipt.actorID != identity.ActorAccountID ||
		receipt.action != identity.Action ||
		receipt.payloadHash != identity.PayloadHash {
		conflict := degradedConflictKey{
			degradedReceiptKey: key,
			actorID:            identity.ActorAccountID,
			action:             identity.Action,
			payloadHash:        identity.PayloadHash,
		}
		if _, retained := service.degradedConflicts[conflict]; !retained {
			service.degradedConflicts[conflict] = struct{}{}
			service.degradedPending = append(service.degradedPending, degradedCommand{
				actor: cloneActor(actor), identity: identity, conflict: true,
			})
		}
		return store.DisplayOverride{}, true, ErrCommandConflict
	}
	if receipt.rejection != "" {
		return receipt.outcome, true, overrideRejectionError(receipt.rejection)
	}
	return receipt.outcome, true, nil
}

func (service *Service) retainDegradedRejectionLocked(
	key degradedReceiptKey,
	actor auth.Account,
	identity store.CommandIdentity,
	rejectionErr error,
) error {
	rejection, _ := overrideRejection(rejectionErr)
	service.degradedReceipts[key] = degradedReceipt{
		actorID: identity.ActorAccountID,
		action:  identity.Action, payloadHash: identity.PayloadHash,
		rejection: rejection.Code,
	}
	service.degradedPending = append(service.degradedPending, degradedCommand{
		actor: cloneActor(actor), identity: identity, rejection: rejection,
	})
	return rejectionErr
}

func (service *Service) degradedCommandIdentity(
	actor auth.Account,
	commandID string,
	action string,
	targetType string,
	targetID string,
	payload any,
) (store.CommandIdentity, error) {
	if err := command.ValidateID(commandID); err != nil {
		return store.CommandIdentity{}, err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return store.CommandIdentity{}, errors.New("encode degraded Emergency command")
	}
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(encodedPayload)), Action: action,
		TargetType: targetType, TargetID: targetID, Now: service.now().UTC(),
	}, nil
}

func (service *Service) isDegraded() bool {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	return service.degraded
}

func (service *Service) markDegraded(cause error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	service.degraded = true
	if cause != nil && service.degradedCause == nil {
		service.degradedCause = cause
	}
}

func (service *Service) activateDurably(
	activate func() (Override, error),
) (Override, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	if service.degraded {
		if service.degradedCause != nil {
			return Override{}, service.degradedCause
		}
		return Override{}, auth.ErrStorageDegraded
	}
	activated, err := activate()
	if err == nil && activated.ID > 0 {
		service.nextDegradedID = max(service.nextDegradedID, activated.ID)
	}
	return activated, err
}

// Degraded reports whether Emergency Alert commands are being retained in
// process memory while durable command evidence is unavailable.
func (service *Service) Degraded() bool {
	return service.isDegraded()
}

func canOperateDegradedEmergency(
	actor auth.Account,
	eventID int,
	target Target,
) bool {
	identity := viewer.Identity{
		AccountID: actor.ID, Administrator: actor.Administrator,
		EventRoles: actor.EventRoles, EventScopes: actor.EventScopes,
	}
	if !identity.HasCapability(eventID, viewer.EmergencyAlert) {
		return false
	}
	if identity.CanProduceEvent(eventID) {
		return true
	}
	if target.Type == store.DisplayOverrideTargetLane {
		return identity.CanOperateLane(eventID, target.ID)
	}
	return identity.CanOperateDisplayGroup(eventID, degradedTargetKey(target))
}

func degradedTargetKey(target Target) string {
	switch target.Type {
	case store.DisplayOverrideTargetEvent:
		return "event"
	case store.DisplayOverrideTargetPublic:
		return "public"
	case store.DisplayOverrideTargetCrew:
		return "crew"
	case store.DisplayOverrideTargetDisplayGroup:
		return target.Key
	case store.DisplayOverrideTargetLocation,
		store.DisplayOverrideTargetLane,
		store.DisplayOverrideTargetProgramChannel,
		store.DisplayOverrideTargetDisplay:
		return strings.ToLower(string(target.Type)) + ":" + strconv.Itoa(target.ID)
	default:
		return ""
	}
}

func degradedTargetMatches(
	snapshot store.DisplaySnapshotState,
	eventID int,
	target Target,
) bool {
	if snapshot.ActiveEventID != eventID || snapshot.Standby || snapshot.LocationID <= 0 {
		return false
	}
	switch target.Type {
	case store.DisplayOverrideTargetEvent:
		return true
	case store.DisplayOverrideTargetPublic:
		return snapshot.ViewKey != "stage-timer"
	case store.DisplayOverrideTargetCrew:
		return snapshot.ViewKey == "stage-timer"
	case store.DisplayOverrideTargetLocation:
		return snapshot.LocationID == target.ID
	case store.DisplayOverrideTargetLane:
		return slices.Contains(snapshot.TargetLaneIDs, target.ID)
	case store.DisplayOverrideTargetProgramChannel:
		return snapshot.ViewKey == "competition-output" &&
			snapshot.ProgramChannelID == target.ID
	case store.DisplayOverrideTargetDisplayGroup:
		return slices.Contains(snapshot.DisplayGroupKeys, target.Key)
	case store.DisplayOverrideTargetDisplay:
		return snapshot.Display.ID == target.ID
	default:
		return false
	}
}

func cloneDisplaySnapshot(source store.DisplaySnapshotState) store.DisplaySnapshotState {
	cloned := source
	cloned.DisplayGroupKeys = slices.Clone(source.DisplayGroupKeys)
	cloned.TargetLaneIDs = slices.Clone(source.TargetLaneIDs)
	cloned.Sessions = slices.Clone(source.Sessions)
	cloned.StageMessage = cloneDisplayOverride(source.StageMessage)
	cloned.TechnicalDifficulties = cloneDisplayOverride(source.TechnicalDifficulties)
	cloned.UrgentNotice = cloneDisplayOverride(source.UrgentNotice)
	cloned.EmergencyAlert = cloneDisplayOverride(source.EmergencyAlert)
	return cloned
}

func cloneDisplayOverride(source *store.DisplayOverride) *store.DisplayOverride {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func cloneActor(source auth.Account) auth.Account {
	cloned := source
	cloned.EventRoles = make(map[int]viewer.Role, len(source.EventRoles))
	maps.Copy(cloned.EventRoles, source.EventRoles)
	cloned.EventScopes = make(map[int]viewer.EventScope, len(source.EventScopes))
	for eventID, scope := range source.EventScopes {
		clonedScope := viewer.EventScope{
			LaneIDs:          make(map[int]struct{}, len(scope.LaneIDs)),
			DisplayGroupKeys: make(map[string]struct{}, len(scope.DisplayGroupKeys)),
			Capabilities:     make(map[viewer.Capability]struct{}, len(scope.Capabilities)),
		}
		for laneID := range scope.LaneIDs {
			clonedScope.LaneIDs[laneID] = struct{}{}
		}
		for key := range scope.DisplayGroupKeys {
			clonedScope.DisplayGroupKeys[key] = struct{}{}
		}
		for capability := range scope.Capabilities {
			clonedScope.Capabilities[capability] = struct{}{}
		}
		cloned.EventScopes[eventID] = clonedScope
	}
	return cloned
}
