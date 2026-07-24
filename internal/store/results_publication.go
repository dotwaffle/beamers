package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventawardsdraft"
	"github.com/dotwaffle/beamers/ent/resultspublication"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

var (
	// ErrResultsPublicationRevision means a release scope advanced after observation.
	ErrResultsPublicationRevision = errors.New("results publication revision conflict")
	// ErrResultsPublicationTransition means an append would retract or rewrite public truth.
	ErrResultsPublicationTransition = errors.New("invalid Results Publication transition")
)

// ResultsPublicationScope identifies one independently released result set.
type ResultsPublicationScope string

const (
	// ResultsPublicationPrizegiving identifies one Ceremony release scope.
	ResultsPublicationPrizegiving ResultsPublicationScope = "Prizegiving"
	// ResultsPublicationStandalone identifies one unassigned Competition scope.
	ResultsPublicationStandalone ResultsPublicationScope = "Standalone"
)

// ResultsPublicationPolicy selects the release trigger for one scope.
type ResultsPublicationPolicy = prizegivingvalue.ReleasePolicy

const (
	// ResultsPublicationAllAtCue releases a locked set atomically at its cue.
	ResultsPublicationAllAtCue = prizegivingvalue.ReleaseAllAtCue
	// ResultsPublicationProgressive releases completed Result Items.
	ResultsPublicationProgressive = prizegivingvalue.ReleaseProgressiveOnReveal
	// ResultsPublicationAtCeremonyEnd releases a resolved set at completion.
	ResultsPublicationAtCeremonyEnd = prizegivingvalue.ReleaseAtCeremonyEnd
	// ResultsPublicationStandalonePolicy releases one unassigned Competition.
	ResultsPublicationStandalonePolicy = prizegivingvalue.ReleaseStandalone
)

// ResultsPublicationStatus describes a scope's current public state.
type ResultsPublicationStatus string

const (
	// ResultsPublicationPartial contains a released subset.
	ResultsPublicationPartial ResultsPublicationStatus = "Partial"
	// ResultsPublicationFinal contains the complete scope.
	ResultsPublicationFinal ResultsPublicationStatus = "Final"
)

// ResultsPublication is one immutable release-manifest revision.
type ResultsPublication struct {
	EventID            int
	Scope              ResultsPublicationScope
	ScopeSessionID     int
	Revision           int
	Policy             ResultsPublicationPolicy
	Status             ResultsPublicationStatus
	Items              []PrizegivingResultItemRef
	Lock               PrizegivingPreflightLock
	Template           PrizegivingResultsTextTemplate
	RenderedHTML       string
	RenderedText       string
	RenderedJSON       string
	CreatedByAccountID int
	CreatedAt          time.Time
}

// ResultsPublicationRenderSource contains exact facts resolved for rendering.
type ResultsPublicationRenderSource struct {
	EventName        string                                `json:"event_name"`
	Competitions     []ResultsPublicationCompetitionSource `json:"competitions"`
	EventAwards      []EventAward                          `json:"event_awards"`
	RecipientEntries []CompetitionEntry                    `json:"recipient_entries"`
}

// ResultsPublicationCompetitionSource is one locked Competition and its public facts.
type ResultsPublicationCompetitionSource struct {
	Title   string                  `json:"title"`
	Draft   CompetitionResultsDraft `json:"draft"`
	Entries []CompetitionEntry      `json:"entries"`
}

// AppendResultsPublicationParams contains one complete immutable manifest.
type AppendResultsPublicationParams struct {
	EventID            int
	Scope              ResultsPublicationScope
	ScopeSessionID     int
	ExpectedRevision   int
	Policy             ResultsPublicationPolicy
	Status             ResultsPublicationStatus
	Items              []PrizegivingResultItemRef
	Lock               PrizegivingPreflightLock
	Template           PrizegivingResultsTextTemplate
	RenderedHTML       string
	RenderedText       string
	RenderedJSON       string
	CreatedByAccountID int
	Now                time.Time
}

// AppendResultsPublication appends one monotonic manifest revision.
func (transaction *CommandTx) AppendResultsPublication(
	ctx context.Context,
	params AppendResultsPublicationParams,
) (ResultsPublication, error) {
	ctx = systemContext(ctx)
	current, found, err := loadResultsPublication(
		ctx,
		transaction.transaction.Client(),
		params.EventID,
		params.Scope,
		params.ScopeSessionID,
	)
	if err != nil {
		return ResultsPublication{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return current, ErrResultsPublicationRevision
	}
	if found && !validResultsPublicationAppend(current, params) {
		return current, ErrResultsPublicationTransition
	}
	create := transaction.transaction.ResultsPublication.Create().
		SetEventID(params.EventID).
		SetScope(resultspublication.Scope(params.Scope)).
		SetScopeSessionID(params.ScopeSessionID).
		SetRevision(params.ExpectedRevision + 1).
		SetReleasePolicy(resultspublication.ReleasePolicy(params.Policy)).
		SetStatus(resultspublication.Status(params.Status)).
		SetItems(prizegivingItemRefValues(params.Items)).
		SetPrizegivingLock(prizegivingLockValue(params.Lock)).
		SetResultsTextTemplate(prizegivingTemplateValue(params.Template)).
		SetRenderedHTML(params.RenderedHTML).
		SetRenderedText(params.RenderedText).
		SetRenderedJSON(params.RenderedJSON).
		SetCreatedAt(params.Now)
	if params.CreatedByAccountID > 0 {
		create.SetCreatedByAccountID(params.CreatedByAccountID)
	}
	created, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return ResultsPublication{}, ErrResultsPublicationRevision
		}
		return ResultsPublication{}, opaqueError("append Results Publication", err)
	}
	return resultsPublication(created), nil
}

// LoadResultsPublicationRenderSource resolves one lock inside the release transaction.
func (transaction *CommandTx) LoadResultsPublicationRenderSource(
	ctx context.Context,
	eventID int,
	lock PrizegivingPreflightLock,
) (ResultsPublicationRenderSource, error) {
	return loadResultsPublicationRenderSource(
		systemContext(ctx),
		transaction.transaction.Client(),
		eventID,
		lock,
	)
}

// LoadResultsPublicationRenderSource resolves one lock for side-effect-free Preview.
func (installation *SQLite) LoadResultsPublicationRenderSource(
	ctx context.Context,
	eventID int,
	lock PrizegivingPreflightLock,
) (ResultsPublicationRenderSource, error) {
	return loadResultsPublicationRenderSource(
		systemContext(ctx),
		installation.client,
		eventID,
		lock,
	)
}

func loadResultsPublicationRenderSource(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	lock PrizegivingPreflightLock,
) (ResultsPublicationRenderSource, error) {
	ctx = systemContext(ctx)
	if len(lock.RenderSource) != 0 {
		var frozen ResultsPublicationRenderSource
		if json.Unmarshal(lock.RenderSource, &frozen) != nil {
			return ResultsPublicationRenderSource{}, ErrResultsPublicationTransition
		}
		return frozen, nil
	}
	foundEvent, err := client.Event.Query().
		Where(event.IDEQ(eventID)).
		Only(ctx)
	if err != nil {
		return ResultsPublicationRenderSource{}, opaqueError("load Results Publication Event", err)
	}
	source := ResultsPublicationRenderSource{
		EventName:    foundEvent.Name,
		Competitions: make([]ResultsPublicationCompetitionSource, 0, len(lock.CompetitionSources)),
	}
	for _, locked := range lock.CompetitionSources {
		draft, loadErr := loadCompetitionResultsDraftByID(
			ctx,
			client,
			locked.DraftID,
		)
		if loadErr != nil {
			return ResultsPublicationRenderSource{}, loadErr
		}
		if draft.EventID != eventID ||
			draft.SessionID != locked.SessionID ||
			draft.Revision != locked.DraftRevision ||
			draft.Disposition != locked.Disposition {
			return ResultsPublicationRenderSource{}, ErrResultsPublicationTransition
		}
		competition, loadErr := client.Session.Query().
			Where(session.IDEQ(locked.SessionID), session.EventIDEQ(eventID)).
			Only(ctx)
		if loadErr != nil {
			return ResultsPublicationRenderSource{}, opaqueError(
				"load Results Publication Competition",
				loadErr,
			)
		}
		version, loadErr := competition.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if loadErr != nil || version.Type != sessionpublishedversion.TypeCompetition {
			return ResultsPublicationRenderSource{}, ErrResultsPublicationTransition
		}
		foundEntries, loadErr := client.CompetitionEntry.Query().
			Where(
				competitionentry.EventIDEQ(eventID),
				competitionentry.CompetitionSessionIDEQ(locked.SessionID),
			).
			Order(
				ent.Asc(competitionentry.FieldCreatedAt),
				ent.Asc(competitionentry.FieldID),
			).
			All(ctx)
		if loadErr != nil {
			return ResultsPublicationRenderSource{}, opaqueError(
				"load Results Publication Entries",
				loadErr,
			)
		}
		entries := make([]CompetitionEntry, 0, len(foundEntries))
		for _, found := range foundEntries {
			entries = append(entries, competitionEntry(found))
		}
		source.Competitions = append(source.Competitions, ResultsPublicationCompetitionSource{
			Title: version.Title, Draft: draft, Entries: entries,
		})
	}
	if lock.EventAwardsDraftRevision == 0 {
		return source, nil
	}
	foundAwards, err := client.EventAwardsDraft.Query().
		Where(
			eventawardsdraft.EventIDEQ(eventID),
			eventawardsdraft.RevisionEQ(lock.EventAwardsDraftRevision),
		).
		Only(ctx)
	if err != nil {
		return ResultsPublicationRenderSource{}, opaqueError(
			"load Results Publication Event Awards",
			err,
		)
	}
	source.EventAwards = slices.Clone(eventAwardsDraft(foundAwards).Awards)
	recipientIDs := eventAwardRecipientEntryIDs(source.EventAwards)
	if len(recipientIDs) == 0 {
		return source, nil
	}
	foundRecipients, err := client.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.IDIn(recipientIDs...),
		).
		All(ctx)
	if err != nil {
		return ResultsPublicationRenderSource{}, opaqueError(
			"load Results Publication Award recipients",
			err,
		)
	}
	if len(foundRecipients) != len(recipientIDs) {
		return ResultsPublicationRenderSource{}, ErrResultsPublicationTransition
	}
	source.RecipientEntries = make([]CompetitionEntry, 0, len(foundRecipients))
	for _, found := range foundRecipients {
		source.RecipientEntries = append(source.RecipientEntries, competitionEntry(found))
	}
	return source, nil
}

// LoadResultsPublication returns the latest manifest or zero before first release.
func (installation *SQLite) LoadResultsPublication(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, error) {
	found, _, err := loadResultsPublication(
		systemContext(ctx),
		installation.client,
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

// LoadResultsPublication returns the latest manifest inside a command transaction.
func (transaction *CommandTx) LoadResultsPublication(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, error) {
	found, _, err := loadResultsPublication(
		systemContext(ctx),
		transaction.transaction.Client(),
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

func loadResultsPublication(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, bool, error) {
	found, err := client.ResultsPublication.Query().
		Where(
			resultspublication.EventIDEQ(eventID),
			resultspublication.ScopeEQ(resultspublication.Scope(scope)),
			resultspublication.ScopeSessionIDEQ(scopeSessionID),
		).
		Order(ent.Desc(resultspublication.FieldRevision)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ResultsPublication{}, false, nil
	}
	if err != nil {
		return ResultsPublication{}, false, opaqueError("load Results Publication", err)
	}
	return resultsPublication(found), true, nil
}

func validResultsPublicationAppend(
	current ResultsPublication,
	params AppendResultsPublicationParams,
) bool {
	if current.Status == ResultsPublicationFinal ||
		current.Policy != params.Policy ||
		!sameResultsPublicationLock(current.Lock, params.Lock) {
		return false
	}
	nextItems := make(map[PrizegivingResultItemRef]struct{}, len(params.Items))
	for _, ref := range params.Items {
		nextItems[ref] = struct{}{}
	}
	for _, ref := range current.Items {
		if _, ok := nextItems[ref]; !ok {
			return false
		}
	}
	return true
}

func sameResultsPublicationLock(
	current, next PrizegivingPreflightLock,
) bool {
	if len(current.RenderSource) == 0 {
		next.RenderSource = nil
	}
	return reflect.DeepEqual(current, next)
}

func resultsPublication(found *ent.ResultsPublication) ResultsPublication {
	result := ResultsPublication{
		EventID:        found.EventID,
		Scope:          ResultsPublicationScope(found.Scope),
		ScopeSessionID: found.ScopeSessionID,
		Revision:       found.Revision,
		Policy:         ResultsPublicationPolicy(found.ReleasePolicy),
		Status:         ResultsPublicationStatus(found.Status),
		Items:          prizegivingItemRefs(found.Items),
		Lock:           prizegivingLock(found.PrizegivingLock),
		Template:       prizegivingTemplate(found.ResultsTextTemplate),
		RenderedHTML:   found.RenderedHTML,
		RenderedText:   found.RenderedText,
		RenderedJSON:   found.RenderedJSON,
		CreatedAt:      found.CreatedAt,
	}
	if found.CreatedByAccountID != nil {
		result.CreatedByAccountID = *found.CreatedByAccountID
	}
	return result
}
