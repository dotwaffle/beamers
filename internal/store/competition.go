package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/attachment"
	"github.com/dotwaffle/beamers/ent/attachmentversion"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

var (
	// ErrCompetitionNotFound means no published Competition matched the stable IDs.
	ErrCompetitionNotFound = errors.New("competition not found")
	// ErrCompetitionSubmissionClosed means the fixed Deadline has arrived.
	ErrCompetitionSubmissionClosed = errors.New("competition submissions are closed")
	// ErrCompetitionEntryNotFound means no retained Entry matched the stable IDs.
	ErrCompetitionEntryNotFound = errors.New("competition entry not found")
	// ErrCompetitionEntryRevision means an Entry command used a stale revision.
	ErrCompetitionEntryRevision = errors.New("competition entry revision conflict")
	// ErrLiveDispositionConfirmation means a live change lacked explicit confirmation.
	ErrLiveDispositionConfirmation = errors.New("live Competition disposition change requires confirmation")
	// ErrCompetitionPreflightBlocked means configured readiness rules are not satisfied.
	ErrCompetitionPreflightBlocked = errors.New("competition Start preflight is blocked")
	// ErrCompetitionReadinessRevision means policy configuration used stale state.
	ErrCompetitionReadinessRevision = errors.New("competition readiness revision conflict")
	// ErrAttachmentReadinessRevision means Attachment readiness used stale state.
	ErrAttachmentReadinessRevision = errors.New("attachment readiness revision conflict")
)

// CompetitionEntry is one retained Competition submission.
type CompetitionEntry struct {
	ID                            int       `json:"id"`
	CompetitionSessionID          int       `json:"competition_session_id"`
	Name                          string    `json:"name"`
	PublicDetails                 string    `json:"public_details,omitempty"`
	CrewNotes                     string    `json:"crew_notes,omitempty"`
	Disposition                   string    `json:"disposition"`
	Revision                      int       `json:"revision"`
	ContentRevision               int       `json:"content_revision"`
	ReviewCurrent                 bool      `json:"review_current"`
	PresentationStatus            string    `json:"presentation_status"`
	DeferredSequence              int       `json:"deferred_sequence,omitempty"`
	ResolutionRequired            bool      `json:"resolution_required"`
	ResultDisposition             string    `json:"result_disposition"`
	TechnicalFailureReason        string    `json:"technical_failure_reason,omitempty"`
	ResolutionCrewReason          string    `json:"resolution_crew_reason,omitempty"`
	PublicDisqualificationMessage string    `json:"public_disqualification_message,omitempty"`
	ReleaseHold                   bool      `json:"release_hold"`
	FirstPresentedAt              time.Time `json:"first_presented_at,omitzero"`
	CreatedAt                     time.Time `json:"created_at"`
}

// CompetitionState is the current fixed configuration and retained Entries.
type CompetitionState struct {
	EventID                     int
	SessionID                   int
	SubmissionDeadline          time.Time
	EffectiveDefaultDisposition string
	RequireEntryReview          bool
	FileDeliveryRequired        bool
	ReadinessRevision           int
	EntryOrder                  EntryOrderState
	Entries                     []CompetitionEntry
	ResultsReady                bool
	ReleaseReady                bool
}

// CompetitionPreflightCode is the closed vocabulary of Start blockers.
type CompetitionPreflightCode string

const (
	preflightPendingEntry     CompetitionPreflightCode = "pending_entry"
	preflightUnresolvedReview CompetitionPreflightCode = "unresolved_entry_review"
	preflightMissingDelivery  CompetitionPreflightCode = "missing_file_delivery"
	preflightAmbiguousPrimary CompetitionPreflightCode = "ambiguous_primary_attachment"
	preflightNonFinalPrimary  CompetitionPreflightCode = "non_final_primary_attachment"
)

// CompetitionPreflightFinding is one stable actionable Start blocker.
type CompetitionPreflightFinding struct {
	Code    CompetitionPreflightCode
	Message string
	EntryID int
}

// CompetitionPreflight is the current readiness result for one Competition.
type CompetitionPreflight struct {
	EventID, SessionID   int
	RequireEntryReview   bool
	FileDeliveryRequired bool
	Blockers             []CompetitionPreflightFinding
	Attachments          []AttachmentReadiness
}

// CompetitionPreflightBlockedError preserves explicit blocker codes for Start.
type CompetitionPreflightBlockedError struct {
	Blockers []CompetitionPreflightFinding
}

// Error summarizes the actionable blocker codes without hiding them.
func (err *CompetitionPreflightBlockedError) Error() string {
	codes := make([]string, 0, len(err.Blockers))
	for _, blocker := range err.Blockers {
		codes = append(codes, fmt.Sprintf("%s(entry_id=%d)", blocker.Code, blocker.EntryID))
	}
	return "competition Start preflight blocked: " + strings.Join(codes, ", ")
}

// Unwrap preserves stable failed-precondition classification.
func (err *CompetitionPreflightBlockedError) Unwrap() error {
	return ErrCompetitionPreflightBlocked
}

// CreateCompetitionEntryParams contains one new Entry.
type CreateCompetitionEntryParams struct {
	EventID, SessionID  int
	Name, PublicDetails string
	CrewNotes           string
	Now                 time.Time
}

// UpdateCompetitionEntryParams contains one optimistic Entry content change.
type UpdateCompetitionEntryParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Name, PublicDetails         string
	CrewNotes                   string
	Now                         time.Time
}

// ChangeCompetitionEntryDispositionParams contains one participation change.
type ChangeCompetitionEntryDispositionParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Disposition                 string
	ConfirmedLive               bool
	Now                         time.Time
}

// ConfigureCompetitionReadinessParams changes independent Start policies.
type ConfigureCompetitionReadinessParams struct {
	EventID, SessionID        int
	ExpectedReadinessRevision int
	RequireEntryReview        bool
	FileDeliveryRequired      bool
}

// CompetitionReadiness is current Competition Start policy state.
type CompetitionReadiness struct {
	RequireEntryReview   bool `json:"require_entry_review"`
	FileDeliveryRequired bool `json:"file_delivery_required"`
	ReadinessRevision    int  `json:"readiness_revision"`
}

// ReviewCompetitionEntryParams confirms one exact content revision.
type ReviewCompetitionEntryParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	ReviewerAccountID           int
	Now                         time.Time
}

// SetEntryAttachmentReadinessParams changes independent Final and Primary facts.
type SetEntryAttachmentReadinessParams struct {
	EventID, SessionID, EntryID int
	AttachmentVersionID         int
	ExpectedRevision            int
	Final, Primary              bool
}

// AttachmentReadiness is current Final and Primary state.
type AttachmentReadiness struct {
	AttachmentVersionID int    `json:"attachment_version_id"`
	EntryID             int    `json:"entry_id"`
	AttachmentVersion   int    `json:"attachment_version"`
	LogicalName         string `json:"logical_name"`
	OriginalFilename    string `json:"original_filename"`
	ReadinessRevision   int    `json:"readiness_revision"`
	Final               bool   `json:"final"`
	Primary             bool   `json:"primary"`
}

// LoadCompetition returns current published Competition configuration and Entries.
func (installation *SQLite) LoadCompetition(ctx context.Context, eventID, sessionID int) (CompetitionState, error) {
	state, found, err := loadCompetitionConfiguration(
		ctx, installation.client.Session, installation.client.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionState{}, err
	}
	entries, err := installation.client.CompetitionEntry.Query().
		Where(competitionentry.CompetitionSessionIDEQ(sessionID)).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return CompetitionState{}, opaqueError("load Competition Entries", err)
	}
	for _, entry := range entries {
		state.Entries = append(state.Entries, competitionEntry(entry))
	}
	state.ResultsReady = true
	state.ReleaseReady = true
	for _, entry := range state.Entries {
		if entry.ResolutionRequired {
			state.ResultsReady = false
			state.ReleaseReady = false
		}
	}
	included := make([]*ent.CompetitionEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Disposition == competitionentry.DispositionIncluded {
			included = append(included, entry)
		}
	}
	state.EntryOrder, _, err = competitionEntryOrder(found, included)
	if err != nil {
		return CompetitionState{}, err
	}
	return state, nil
}

// PreflightCompetitionStart applies unambiguous readiness automation and reports blockers.
func (installation *SQLite) PreflightCompetitionStart(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionPreflight, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return CompetitionPreflight{}, opaqueError("begin Competition Preflight", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := loadCompetitionPreflight(ctx, transaction.Client(), eventID, sessionID)
	if err != nil {
		return CompetitionPreflight{}, err
	}
	if err := transaction.Commit(); err != nil {
		return CompetitionPreflight{}, opaqueError("commit Competition Preflight", err)
	}
	return result, nil
}

// PreflightCompetitionStart applies automation inside a Start command transaction.
func (transaction *CommandTx) PreflightCompetitionStart(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionPreflight, error) {
	return loadCompetitionPreflight(ctx, transaction.transaction.Client(), eventID, sessionID)
}

func loadCompetitionPreflight(
	ctx context.Context,
	client *ent.Client,
	eventID, sessionID int,
) (CompetitionPreflight, error) {
	state, _, err := loadCompetitionConfiguration(
		ctx, client.Session, client.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionPreflight{}, err
	}
	result := CompetitionPreflight{
		EventID: eventID, SessionID: sessionID,
		RequireEntryReview:   state.RequireEntryReview,
		FileDeliveryRequired: state.FileDeliveryRequired,
	}
	internalContext := systemContext(ctx)
	entries, err := client.CompetitionEntry.Query().
		Where(competitionentry.CompetitionSessionIDEQ(sessionID)).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(internalContext)
	if err != nil {
		return CompetitionPreflight{}, opaqueError("load Competition Preflight Entries", err)
	}
	for _, entry := range entries {
		switch entry.Disposition {
		case competitionentry.DispositionPending:
			result.Blockers = append(result.Blockers, CompetitionPreflightFinding{
				Code: preflightPendingEntry, Message: "Entry disposition must be Included or Rejected",
				EntryID: entry.ID,
			})
		case competitionentry.DispositionIncluded:
			reviewCurrent := entry.ReviewedContentRevision == entry.ContentRevision
			if result.RequireEntryReview && !reviewCurrent {
				result.Blockers = append(result.Blockers, CompetitionPreflightFinding{
					Code: preflightUnresolvedReview, Message: "Included Entry requires a current review",
					EntryID: entry.ID,
				})
			}
			logical, queryErr := client.Attachment.Query().
				Where(
					attachment.EventIDEQ(eventID),
					attachment.OwnerTypeEQ(attachment.OwnerTypeEntry),
					attachment.OwnerIDEQ(entry.ID),
				).
				WithVersions(func(query *ent.AttachmentVersionQuery) {
					query.Order(ent.Asc(attachmentversion.FieldID))
				}).
				All(internalContext)
			if queryErr != nil {
				return CompetitionPreflight{}, opaqueError("load Entry Attachments for Preflight", queryErr)
			}
			versions := make([]*ent.AttachmentVersion, 0)
			logicalByVersion := make(map[int]*ent.Attachment)
			for _, item := range logical {
				for _, version := range item.Edges.Versions {
					versions = append(versions, version)
					logicalByVersion[version.ID] = item
				}
			}
			if len(versions) == 1 {
				desiredFinal := !result.RequireEntryReview || reviewCurrent
				updated, updateErr := applyAttachmentAutomation(
					internalContext, versions[0], true, desiredFinal,
				)
				if updateErr != nil {
					return CompetitionPreflight{}, updateErr
				}
				versions[0] = updated
			} else if countPrimaryAttachmentVersions(versions) == 0 {
				finals := finalAttachmentVersions(versions)
				if len(finals) == 1 {
					updated, updateErr := applyAttachmentAutomation(
						internalContext, finals[0], true, true,
					)
					if updateErr != nil {
						return CompetitionPreflight{}, updateErr
					}
					for index, version := range versions {
						if version.ID == updated.ID {
							versions[index] = updated
						}
					}
				}
			}
			for _, version := range versions {
				result.Attachments = append(
					result.Attachments,
					attachmentReadinessForEntry(entry.ID, logicalByVersion[version.ID], version),
				)
			}
			if !result.FileDeliveryRequired {
				continue
			}
			primaryCount := countPrimaryAttachmentVersions(versions)
			finalPrimaryCount := countFinalPrimaryAttachmentVersions(versions)
			switch {
			case len(versions) == 0:
				result.Blockers = append(result.Blockers, CompetitionPreflightFinding{
					Code: preflightMissingDelivery, Message: "Included Entry has no Attachment Version",
					EntryID: entry.ID,
				})
			case primaryCount != 1:
				result.Blockers = append(result.Blockers, CompetitionPreflightFinding{
					Code: preflightAmbiguousPrimary, Message: "Included Entry must have exactly one Primary Attachment",
					EntryID: entry.ID,
				})
			case finalPrimaryCount != 1:
				result.Blockers = append(result.Blockers, CompetitionPreflightFinding{
					Code: preflightNonFinalPrimary, Message: "Primary Attachment must be Final",
					EntryID: entry.ID,
				})
			}
		case competitionentry.DispositionRejected:
			continue
		}
	}
	return result, nil
}

// CreateCompetitionEntry creates one Entry using the effective default disposition.
func (transaction *CommandTx) CreateCompetitionEntry(ctx context.Context, params CreateCompetitionEntryParams) (CompetitionEntry, error) {
	state, _, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	created, err := transaction.transaction.CompetitionEntry.Create().
		SetEventID(params.EventID).
		SetCompetitionSessionID(params.SessionID).
		SetName(params.Name).
		SetPublicDetails(params.PublicDetails).
		SetCrewNotes(params.CrewNotes).
		SetDisposition(competitionentry.Disposition(state.EffectiveDefaultDisposition)).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("create Competition Entry", err)
	}
	if err := transaction.SupersedeCompetitionResultsDraft(
		ctx, params.EventID, params.SessionID, params.Now,
	); err != nil {
		return CompetitionEntry{}, err
	}
	return competitionEntry(created), nil
}

// UpdateCompetitionEntry changes retained Entry content before the Deadline.
func (transaction *CommandTx) UpdateCompetitionEntry(ctx context.Context, params UpdateCompetitionEntryParams) (CompetitionEntry, error) {
	state, _, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !entry.FirstPresentedAt.IsZero() {
		return competitionEntry(entry), ErrPresentedEntryDisposition
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	updated, err := transaction.transaction.CompetitionEntry.UpdateOne(entry).
		SetName(params.Name).
		SetPublicDetails(params.PublicDetails).
		SetCrewNotes(params.CrewNotes).
		AddContentRevision(1).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("update Competition Entry", err)
	}
	if err := transaction.SupersedeCompetitionResultsDraft(
		ctx, params.EventID, params.SessionID, params.Now,
	); err != nil {
		return CompetitionEntry{}, err
	}
	return competitionEntry(updated), nil
}

// ConfigureCompetitionReadiness updates independent Competition Start policies.
func (transaction *CommandTx) ConfigureCompetitionReadiness(
	ctx context.Context,
	params ConfigureCompetitionReadinessParams,
) (CompetitionReadiness, error) {
	_, found, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionReadiness{}, err
	}
	if found.ReadinessRevision != params.ExpectedReadinessRevision {
		return competitionReadiness(found), ErrCompetitionReadinessRevision
	}
	updated, err := transaction.transaction.Session.UpdateOne(found).
		SetRequireEntryReview(params.RequireEntryReview).
		SetFileDeliveryRequired(params.FileDeliveryRequired).
		AddReadinessRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionReadiness{}, opaqueError("configure Competition readiness", err)
	}
	return competitionReadiness(updated), nil
}

// ReviewCompetitionEntry confirms one exact current Entry content revision.
func (transaction *CommandTx) ReviewCompetitionEntry(
	ctx context.Context,
	params ReviewCompetitionEntryParams,
) (CompetitionEntry, error) {
	if _, _, err := transaction.competitionConfiguration(
		ctx, params.EventID, params.SessionID,
	); err != nil {
		return CompetitionEntry{}, err
	}
	entry, err := transaction.competitionEntry(
		ctx, params.EventID, params.SessionID, params.EntryID,
	)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	internalContext := systemContext(ctx)
	versions, err := transaction.transaction.AttachmentVersion.Query().
		Where(attachmentversion.HasAttachmentWith(
			attachment.EventIDEQ(params.EventID),
			attachment.OwnerTypeEQ(attachment.OwnerTypeEntry),
			attachment.OwnerIDEQ(params.EntryID),
		)).
		All(internalContext)
	if err != nil {
		return CompetitionEntry{}, opaqueError("load reviewed Entry Attachments", err)
	}
	if len(versions) == 1 && versions[0].Primary && !versions[0].Final {
		if _, err = versions[0].Update().
			SetFinal(true).
			AddReadinessRevision(1).
			Save(internalContext); err != nil {
			return CompetitionEntry{}, opaqueError("finalize reviewed Entry Attachment", err)
		}
	}
	updated, err := transaction.transaction.CompetitionEntry.UpdateOne(entry).
		SetReviewedContentRevision(entry.ContentRevision).
		SetReviewedByAccountID(params.ReviewerAccountID).
		SetReviewedAt(params.Now).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("review Competition Entry", err)
	}
	return competitionEntry(updated), nil
}

// SetEntryAttachmentReadiness changes Final and Primary independently.
func (transaction *CommandTx) SetEntryAttachmentReadiness(
	ctx context.Context,
	params SetEntryAttachmentReadinessParams,
) (AttachmentReadiness, error) {
	if _, _, err := transaction.competitionConfiguration(
		ctx, params.EventID, params.SessionID,
	); err != nil {
		return AttachmentReadiness{}, err
	}
	if _, err := transaction.competitionEntry(
		ctx, params.EventID, params.SessionID, params.EntryID,
	); err != nil {
		return AttachmentReadiness{}, err
	}
	internalContext := systemContext(ctx)
	version, err := transaction.transaction.AttachmentVersion.Query().
		Where(attachmentversion.IDEQ(params.AttachmentVersionID)).
		WithAttachment().
		Only(internalContext)
	if ent.IsNotFound(err) {
		return AttachmentReadiness{}, ErrCompetitionEntryNotFound
	}
	if err != nil {
		return AttachmentReadiness{}, opaqueError("load Entry Attachment Version", err)
	}
	logical, err := version.Edges.AttachmentOrErr()
	if err != nil || logical.EventID != params.EventID ||
		logical.OwnerType != attachment.OwnerTypeEntry || logical.OwnerID != params.EntryID {
		return AttachmentReadiness{}, ErrCompetitionEntryNotFound
	}
	if version.ReadinessRevision != params.ExpectedRevision {
		return attachmentReadiness(version), ErrAttachmentReadinessRevision
	}
	if params.Primary {
		otherPrimaries, queryErr := transaction.transaction.AttachmentVersion.Query().
			Where(
				attachmentversion.IDNEQ(version.ID),
				attachmentversion.PrimaryEQ(true),
				attachmentversion.HasAttachmentWith(
					attachment.EventIDEQ(params.EventID),
					attachment.OwnerTypeEQ(attachment.OwnerTypeEntry),
					attachment.OwnerIDEQ(params.EntryID),
				),
			).
			All(internalContext)
		if queryErr != nil {
			return AttachmentReadiness{}, opaqueError("load other Primary Attachments", queryErr)
		}
		for _, other := range otherPrimaries {
			if _, updateErr := other.Update().
				SetPrimary(false).
				AddReadinessRevision(1).
				Save(internalContext); updateErr != nil {
				return AttachmentReadiness{}, opaqueError("clear prior Primary Attachment", updateErr)
			}
		}
	}
	updated, err := version.Update().
		SetFinal(params.Final).
		SetPrimary(params.Primary).
		AddReadinessRevision(1).
		Save(internalContext)
	if err != nil {
		return AttachmentReadiness{}, opaqueError("set Entry Attachment readiness", err)
	}
	if _, err = transaction.transaction.CompetitionEntry.UpdateOneID(params.EntryID).
		AddContentRevision(1).
		AddRevision(1).
		Save(internalContext); err != nil {
		return AttachmentReadiness{}, opaqueError("invalidate Entry review after readiness change", err)
	}
	return attachmentReadiness(updated), nil
}

// ChangeCompetitionEntryDisposition changes participation with a live override.
func (transaction *CommandTx) ChangeCompetitionEntryDisposition(
	ctx context.Context,
	params ChangeCompetitionEntryDispositionParams,
) (CompetitionEntry, error) {
	state, found, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	presentationBegan, err := found.QueryRuns().Exist(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("load Competition presentation history", err)
	}
	if presentationBegan && !params.ConfirmedLive {
		return CompetitionEntry{}, ErrLiveDispositionConfirmation
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !entry.FirstPresentedAt.IsZero() {
		return competitionEntry(entry), ErrPresentedEntryDisposition
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	update := transaction.transaction.CompetitionEntry.UpdateOne(entry).
		SetDisposition(competitionentry.Disposition(params.Disposition)).
		AddRevision(1)
	if params.Disposition == "Rejected" {
		update.SetUploadClosedAt(params.Now)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("change Competition Entry disposition", err)
	}
	if err := transaction.SupersedeCompetitionResultsDraft(
		ctx, params.EventID, params.SessionID, params.Now,
	); err != nil {
		return CompetitionEntry{}, err
	}
	return competitionEntry(updated), nil
}

func (transaction *CommandTx) competitionConfiguration(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionState, *ent.Session, error) {
	state, found, err := loadCompetitionConfiguration(
		ctx, transaction.transaction.Session, transaction.transaction.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionState{}, nil, err
	}
	return state, found, nil
}

func loadCompetitionConfiguration(
	ctx context.Context,
	sessions *ent.SessionClient,
	events *ent.EventClient,
	eventID, sessionID int,
) (CompetitionState, *ent.Session, error) {
	found, err := sessions.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return CompetitionState{}, nil, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition", err)
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if ent.IsNotFound(err) || (err == nil && version.Type != sessionpublishedversion.TypeCompetition) {
		return CompetitionState{}, nil, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition version", err)
	}
	event, err := events.Get(ctx, eventID)
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition Event", err)
	}
	state := competitionState(
		eventID, sessionID, version.SubmissionDeadline, string(version.EntryDefaultDisposition),
		string(event.EntryDefaultDisposition), nil,
	)
	readiness := competitionReadiness(found)
	state.RequireEntryReview = readiness.RequireEntryReview
	state.FileDeliveryRequired = readiness.FileDeliveryRequired
	state.ReadinessRevision = readiness.ReadinessRevision
	return state, found, nil
}

func (transaction *CommandTx) competitionEntry(
	ctx context.Context,
	eventID, sessionID, entryID int,
) (*ent.CompetitionEntry, error) {
	entry, err := transaction.transaction.CompetitionEntry.Query().Where(
		competitionentry.IDEQ(entryID),
		competitionentry.EventIDEQ(eventID),
		competitionentry.CompetitionSessionIDEQ(sessionID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrCompetitionEntryNotFound
	}
	if err != nil {
		return nil, opaqueError("load Competition Entry", err)
	}
	return entry, nil
}

func competitionState(
	eventID, sessionID int,
	deadline time.Time,
	override, eventDefault string,
	entries []*ent.CompetitionEntry,
) CompetitionState {
	effective := override
	if effective == "" {
		effective = eventDefault
	}
	state := CompetitionState{
		EventID: eventID, SessionID: sessionID, SubmissionDeadline: deadline,
		EffectiveDefaultDisposition: effective,
		Entries:                     make([]CompetitionEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		state.Entries = append(state.Entries, competitionEntry(entry))
	}
	return state
}

func competitionEntry(entry *ent.CompetitionEntry) CompetitionEntry {
	return CompetitionEntry{
		ID: entry.ID, CompetitionSessionID: entry.CompetitionSessionID,
		Name: entry.Name, PublicDetails: entry.PublicDetails, CrewNotes: entry.CrewNotes,
		Disposition: string(entry.Disposition), Revision: entry.Revision,
		ContentRevision:               entry.ContentRevision,
		ReviewCurrent:                 entry.ReviewedContentRevision == entry.ContentRevision,
		PresentationStatus:            string(entry.PresentationStatus),
		DeferredSequence:              entry.DeferredSequence,
		ResolutionRequired:            entry.ResolutionRequired,
		ResultDisposition:             string(entry.ResultDisposition),
		TechnicalFailureReason:        entry.TechnicalFailureReason,
		ResolutionCrewReason:          entry.ResolutionCrewReason,
		PublicDisqualificationMessage: entry.PublicDisqualificationMessage,
		ReleaseHold:                   entry.ReleaseHold,
		FirstPresentedAt:              entry.FirstPresentedAt,
		CreatedAt:                     entry.CreatedAt,
	}
}

func competitionReadiness(found *ent.Session) CompetitionReadiness {
	required := true
	if found.FileDeliveryRequired != nil {
		required = *found.FileDeliveryRequired
	}
	return CompetitionReadiness{
		RequireEntryReview:   found.RequireEntryReview,
		FileDeliveryRequired: required,
		ReadinessRevision:    found.ReadinessRevision,
	}
}

func attachmentReadiness(version *ent.AttachmentVersion) AttachmentReadiness {
	return AttachmentReadiness{
		AttachmentVersionID: version.ID,
		ReadinessRevision:   version.ReadinessRevision,
		Final:               version.Final,
		Primary:             version.Primary,
	}
}

func attachmentReadinessForEntry(
	entryID int,
	logical *ent.Attachment,
	version *ent.AttachmentVersion,
) AttachmentReadiness {
	readiness := attachmentReadiness(version)
	readiness.EntryID = entryID
	readiness.AttachmentVersion = version.Version
	readiness.LogicalName = logical.Name
	readiness.OriginalFilename = version.OriginalFilename
	return readiness
}

func applyAttachmentAutomation(
	ctx context.Context,
	version *ent.AttachmentVersion,
	primary, final bool,
) (*ent.AttachmentVersion, error) {
	if version.Primary == primary && version.Final == final {
		return version, nil
	}
	updated, err := version.Update().
		SetPrimary(primary).
		SetFinal(final).
		AddReadinessRevision(1).
		Save(ctx)
	if err != nil {
		return nil, opaqueError("apply Attachment Preflight automation", err)
	}
	return updated, nil
}

func countPrimaryAttachmentVersions(versions []*ent.AttachmentVersion) int {
	count := 0
	for _, version := range versions {
		if version.Primary {
			count++
		}
	}
	return count
}

func countFinalPrimaryAttachmentVersions(versions []*ent.AttachmentVersion) int {
	count := 0
	for _, version := range versions {
		if version.Primary && version.Final {
			count++
		}
	}
	return count
}

func finalAttachmentVersions(versions []*ent.AttachmentVersion) []*ent.AttachmentVersion {
	finals := make([]*ent.AttachmentVersion, 0, len(versions))
	for _, version := range versions {
		if version.Final {
			finals = append(finals, version)
		}
	}
	return finals
}
