// Package authz declares the Capability Table: the single statement of which
// Capability each state-changing command action requires and against which
// Event scope dimension it is judged.
//
// The table is data in shape and compiled in practice, so a row changes exactly
// when command code changes. One evaluator applies it inside the command
// execution path, after the Command Receipt lookup and before apply, which is
// what makes every refusal leave a Command Receipt and a Rejected Audit Entry.
//
// # Parity
//
// This is the Stage 2 landing of ADR 0061, and it mirrors the imperative checks
// it will eventually replace rather than reforming them. The imperative checks
// stay in place alongside the evaluator until Stage 3 deletes them area by area.
//
// The rule the table obeys is that no row is stricter than the guard it
// mirrors: every actor admitted today is admitted by its row. Rows are as
// strict as today wherever the enum can say so, and where today's guard is
// stricter than any Capability the enum can name — a Producer-only guard on an
// action whose Capability an Operator can hold — the row carries a Note naming
// the discrepancy from the Stage 1 draft, and the imperative guard supplying
// the extra strictness remains until that discrepancy is decided. A row is
// never silently loosened: the Note is the record.
//
// The discrepancies the Stage 1 draft marks as needing a behavior-change
// decision (D3, D4, D5) are therefore encoded as today's behavior, not as
// the draft's proposed resolution. D6 has since been decided and implemented:
// a Program Channel Override target is judged by the Display Groups of the
// Displays consuming it, not by a synthetic identifier key.
package authz

import "github.com/dotwaffle/beamers/internal/viewer"

// Capability is one authority a command action can require. The set is closed:
// a row naming a Capability outside it fails the completeness check.
type Capability string

// Installation Capabilities are held only by the installation Administrator and
// are judged with scope dimension ScopeNone.
//
// The code has one Administrator boolean where these are six constants. The
// split is deliberate: rows are written in this vocabulary, and one
// AdministerInstallation constant would make every administrative row
// indistinguishable. Role expansion maps the Administrator flag to all six, so
// behavior is unchanged.
const (
	// AdministerAccounts covers Account creation, disablement, and recovery
	// token issue.
	AdministerAccounts Capability = "AdministerAccounts"
	// AdministerEvents covers Event creation, Event Grant creation, and Event
	// Slug Alias pruning.
	AdministerEvents Capability = "AdministerEvents"
	// AdministerActiveEvent covers activating one Event.
	AdministerActiveEvent Capability = "AdministerActiveEvent"
	// AdministerDisplays covers Display Enrollment and Display assignment.
	AdministerDisplays Capability = "AdministerDisplays"
	// AdministerInstallationThemes covers Installation Theme revisions.
	AdministerInstallationThemes Capability = "AdministerInstallationThemes"
	// AdministerInterchange covers Event Interchange import.
	AdministerInterchange Capability = "AdministerInterchange"
)

// Viewing Capabilities are held by every Crew Member with an Event Grant,
// whatever their role.
const (
	// ViewEventCrew covers crew reads of an Event. It has no state-changing
	// action and therefore no row; it exists for the read entrypoints that
	// reuse this evaluator.
	ViewEventCrew Capability = "ViewEventCrew"
)

// Producer Capabilities are held by the Producer of an Event and by nobody
// else. They are not grantable: an Operator cannot be given one.
const (
	// ConfigureEvent covers Event settings and Display configuration.
	ConfigureEvent Capability = "ConfigureEvent"
	// ConfigureRundown covers Draft editing, history, import, and Publish.
	ConfigureRundown Capability = "ConfigureRundown"
	// ConfigureCompetition covers Competition readiness, Submission
	// Eligibility, Entry order, and Entry authoring.
	ConfigureCompetition Capability = "ConfigureCompetition"
	// ManageAttachments covers Attachment upload, release, and cues.
	ManageAttachments Capability = "ManageAttachments"
	// ManageEventThemes covers Event Theme revisions.
	ManageEventThemes Capability = "ManageEventThemes"
	// ManageVoting covers Voting Key administration and voting windows.
	ManageVoting Capability = "ManageVoting"
	// ManagePresentations covers Reopen Windows and Submitter assignment.
	ManagePresentations Capability = "ManagePresentations"
	// CaptureScheduleBaseline covers Public Schedule baseline capture.
	CaptureScheduleBaseline Capability = "CaptureScheduleBaseline"
	// ExportInterchange covers Event Interchange export.
	ExportInterchange Capability = "ExportInterchange"
	// ConfigureOverrides covers Stage Message preset configuration.
	ConfigureOverrides Capability = "ConfigureOverrides"
)

// Operation Capabilities are held by the Producer of an Event and by an
// Operator of it. They are the Capabilities a scope dimension narrows.
const (
	// OperateSession covers live Session progression and correction.
	OperateSession Capability = "OperateSession"
	// OperateCompetitionEntry covers live Competition Entry disposition.
	OperateCompetitionEntry Capability = "OperateCompetitionEntry"
	// OperateDisplayOverride covers ordinary Display Override activation and
	// clearing.
	OperateDisplayOverride Capability = "OperateDisplayOverride"
	// OperateProgramChannel covers Program Channel control and Program Output.
	OperateProgramChannel Capability = "OperateProgramChannel"
)

// Grantable Capabilities are the three an Event Grant can name explicitly. The
// set is frozen for this program: Event Grant schema and administration do not
// change.
const (
	// EmergencyAlert permits Emergency Alert activation and clearing.
	EmergencyAlert Capability = Capability(viewer.EmergencyAlert)
	// ViewResults permits reading unreleased Event results.
	ViewResults Capability = Capability(viewer.ViewResults)
	// ManageResults permits mutating Event results.
	ManageResults Capability = Capability(viewer.ManageResults)
)

// class is how a Capability expands from a role at evaluation.
type class uint8

const (
	classInstallation class = iota
	classViewing
	classProducer
	classOperation
	classGrantable
)

// capabilities is the closed enum, each constant paired with the role expansion
// that decides whether an identity holds it.
var capabilities = map[Capability]class{
	AdministerAccounts:           classInstallation,
	AdministerEvents:             classInstallation,
	AdministerActiveEvent:        classInstallation,
	AdministerDisplays:           classInstallation,
	AdministerInstallationThemes: classInstallation,
	AdministerInterchange:        classInstallation,

	ViewEventCrew: classViewing,

	ConfigureEvent:          classProducer,
	ConfigureRundown:        classProducer,
	ConfigureCompetition:    classProducer,
	ManageAttachments:       classProducer,
	ManageEventThemes:       classProducer,
	ManageVoting:            classProducer,
	ManagePresentations:     classProducer,
	CaptureScheduleBaseline: classProducer,
	ExportInterchange:       classProducer,
	ConfigureOverrides:      classProducer,

	OperateSession:          classOperation,
	OperateCompetitionEntry: classOperation,
	OperateDisplayOverride:  classOperation,
	OperateProgramChannel:   classOperation,

	EmergencyAlert: classGrantable,
	ViewResults:    classGrantable,
	ManageResults:  classGrantable,
}

// Known reports whether capability is in the closed enum.
func Known(capability Capability) bool {
	_, ok := capabilities[capability]
	return ok
}

// Capabilities returns the closed enum, for walking tests.
func Capabilities() []Capability {
	names := make([]Capability, 0, len(capabilities))
	for capability := range capabilities {
		names = append(names, capability)
	}
	return names
}

// holds expands identity's roles to Capabilities and reports whether the
// expansion contains capability for eventID.
//
// Producer expands to every Event Capability, Operator to the operation
// Capabilities plus explicitly granted ones, Observer to the viewing
// Capabilities plus a granted ViewResults. Installation Capabilities come from
// the Administrator flag alone and ignore eventID.
func holds(identity viewer.Identity, eventID int, capability Capability) bool {
	switch capabilities[capability] {
	case classInstallation:
		return identity.Administrator
	case classViewing:
		_, granted := identity.EventRoles[eventID]
		return granted
	case classProducer:
		return identity.CanProduceEvent(eventID)
	case classOperation:
		role := identity.EventRoles[eventID]
		return role == viewer.Producer || role == viewer.Operator
	case classGrantable:
		return identity.HasCapability(eventID, viewer.Capability(capability))
	default:
		return false
	}
}
