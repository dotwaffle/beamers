// Package displays enrolls and routes persistent Display identities.
package displays

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/store"
)

const (
	defaultEnrollmentTTL = 10 * time.Minute
	displayTokenBytes    = 32
	enrollmentCodeBytes  = 5
	credentialLifetime   = 10 * 365 * 24 * time.Hour
	protocolVersion      = "beamers.display.v1"
	maxClockHealthMillis = int64(24 * 60 * 60 * 1000)
)

var (
	// ErrAdministratorRequired means Display Enrollment lacked installation authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrCrewRequired means the current Active Event is not visible to the Account.
	ErrCrewRequired = errors.New("crew authority required")
	// ErrEnrollmentUnavailable means a claim code is unknown, expired, or already used.
	ErrEnrollmentUnavailable = errors.New("Display Enrollment is unavailable")
	// ErrInvalidDisplay means Display Enrollment input is invalid.
	ErrInvalidDisplay = errors.New("invalid Display details")
	// ErrDisplayAuthentication means a credential cannot authenticate a Display.
	ErrDisplayAuthentication = errors.New("Display authentication required")
	// ErrInvalidAcknowledgment means applied Display state is malformed.
	ErrInvalidAcknowledgment = errors.New("invalid Display acknowledgment")
	// ErrAcknowledgmentRegression means applied Display state moved backward.
	ErrAcknowledgmentRegression = errors.New("Display acknowledgment regressed")
	// ErrAcknowledgmentConflict means one stream position names different applied state.
	ErrAcknowledgmentConflict = errors.New("Display acknowledgment conflicts")
	// ErrDisplayNotFound means Assignment targeted no enrolled Display.
	ErrDisplayNotFound = errors.New("Display not found")
	// ErrAssignmentReference means Event, Location, or View routing is invalid.
	ErrAssignmentReference = errors.New("invalid Display Assignment reference")
	// ErrCommandConflict means a Command ID was reused for different Display work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Config contains explicit Display Enrollment dependencies.
type Config struct {
	Now           func() time.Time
	Random        io.Reader
	EnrollmentTTL time.Duration
}

// DefaultConfig returns production Display Enrollment dependencies.
func DefaultConfig() Config {
	return Config{Now: time.Now, Random: rand.Reader, EnrollmentTTL: defaultEnrollmentTTL}
}

// Service owns Display Enrollment credentials and Assignment commands.
type Service struct {
	storage       *store.SQLite
	now           func() time.Time
	random        io.Reader
	enrollmentTTL time.Duration
}

// Enrollment is browser-held material for one pending Display claim.
type Enrollment struct {
	Code              string
	Credential        string
	ExpiresAt         time.Time
	CredentialExpires time.Time
}

// Display is one enrolled screen identity.
type Display struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// Snapshot is the current output routing projection for one Display.
type Snapshot struct {
	ProtocolVersion      string
	AssetVersion         string
	ServerTime           time.Time
	Display              Display
	ActiveEventID        int
	EventName            string
	EventTimezone        string
	ActivationGeneration int
	PublishedRevision    int
	LocationID           int
	LocationName         string
	ViewKey              string
	Standby              bool
	Sessions             []Session
}

// AcknowledgmentInput reports the exact state one Display has applied.
type AcknowledgmentInput struct {
	ProtocolVersion      string
	AssetVersion         string
	StreamID             string
	StreamPosition       uint64
	ActiveEventID        int64
	ActivationGeneration int64
	PublishedRevision    int64
	Standby              bool
	ClockOffset          int64
	ClockUncertainty     uint64
	RendererUnstable     bool
}

// Acknowledgment is the latest durably recorded state one Display applied.
type Acknowledgment struct {
	DisplayID            int
	ProtocolVersion      string
	AssetVersion         string
	StreamID             string
	StreamPosition       uint64
	ActiveEventID        int
	ActivationGeneration int
	PublishedRevision    int
	AppliedAt            time.Time
	Standby              bool
	ClockOffset          int64
	ClockUncertainty     uint64
	RendererUnstable     bool
}

// Session is one Display-safe committed Schedule item.
type Session struct {
	ID                   int
	Title                string
	Speaker              string
	PublicDetails        string
	ForecastStart        time.Time
	ForecastEnd          time.Time
	Lifecycle            string
	LiveStateRevision    int
	ActualStart          time.Time
	ActualEnd            *time.Time
	LocationIDs          []int
	LaneIDs              []int
	TrackIDs             []int
	Unavailable          bool
	AvailabilityMessage  string
	DisplayForecastStart string
	DisplayForecastEnd   string
	orderAt              time.Time
}

// ClaimInput confirms one Display Enrollment code.
type ClaimInput struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	CommandID string `json:"command_id"`
}

// AssignInput binds one Display to one Event Location and normal View.
type AssignInput struct {
	DisplayID  int    `json:"display_id"`
	EventID    int    `json:"event_id"`
	LocationID int    `json:"location_id"`
	ViewKey    string `json:"view_key"`
	CommandID  string `json:"command_id"`
}

// Assignment is one committed Event-specific Display route.
type Assignment struct {
	DisplayID  int    `json:"display_id"`
	EventID    int    `json:"event_id"`
	LocationID int    `json:"location_id"`
	ViewKey    string `json:"view_key"`
}

// Status is one crew-visible current Display routing summary.
type Status struct {
	ID                          int        `json:"id"`
	Name                        string     `json:"name"`
	ActiveEventID               int        `json:"active_event_id"`
	Standby                     bool       `json:"standby"`
	EventName                   string     `json:"event_name,omitempty"`
	LocationName                string     `json:"location_name,omitempty"`
	ViewKey                     string     `json:"view_key,omitempty"`
	DeliveryState               string     `json:"delivery_state"`
	AppliedActiveEventID        int        `json:"applied_active_event_id"`
	AppliedActivationGeneration int        `json:"applied_activation_generation"`
	AppliedPublishedRevision    int        `json:"applied_published_revision"`
	AppliedStandby              bool       `json:"applied_standby"`
	AppliedAt                   *time.Time `json:"applied_at,omitempty"`
	ClockOffset                 int64      `json:"clock_offset_milliseconds"`
	ClockUncertainty            int64      `json:"clock_uncertainty_milliseconds"`
}

// New creates a Display service with explicit storage, clock, and randomness.
func New(storage *store.SQLite, config Config) (*Service, error) {
	if storage == nil {
		return nil, errors.New("Display storage is required")
	}
	if config.Now == nil {
		return nil, errors.New("Display clock is required")
	}
	if config.Random == nil {
		return nil, errors.New("Display randomness is required")
	}
	if config.EnrollmentTTL <= 0 {
		return nil, errors.New("Display Enrollment lifetime must be positive")
	}
	return &Service{
		storage: storage, now: config.Now, random: config.Random, enrollmentTTL: config.EnrollmentTTL,
	}, nil
}

// Current authenticates a Display and returns its complete authorized Active Event Snapshot.
func (service *Service) Current(ctx context.Context, credential string) (Snapshot, error) {
	if !validDisplayToken(credential) {
		return Snapshot{}, ErrDisplayAuthentication
	}
	found, err := service.storage.LoadDisplaySnapshot(ctx, digest(credential))
	if errors.Is(err, store.ErrDisplayCredential) {
		return Snapshot{}, ErrDisplayAuthentication
	}
	if err != nil {
		return Snapshot{}, err
	}
	now := service.now().UTC()
	result := Snapshot{
		ProtocolVersion: protocolVersion, AssetVersion: AssetVersion(), ServerTime: now,
		Display: display(found.Display), ActiveEventID: found.ActiveEventID,
		EventName: found.EventName, EventTimezone: found.EventTimezone,
		ActivationGeneration: found.ActivationGeneration,
		PublishedRevision:    found.PublishedRevision, LocationID: found.LocationID,
		LocationName: found.LocationName, ViewKey: found.ViewKey, Standby: found.Standby,
	}
	zone := time.UTC
	if found.EventTimezone != "" {
		zone, err = time.LoadLocation(found.EventTimezone)
		if err != nil {
			return Snapshot{}, errors.Join(errors.New("load Display Event timezone"), err)
		}
	}
	for _, item := range found.Sessions {
		if selected, ok := displaySession(found, item, now, zone); ok {
			result.Sessions = append(result.Sessions, selected)
		}
	}
	sort.SliceStable(result.Sessions, func(first, second int) bool {
		return result.Sessions[first].orderAt.Before(result.Sessions[second].orderAt)
	})
	return result, nil
}

// Acknowledge independently records state already applied by one Display.
func (service *Service) Acknowledge(
	ctx context.Context,
	credential string,
	input AcknowledgmentInput,
) (Acknowledgment, error) {
	if !validDisplayToken(credential) {
		return Acknowledgment{}, ErrDisplayAuthentication
	}
	if input.ProtocolVersion != protocolVersion ||
		input.AssetVersion != AssetVersion() ||
		input.StreamID == "" ||
		len(input.StreamID) > 200 ||
		input.StreamPosition > math.MaxInt64 ||
		input.ActiveEventID < 0 || input.ActiveEventID > math.MaxInt ||
		input.ActivationGeneration < 0 || input.ActivationGeneration > math.MaxInt ||
		input.PublishedRevision < 0 || input.PublishedRevision > math.MaxInt ||
		input.ClockOffset < -maxClockHealthMillis ||
		input.ClockOffset > maxClockHealthMillis ||
		input.ClockUncertainty > uint64(maxClockHealthMillis) {
		return Acknowledgment{}, ErrInvalidAcknowledgment
	}
	stored, err := service.storage.RecordDisplayAcknowledgment(ctx, digest(credential), store.DisplayAcknowledgment{
		ProtocolVersion: input.ProtocolVersion, AssetVersion: input.AssetVersion,
		StreamID:       input.StreamID,
		StreamPosition: int64(input.StreamPosition), ActiveEventID: int(input.ActiveEventID),
		ActivationGeneration: int(input.ActivationGeneration),
		PublishedRevision:    int(input.PublishedRevision),
		AppliedAt:            service.now().UTC(), AppliedStandby: input.Standby,
		ClockOffsetMilliseconds:      input.ClockOffset,
		ClockUncertaintyMilliseconds: int64(input.ClockUncertainty),
		RendererUnstable:             input.RendererUnstable,
	})
	switch {
	case errors.Is(err, store.ErrDisplayCredential):
		return Acknowledgment{}, ErrDisplayAuthentication
	case errors.Is(err, store.ErrDisplayAcknowledgmentRegression):
		return Acknowledgment{}, ErrAcknowledgmentRegression
	case errors.Is(err, store.ErrDisplayAcknowledgmentConflict):
		return Acknowledgment{}, ErrAcknowledgmentConflict
	case err != nil:
		return Acknowledgment{}, err
	}
	if stored.StreamPosition < 0 || stored.ClockUncertaintyMilliseconds < 0 {
		return Acknowledgment{}, errors.New("stored Display acknowledgment values are invalid")
	}
	return Acknowledgment{
		DisplayID: stored.DisplayID, ProtocolVersion: stored.ProtocolVersion,
		AssetVersion: stored.AssetVersion,
		StreamID:     stored.StreamID, StreamPosition: uint64(stored.StreamPosition),
		ActiveEventID:        stored.ActiveEventID,
		ActivationGeneration: stored.ActivationGeneration,
		PublishedRevision:    stored.PublishedRevision, AppliedAt: stored.AppliedAt,
		Standby: stored.AppliedStandby, ClockOffset: stored.ClockOffsetMilliseconds,
		ClockUncertainty: uint64(stored.ClockUncertaintyMilliseconds),
		RendererUnstable: stored.RendererUnstable,
	}, nil
}

// List returns current Display routing summaries to the Active Event's Crew Members.
func (service *Service) List(
	ctx context.Context,
	actor auth.Account,
	cursor displaystream.Cursor,
) ([]Status, error) {
	activeEventID, stored, err := service.storage.ListDisplayStatuses(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	if _, ok := actor.EventRoles[activeEventID]; !actor.Administrator && (activeEventID <= 0 || !ok) {
		return nil, ErrCrewRequired
	}
	result := make([]Status, 0, len(stored))
	for _, item := range stored {
		result = append(result, status(item, cursor, service.now().UTC()))
	}
	return result, nil
}

// Assign commits one Event-specific Display route.
func (service *Service) Assign(
	ctx context.Context,
	actor auth.Account,
	input AssignInput,
) (Assignment, error) {
	input.ViewKey = strings.TrimSpace(input.ViewKey)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Assignment{}, ErrInvalidDisplay
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(input.DisplayID), strconv.Itoa(input.EventID),
			strconv.Itoa(input.LocationID), input.ViewKey,
		),
		Action: "AssignDisplay", TargetType: "Display",
		TargetID: strconv.Itoa(input.DisplayID), Now: service.now().UTC(),
	}
	result, err := command.Execute(actor.Context(ctx), command.Plan[Assignment]{
		Storage: service.storage, Identity: identity, Replay: replayAssignment,
		Apply: func(transaction *store.CommandTx) (command.Execution[Assignment], error) {
			if !actor.Administrator {
				return assignmentRejection(ErrAdministratorRequired), nil
			}
			if input.DisplayID <= 0 || input.EventID <= 0 || input.LocationID <= 0 || !displayviews.IsNormal(input.ViewKey) {
				return assignmentRejection(ErrInvalidDisplay), nil
			}
			stored, storeErr := transaction.AssignDisplay(actor.Context(ctx), store.DisplayAssignment{
				DisplayID: input.DisplayID, EventID: input.EventID,
				LocationID: input.LocationID, ViewKey: input.ViewKey,
			}, identity.Now)
			switch {
			case errors.Is(storeErr, store.ErrDisplayNotFound):
				return assignmentRejection(ErrDisplayNotFound), nil
			case errors.Is(storeErr, store.ErrDisplayAssignmentReference):
				return assignmentRejection(ErrAssignmentReference), nil
			case storeErr != nil:
				return command.Execution[Assignment]{}, storeErr
			}
			encoded, encodeErr := jsonMarshal(stored)
			if encodeErr != nil {
				return command.Execution[Assignment]{}, encodeErr
			}
			return command.Success(assignment(stored), encoded), nil
		},
	})
	if err != nil {
		return Assignment{}, restoreDisplayRejection(err)
	}
	return result, nil
}

// ClaimEnrollment consumes one code and persistently enrolls its Display.
func (service *Service) ClaimEnrollment(
	ctx context.Context,
	actor auth.Account,
	input ClaimInput,
) (Display, error) {
	code := normalizeEnrollmentCode(input.Code)
	name := strings.TrimSpace(input.Name)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Display{}, ErrInvalidDisplay
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(code, name), Action: "EnrollDisplay",
		TargetType: "Display", TargetID: "unidentified", Now: service.now().UTC(),
	}
	result, err := command.Execute(actor.Context(ctx), command.Plan[Display]{
		Storage: service.storage, Identity: identity, Replay: replayDisplay,
		Apply: func(transaction *store.CommandTx) (command.Execution[Display], error) {
			if !actor.Administrator {
				return displayRejection(ErrAdministratorRequired), nil
			}
			if !validEnrollmentCode(code) || !validDisplayName(name) {
				return displayRejection(ErrInvalidDisplay), nil
			}
			created, createErr := transaction.ClaimDisplayEnrollment(
				actor.Context(ctx), digest(code), name, identity.Now,
			)
			if errors.Is(createErr, store.ErrDisplayEnrollmentUnavailable) {
				return displayRejection(ErrEnrollmentUnavailable), nil
			}
			if createErr != nil {
				return command.Execution[Display]{}, createErr
			}
			encoded, encodeErr := jsonMarshal(created)
			if encodeErr != nil {
				return command.Execution[Display]{}, encodeErr
			}
			return command.Success(display(created), encoded).
				WithTargetID(store.DisplayTargetID(created.ID)), nil
		},
	})
	if err != nil {
		return Display{}, restoreDisplayRejection(err)
	}
	return result, nil
}

// EnrollmentForBrowser reuses exact pending material or issues a fresh offer.
func (service *Service) EnrollmentForBrowser(
	ctx context.Context,
	code string,
	credential string,
) (Enrollment, error) {
	now := service.now().UTC()
	if validEnrollmentCode(code) && validDisplayToken(credential) {
		expiresAt, pending, err := service.storage.PendingDisplayEnrollment(
			ctx, digest(code), digest(credential), now,
		)
		if err != nil {
			return Enrollment{}, err
		}
		if pending {
			return Enrollment{
				Code: code, Credential: credential, ExpiresAt: expiresAt,
				CredentialExpires: now.Add(credentialLifetime),
			}, nil
		}
	}
	for range 3 {
		issued, err := service.newEnrollment(ctx, now)
		if errors.Is(err, store.ErrDisplayEnrollmentConflict) {
			continue
		}
		return issued, err
	}
	return Enrollment{}, errors.New("generate unique Display Enrollment")
}

func (service *Service) newEnrollment(ctx context.Context, now time.Time) (Enrollment, error) {
	codeBytes := make([]byte, enrollmentCodeBytes)
	if _, err := io.ReadFull(service.random, codeBytes); err != nil {
		return Enrollment{}, errors.New("generate Display Enrollment code")
	}
	encodedCode := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(codeBytes)
	code := encodedCode[:4] + "-" + encodedCode[4:]
	tokenBytes := make([]byte, displayTokenBytes)
	if _, err := io.ReadFull(service.random, tokenBytes); err != nil {
		return Enrollment{}, errors.New("generate Display credential")
	}
	credential := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := now.Add(service.enrollmentTTL)
	if err := service.storage.IssueDisplayEnrollment(ctx, store.DisplayEnrollmentParams{
		CodeHash: digest(code), CredentialHash: digest(credential), CreatedAt: now, ExpiresAt: expiresAt,
	}); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{
		Code: code, Credential: credential, ExpiresAt: expiresAt,
		CredentialExpires: now.Add(credentialLifetime),
	}, nil
}

func validEnrollmentCode(code string) bool {
	compact := strings.ReplaceAll(code, "-", "")
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(compact)
	return err == nil && len(decoded) == enrollmentCodeBytes &&
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded) == compact &&
		len(code) == 9 && code[4] == '-'
}

func normalizeEnrollmentCode(code string) string {
	compact := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
	if len(compact) != 8 {
		return code
	}
	return compact[:4] + "-" + compact[4:]
}

func validDisplayName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validDisplayToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == displayTokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func digest(value string) string {
	hashed := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hashed)
}

func display(found store.Display) Display {
	return Display{ID: found.ID, Name: found.Name, EnrolledAt: found.EnrolledAt}
}

func assignment(found store.DisplayAssignment) Assignment {
	return Assignment{
		DisplayID: found.DisplayID, EventID: found.EventID,
		LocationID: found.LocationID, ViewKey: found.ViewKey,
	}
}

const (
	displayOfflineAfter      = 15 * time.Second
	excessiveClockSkew       = 250 * time.Millisecond
	unstableClockUncertainty = time.Second
)

func status(found store.DisplayStatus, cursor displaystream.Cursor, now time.Time) Status {
	result := Status{
		ID: found.ID, Name: found.Name, ActiveEventID: found.ActiveEventID, Standby: found.Standby,
		EventName: found.EventName, LocationName: found.LocationName, ViewKey: found.ViewKey,
		AppliedActiveEventID:        found.AppliedActiveEventID,
		AppliedActivationGeneration: found.AppliedActivationGeneration,
		AppliedPublishedRevision:    found.AppliedPublishedRevision,
		AppliedStandby:              found.AppliedStandby, AppliedAt: found.AppliedAt,
		ClockOffset:      found.ClockOffsetMilliseconds,
		ClockUncertainty: found.ClockUncertaintyMilliseconds,
	}
	result.DeliveryState = displayDeliveryState(found, cursor, now)
	return result
}

func displayDeliveryState(found store.DisplayStatus, cursor displaystream.Cursor, now time.Time) string {
	if found.AppliedAt == nil || now.Sub(*found.AppliedAt) > displayOfflineAfter {
		return "offline"
	}
	if found.RendererUnstable ||
		time.Duration(found.ClockUncertaintyMilliseconds)*time.Millisecond > unstableClockUncertainty {
		return "unstable"
	}
	if abs64(found.ClockOffsetMilliseconds) > excessiveClockSkew.Milliseconds() {
		return "excessively_skewed"
	}
	if found.AppliedProtocolVersion != protocolVersion ||
		found.AppliedAssetVersion != AssetVersion() ||
		found.AppliedStreamID != cursor.StreamID ||
		found.AppliedStreamPosition < 0 ||
		uint64(found.AppliedStreamPosition) < cursor.Position ||
		found.AppliedActiveEventID != found.ActiveEventID ||
		found.AppliedActivationGeneration != found.ActivationGeneration ||
		found.AppliedPublishedRevision != found.PublishedRevision ||
		found.AppliedStandby != found.Standby {
		return "lagging"
	}
	return "applied"
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func displaySession(
	snapshot store.DisplaySnapshotState,
	found store.DisplaySessionState,
	now time.Time,
	zone *time.Location,
) (Session, bool) {
	if found.Lifecycle == "Ended" || found.Lifecycle != "Live" && !found.ForecastEnd.After(now) {
		return Session{}, false
	}
	if snapshot.ViewKey == displayviews.EventOverview && found.AudienceVisibility != "Public" {
		return Session{}, false
	}
	if snapshot.ViewKey == displayviews.LocationSignage && !containsID(found.LocationIDs, snapshot.LocationID) {
		return Session{}, false
	}
	if found.AudienceVisibility != "Public" {
		return Session{
			Unavailable: true,
			AvailabilityMessage: "Location unavailable until " +
				found.ForecastEnd.In(zone).Format("Jan 2, 2006 15:04 MST"),
			orderAt: found.ForecastStart,
		}, true
	}
	return Session{
		ID: found.ID, Title: found.Title, Speaker: found.Speaker, PublicDetails: found.PublicDetails,
		ForecastStart: found.ForecastStart, ForecastEnd: found.ForecastEnd,
		Lifecycle: found.Lifecycle, LiveStateRevision: found.LiveStateRevision,
		ActualStart: found.ActualStart, ActualEnd: found.ActualEnd,
		LocationIDs: found.LocationIDs, LaneIDs: found.LaneIDs, TrackIDs: found.TrackIDs,
		DisplayForecastStart: found.ForecastStart.In(zone).Format("Jan 2, 2006 15:04 MST"),
		DisplayForecastEnd:   found.ForecastEnd.In(zone).Format("Jan 2, 2006 15:04 MST"),
		orderAt:              found.ForecastStart,
	}, true
}

func containsID(ids []int, selected int) bool {
	return slices.Contains(ids, selected)
}

func displayRejection(reason error) command.Execution[Display] {
	return command.Reject(Display{}, displayCommandRejection(reason), reason)
}

func assignmentRejection(reason error) command.Execution[Assignment] {
	return command.Reject(Assignment{}, displayCommandRejection(reason), reason)
}

func displayCommandRejection(reason error) store.CommandRejection {
	code := "invalid_display"
	switch {
	case errors.Is(reason, ErrAdministratorRequired):
		code = "administrator_required"
	case errors.Is(reason, ErrEnrollmentUnavailable):
		code = "enrollment_unavailable"
	case errors.Is(reason, ErrDisplayNotFound):
		code = "display_not_found"
	case errors.Is(reason, ErrAssignmentReference):
		code = "assignment_reference"
	}
	return store.CommandRejection{Code: code}
}

func replayDisplay(outcome string) (Display, error) {
	var found store.Display
	if err := store.DecodeCommandReceipt(outcome, &found); err != nil {
		return Display{}, restoreDisplayRejection(err)
	}
	return display(found), nil
}

func replayAssignment(outcome string) (Assignment, error) {
	var found store.DisplayAssignment
	if err := store.DecodeCommandReceipt(outcome, &found); err != nil {
		return Assignment{}, restoreDisplayRejection(err)
	}
	return assignment(found), nil
}

func restoreDisplayRejection(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	switch rejected.Rejection.Code {
	case "administrator_required":
		return ErrAdministratorRequired
	case "enrollment_unavailable":
		return ErrEnrollmentUnavailable
	case "invalid_display":
		return ErrInvalidDisplay
	case "display_not_found":
		return ErrDisplayNotFound
	case "assignment_reference":
		return ErrAssignmentReference
	default:
		return errors.New("Display command unavailable")
	}
}

func jsonMarshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("encode Display command outcome")
	}
	return string(encoded), nil
}
