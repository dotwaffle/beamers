package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/vote"
	"github.com/dotwaffle/beamers/ent/votingeligibility"
	"github.com/dotwaffle/beamers/ent/votingtally"
	"github.com/dotwaffle/beamers/internal/votingvalue"
)

var (
	// ErrVotingCompetitionNotFound hides unknown and non-Competition Sessions.
	ErrVotingCompetitionNotFound = errors.New("voting Competition not found")
	// ErrVotingWindowState means the requested window transition is unavailable.
	ErrVotingWindowState = errors.New("voting window state does not permit the command")
	// ErrVotingRevision means a command used a stale Ballot snapshot.
	ErrVotingRevision = errors.New("voting revision conflict")
	// ErrVotingIneligible means the Account lacks Event Voting Eligibility.
	ErrVotingIneligible = errors.New("account is not voting eligible")
	// ErrVoteUnavailable hides unknown, unpresented, and excluded Entries.
	ErrVoteUnavailable = errors.New("competition Entry is not votable")
)

// VotingWindow is the Competition's effective or frozen voting configuration.
type VotingWindow struct {
	EventID, SessionID int
	EventName, Title   string
	Method             string
	SelfVotePolicy     string
	State              string
	Revision           int
	OpenedAt, ClosedAt time.Time
}

// BallotEntry is one presented Entry plus only the requesting Account's score.
type BallotEntry struct {
	ID, Value int
	Name      string
	CanVote   bool
}

// Ballot is one complete private Account snapshot.
type Ballot struct {
	Window  VotingWindow
	Entries []BallotEntry
}

// VotingParticipation is the value-free Crew projection.
type VotingParticipation struct {
	Eligible, Participating, Complete, Votable int
}

// VotingTally is one immutable aggregate snapshot without Account-level Votes.
type VotingTally struct {
	ID, EventID, SessionID int
	Method, SelfVotePolicy string
	Participating          int
	Entries                []votingvalue.Entry
	CreatedByAccountID     int
	CreatedAt              time.Time
}

// ConfigureVotingParams changes only pre-open Competition policy.
type ConfigureVotingParams struct {
	EventID, SessionID, ExpectedRevision int
	MethodOverride, SelfVoteOverride     string
}

// VotingWindowParams opens or closes one exact Competition snapshot.
type VotingWindowParams struct {
	EventID, SessionID, ExpectedRevision int
	CreatedByAccountID                   int
	Now                                  time.Time
}

// CastVoteParams replaces one private score.
type CastVoteParams struct {
	EventID, SessionID, AccountID, EntryID int
	Value, ExpectedRevision                int
	Now                                    time.Time
}

func votingCompetition(
	ctx context.Context,
	query *ent.SessionQuery,
	eventID, sessionID int,
) (*ent.Session, *ent.Event, *ent.SessionPublishedVersion, error) {
	found, err := query.
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		WithEvent().
		WithPublishedVersions(func(versions *ent.SessionPublishedVersionQuery) {
			versions.Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).Limit(1)
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, nil, ErrVotingCompetitionNotFound
	}
	if err != nil {
		return nil, nil, nil, opaqueError("load voting Competition", err)
	}
	foundEvent, err := found.Edges.EventOrErr()
	if err != nil {
		return nil, nil, nil, opaqueError("load voting Event", err)
	}
	versions := found.Edges.PublishedVersions
	if len(versions) != 1 || versions[0].Type != sessionpublishedversion.TypeCompetition {
		return nil, nil, nil, ErrVotingCompetitionNotFound
	}
	return found, foundEvent, versions[0], nil
}

func votingWindow(found *ent.Session, foundEvent *ent.Event, title string) VotingWindow {
	method := string(foundEvent.VotingMethod)
	if found.VotingMethodOverride != nil {
		method = string(*found.VotingMethodOverride)
	}
	policy := string(foundEvent.SelfVotePolicy)
	if found.SelfVotePolicyOverride != nil {
		policy = string(*found.SelfVotePolicyOverride)
	}
	if found.FrozenVotingMethod != nil {
		method = string(*found.FrozenVotingMethod)
	}
	if found.FrozenSelfVotePolicy != nil {
		policy = string(*found.FrozenSelfVotePolicy)
	}
	return VotingWindow{
		EventID: found.EventID, SessionID: found.ID,
		EventName: foundEvent.Name, Title: title,
		Method: method, SelfVotePolicy: policy,
		State: string(found.VotingWindowState), Revision: found.VotingRevision,
		OpenedAt: found.VotingOpenedAt, ClosedAt: found.VotingClosedAt,
	}
}

// LoadVotingWindow returns value-free Competition configuration to Crew.
func (installation *SQLite) LoadVotingWindow(
	ctx context.Context,
	eventID, sessionID int,
) (VotingWindow, error) {
	found, foundEvent, version, err := votingCompetition(
		ctx, installation.client.Session.Query(), eventID, sessionID,
	)
	if err != nil {
		return VotingWindow{}, err
	}
	return votingWindow(found, foundEvent, version.Title), nil
}

// ConfigureVoting changes Competition overrides before the first open.
func (transaction *CommandTx) ConfigureVoting(
	ctx context.Context,
	params ConfigureVotingParams,
) (VotingWindow, error) {
	found, foundEvent, version, err := votingCompetition(
		ctx, transaction.transaction.Session.Query(), params.EventID, params.SessionID,
	)
	if err != nil {
		return VotingWindow{}, err
	}
	if found.VotingWindowState != session.VotingWindowStateUnopened {
		return VotingWindow{}, ErrVotingWindowState
	}
	if found.VotingRevision != params.ExpectedRevision {
		return VotingWindow{}, ErrVotingRevision
	}
	update := found.Update().AddVotingRevision(1)
	if params.MethodOverride == "" {
		update.ClearVotingMethodOverride()
	} else {
		update.SetVotingMethodOverride(session.VotingMethodOverride(params.MethodOverride))
	}
	if params.SelfVoteOverride == "" {
		update.ClearSelfVotePolicyOverride()
	} else {
		update.SetSelfVotePolicyOverride(session.SelfVotePolicyOverride(params.SelfVoteOverride))
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return VotingWindow{}, opaqueError("configure Competition voting", err)
	}
	return votingWindow(updated, foundEvent, version.Title), nil
}

// OpenVotingWindow freezes the effective method and Self-Vote Policy.
func (transaction *CommandTx) OpenVotingWindow(
	ctx context.Context,
	params VotingWindowParams,
) (VotingWindow, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return VotingWindow{}, err
	}
	found, foundEvent, version, err := votingCompetition(
		ctx, transaction.transaction.Session.Query(), params.EventID, params.SessionID,
	)
	if err != nil {
		return VotingWindow{}, err
	}
	if found.Lifecycle != session.LifecycleLive ||
		found.VotingWindowState != session.VotingWindowStateUnopened {
		return VotingWindow{}, ErrVotingWindowState
	}
	if found.VotingRevision != params.ExpectedRevision {
		return VotingWindow{}, ErrVotingRevision
	}
	effective := votingWindow(found, foundEvent, version.Title)
	updated, err := found.Update().
		SetVotingWindowState(session.VotingWindowStateOpen).
		SetFrozenVotingMethod(session.FrozenVotingMethod(effective.Method)).
		SetFrozenSelfVotePolicy(session.FrozenSelfVotePolicy(effective.SelfVotePolicy)).
		SetVotingOpenedAt(params.Now).
		AddVotingRevision(1).
		Save(ctx)
	if err != nil {
		return VotingWindow{}, opaqueError("open Competition Voting Window", err)
	}
	return votingWindow(updated, foundEvent, version.Title), nil
}

// CloseVotingWindow freezes Ballot mutation, tallies once, and seeds Results review.
func (transaction *CommandTx) CloseVotingWindow(
	ctx context.Context,
	params VotingWindowParams,
) (VotingWindow, error) {
	found, foundEvent, version, err := votingCompetition(
		ctx, transaction.transaction.Session.Query(), params.EventID, params.SessionID,
	)
	if err != nil {
		return VotingWindow{}, err
	}
	if found.VotingWindowState != session.VotingWindowStateOpen {
		return VotingWindow{}, ErrVotingWindowState
	}
	if found.VotingRevision != params.ExpectedRevision {
		return VotingWindow{}, ErrVotingRevision
	}
	updated, err := found.Update().
		SetVotingWindowState(session.VotingWindowStateClosed).
		SetVotingClosedAt(params.Now).
		AddVotingRevision(1).
		Save(ctx)
	if err != nil {
		return VotingWindow{}, opaqueError("close Competition Voting Window", err)
	}
	if tallyErr := transaction.createVotingTally(
		ctx,
		updated,
		votingWindow(updated, foundEvent, version.Title),
		params.CreatedByAccountID,
		params.Now,
	); tallyErr != nil {
		return VotingWindow{}, tallyErr
	}
	return votingWindow(updated, foundEvent, version.Title), nil
}

func (transaction *CommandTx) createVotingTally(
	ctx context.Context,
	found *ent.Session,
	window VotingWindow,
	createdByAccountID int,
	now time.Time,
) error {
	if createdByAccountID <= 0 {
		return errors.New("voting Tally creator is required")
	}
	client := transaction.transaction.Client()
	entries, err := client.CompetitionEntry.Query().Where(
		competitionentry.EventIDEQ(found.EventID),
		competitionentry.CompetitionSessionIDEQ(found.ID),
		competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		competitionentry.FirstPresentedAtNotNil(),
	).Order(ent.Asc(competitionentry.FieldFirstPresentedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return opaqueError("load Voting Tally Entries", err)
	}
	votes, err := client.Vote.Query().Where(
		vote.EventIDEQ(found.EventID),
		vote.CompetitionSessionIDEQ(found.ID),
	).All(ctx)
	if err != nil {
		return opaqueError("load Voting Tally Votes", err)
	}
	ballotValues := make(map[int]map[int]int)
	for _, stored := range votes {
		if ballotValues[stored.AccountID] == nil {
			ballotValues[stored.AccountID] = make(map[int]int)
		}
		ballotValues[stored.AccountID][stored.EntryID] = stored.Value
	}
	ballots := make([]votingvalue.Ballot, 0, len(ballotValues))
	for accountID, scores := range ballotValues {
		ballots = append(ballots, votingvalue.Ballot{
			AccountID: accountID, Scores: scores,
		})
	}
	eligibleEntries := make([]votingvalue.EligibleEntry, 0, len(entries))
	for _, entry := range entries {
		submitterAccountID := 0
		if entry.SubmitterAccountID != nil {
			submitterAccountID = *entry.SubmitterAccountID
		}
		eligibleEntries = append(eligibleEntries, votingvalue.EligibleEntry{
			EntryID: entry.ID, SubmitterAccountID: submitterAccountID,
		})
	}
	aggregates := votingvalue.Aggregate(
		eligibleEntries,
		ballots,
		window.SelfVotePolicy,
	)
	created, err := client.VotingTally.Create().
		SetEventID(found.EventID).
		SetCompetitionSessionID(found.ID).
		SetMethod(votingtally.Method(window.Method)).
		SetSelfVotePolicy(votingtally.SelfVotePolicy(window.SelfVotePolicy)).
		SetParticipating(len(ballots)).
		SetEntries(aggregates).
		SetCreatedByAccountID(createdByAccountID).
		SetCreatedAt(now.UTC()).
		Save(ctx)
	if err != nil {
		return opaqueError("create Voting Tally", err)
	}
	current, err := transaction.LoadCompetitionResultsDraft(
		ctx,
		found.EventID,
		found.ID,
	)
	if err != nil {
		return err
	}
	eligibleEntryIDs := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.ResultDisposition == competitionentry.ResultDispositionEligible {
			eligibleEntryIDs = append(eligibleEntryIDs, entry.ID)
		}
	}
	placements := votingvalue.Placements(aggregates, eligibleEntryIDs)
	standings := make([]CompetitionResultStandingInput, 0, len(placements))
	for _, placement := range placements {
		standings = append(standings, CompetitionResultStandingInput{
			EntryID: placement.EntryID, Standing: "Placed",
			Placement: placement.Placement, DisplayOrder: placement.DisplayOrder,
		})
	}
	_, err = transaction.SaveCompetitionResultsDraft(
		ctx,
		SaveCompetitionResultsDraftParams{
			EventID: found.EventID, SessionID: found.ID,
			ExpectedRevision: current.Revision,
			Disposition:      "Pending", ScoreType: "None",
			CreatedByAccountID: createdByAccountID, Now: now,
			VotingTallyID: created.ID, Standings: standings,
			Awards: append([]CompetitionAwardInput(nil), current.Awards...),
		},
	)
	return err
}

// LoadVotingTally returns aggregate evidence without Account-level Votes.
func (installation *SQLite) LoadVotingTally(
	ctx context.Context,
	eventID, sessionID int,
) (VotingTally, error) {
	found, err := installation.client.VotingTally.Query().Where(
		votingtally.EventIDEQ(eventID),
		votingtally.CompetitionSessionIDEQ(sessionID),
	).Only(ctx)
	if err != nil {
		return VotingTally{}, opaqueError("load Voting Tally", err)
	}
	return votingTally(found), nil
}

// LoadVotingTally returns aggregate evidence inside a command transaction.
func (transaction *CommandTx) LoadVotingTally(
	ctx context.Context,
	eventID, sessionID int,
) (VotingTally, error) {
	found, err := transaction.transaction.VotingTally.Query().Where(
		votingtally.EventIDEQ(eventID),
		votingtally.CompetitionSessionIDEQ(sessionID),
	).Only(ctx)
	if err != nil {
		return VotingTally{}, opaqueError("load Voting Tally", err)
	}
	return votingTally(found), nil
}

func votingTally(found *ent.VotingTally) VotingTally {
	return VotingTally{
		ID: found.ID, EventID: found.EventID, SessionID: found.CompetitionSessionID,
		Method: string(found.Method), SelfVotePolicy: string(found.SelfVotePolicy),
		Participating:      found.Participating,
		Entries:            append([]votingvalue.Entry(nil), found.Entries...),
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

func votingEligible(ctx context.Context, client *ent.Client, eventID, accountID int) (bool, error) {
	eligible, err := client.VotingEligibility.Query().Where(
		votingeligibility.EventIDEQ(eventID),
		votingeligibility.AccountIDEQ(accountID),
	).Exist(ctx)
	if err != nil {
		return false, opaqueError("load Voting Eligibility", err)
	}
	return eligible, nil
}

// CastVote replaces one private score while the Ballot snapshot remains current.
func (transaction *CommandTx) CastVote(
	ctx context.Context,
	params CastVoteParams,
) (Ballot, error) {
	found, _, _, err := votingCompetition(
		ctx, transaction.transaction.Session.Query(), params.EventID, params.SessionID,
	)
	if err != nil {
		return Ballot{}, err
	}
	if found.VotingWindowState != session.VotingWindowStateOpen {
		return Ballot{}, ErrVotingWindowState
	}
	if found.VotingRevision != params.ExpectedRevision {
		return Ballot{}, ErrVotingRevision
	}
	eligible, err := transaction.transaction.VotingEligibility.Query().Where(
		votingeligibility.EventIDEQ(params.EventID),
		votingeligibility.AccountIDEQ(params.AccountID),
	).Exist(ctx)
	if err != nil {
		return Ballot{}, opaqueError("load Voting Eligibility", err)
	}
	if !eligible {
		return Ballot{}, ErrVotingIneligible
	}
	entry, err := transaction.transaction.CompetitionEntry.Query().Where(
		competitionentry.IDEQ(params.EntryID),
		competitionentry.EventIDEQ(params.EventID),
		competitionentry.CompetitionSessionIDEQ(params.SessionID),
		competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		competitionentry.FirstPresentedAtNotNil(),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return Ballot{}, ErrVoteUnavailable
	}
	if err != nil {
		return Ballot{}, opaqueError("load votable Competition Entry", err)
	}
	if found.FrozenSelfVotePolicy != nil &&
		*found.FrozenSelfVotePolicy == session.FrozenSelfVotePolicyNeutral &&
		entry.SubmitterAccountID != nil && *entry.SubmitterAccountID == params.AccountID {
		return Ballot{}, ErrVoteUnavailable
	}
	stored, err := transaction.transaction.Vote.Query().Where(
		vote.CompetitionSessionIDEQ(params.SessionID),
		vote.AccountIDEQ(params.AccountID),
		vote.EntryIDEQ(params.EntryID),
	).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, err = transaction.transaction.Vote.Create().
			SetEventID(params.EventID).
			SetCompetitionSessionID(params.SessionID).
			SetAccountID(params.AccountID).
			SetEntryID(params.EntryID).
			SetValue(params.Value).
			SetCreatedAt(params.Now).
			SetUpdatedAt(params.Now).
			Save(ctx)
	case err == nil:
		_, err = stored.Update().SetValue(params.Value).SetUpdatedAt(params.Now).Save(ctx)
	}
	if err != nil {
		return Ballot{}, opaqueError("save Competition Vote", err)
	}
	return transaction.loadBallot(ctx, params.EventID, params.SessionID, params.AccountID)
}

func ballotEntries(
	ctx context.Context,
	client *ent.Client,
	found *ent.Session,
	accountID int,
) ([]BallotEntry, error) {
	entries, err := client.CompetitionEntry.Query().Where(
		competitionentry.EventIDEQ(found.EventID),
		competitionentry.CompetitionSessionIDEQ(found.ID),
		competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		competitionentry.FirstPresentedAtNotNil(),
	).Order(ent.Asc(competitionentry.FieldFirstPresentedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("load votable Competition Entries", err)
	}
	votes, err := client.Vote.Query().Where(
		vote.CompetitionSessionIDEQ(found.ID),
		vote.AccountIDEQ(accountID),
	).All(ctx)
	if err != nil {
		return nil, opaqueError("load Account Ballot", err)
	}
	values := make(map[int]int, len(votes))
	for _, stored := range votes {
		values[stored.EntryID] = stored.Value
	}
	result := make([]BallotEntry, 0, len(entries))
	for _, entry := range entries {
		canVote := found.FrozenSelfVotePolicy == nil ||
			*found.FrozenSelfVotePolicy != session.FrozenSelfVotePolicyNeutral ||
			entry.SubmitterAccountID == nil || *entry.SubmitterAccountID != accountID
		result = append(result, BallotEntry{
			ID: entry.ID, Name: entry.Name, Value: values[entry.ID], CanVote: canVote,
		})
	}
	return result, nil
}

func (transaction *CommandTx) loadBallot(
	ctx context.Context,
	eventID, sessionID, accountID int,
) (Ballot, error) {
	found, foundEvent, version, err := votingCompetition(
		ctx, transaction.transaction.Session.Query(), eventID, sessionID,
	)
	if err != nil {
		return Ballot{}, err
	}
	entries, err := ballotEntries(ctx, transaction.transaction.Client(), found, accountID)
	if err != nil {
		return Ballot{}, err
	}
	return Ballot{Window: votingWindow(found, foundEvent, version.Title), Entries: entries}, nil
}

// LoadBallot returns one complete private Account snapshot.
func (installation *SQLite) LoadBallot(
	ctx context.Context,
	eventID, sessionID, accountID int,
) (Ballot, error) {
	found, foundEvent, version, err := votingCompetition(
		ctx, installation.client.Session.Query(), eventID, sessionID,
	)
	if err != nil {
		return Ballot{}, err
	}
	if found.VotingWindowState != session.VotingWindowStateOpen &&
		found.VotingWindowState != session.VotingWindowStateClosed {
		return Ballot{}, ErrVotingWindowState
	}
	eligible, err := votingEligible(ctx, installation.client, eventID, accountID)
	if err != nil {
		return Ballot{}, err
	}
	if !eligible {
		return Ballot{}, ErrVotingIneligible
	}
	entries, err := ballotEntries(ctx, installation.client, found, accountID)
	if err != nil {
		return Ballot{}, err
	}
	return Ballot{Window: votingWindow(found, foundEvent, version.Title), Entries: entries}, nil
}

// ListOpenBallots returns only eligible, currently open Competition links.
func (installation *SQLite) ListOpenBallots(
	ctx context.Context,
	accountID int,
) ([]VotingWindow, error) {
	eligibilities, err := installation.client.VotingEligibility.Query().
		Where(votingeligibility.AccountIDEQ(accountID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("load Account Voting Eligibilities", err)
	}
	eventIDs := make([]int, 0, len(eligibilities))
	for _, eligibility := range eligibilities {
		eventIDs = append(eventIDs, eligibility.EventID)
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	sessions, err := installation.client.Session.Query().Where(
		session.EventIDIn(eventIDs...),
		session.VotingWindowStateEQ(session.VotingWindowStateOpen),
	).WithEvent().WithPublishedVersions(func(versions *ent.SessionPublishedVersionQuery) {
		versions.Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).Limit(1)
	}).Order(ent.Asc(session.FieldID)).All(ctx)
	if err != nil {
		return nil, opaqueError("list open Voting Windows", err)
	}
	result := make([]VotingWindow, 0, len(sessions))
	for _, found := range sessions {
		foundEvent, edgeErr := found.Edges.EventOrErr()
		versions := found.Edges.PublishedVersions
		if edgeErr != nil || len(versions) != 1 ||
			versions[0].Type != sessionpublishedversion.TypeCompetition {
			continue
		}
		result = append(result, votingWindow(found, foundEvent, versions[0].Title))
	}
	return result, nil
}

// LoadVotingParticipation returns aggregate counts without Vote values.
func (installation *SQLite) LoadVotingParticipation(
	ctx context.Context,
	eventID, sessionID int,
) (VotingParticipation, error) {
	found, _, _, err := votingCompetition(
		ctx, installation.client.Session.Query(), eventID, sessionID,
	)
	if err != nil {
		return VotingParticipation{}, err
	}
	eligible, err := installation.client.VotingEligibility.Query().
		Where(votingeligibility.EventIDEQ(eventID)).
		Count(ctx)
	if err != nil {
		return VotingParticipation{}, opaqueError("count Voting Eligibility", err)
	}
	entries, err := installation.client.CompetitionEntry.Query().Where(
		competitionentry.EventIDEQ(eventID),
		competitionentry.CompetitionSessionIDEQ(sessionID),
		competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		competitionentry.FirstPresentedAtNotNil(),
	).All(ctx)
	if err != nil {
		return VotingParticipation{}, opaqueError("count votable Entries", err)
	}
	votes, err := installation.client.Vote.Query().
		Where(vote.CompetitionSessionIDEQ(sessionID)).
		All(ctx)
	if err != nil {
		return VotingParticipation{}, opaqueError("count Ballot participation", err)
	}
	byAccount := make(map[int]map[int]struct{})
	for _, stored := range votes {
		if byAccount[stored.AccountID] == nil {
			byAccount[stored.AccountID] = make(map[int]struct{})
		}
		byAccount[stored.AccountID][stored.EntryID] = struct{}{}
	}
	complete := 0
	for accountID, scored := range byAccount {
		required := len(entries)
		if found.FrozenSelfVotePolicy != nil &&
			*found.FrozenSelfVotePolicy == session.FrozenSelfVotePolicyNeutral {
			for _, entry := range entries {
				if entry.SubmitterAccountID != nil && *entry.SubmitterAccountID == accountID {
					required--
				}
			}
		}
		if len(scored) == required {
			complete++
		}
	}
	return VotingParticipation{
		Eligible: eligible, Participating: len(byAccount), Complete: complete, Votable: len(entries),
	}, nil
}
