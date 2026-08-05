package authz

// Row declares the authorization rule for one state-changing command action.
//
// One row exists per action name recorded in a Command Receipt, not per
// authorization rule, so that adding a value to an enum a command concatenates
// into its action name fails the completeness test instead of silently
// producing an unrowed action.
type Row struct {
	// Action is the name recorded in the Command Receipt.
	Action string
	// Capabilities are required unconditionally. An empty list means the
	// action's rule is ownership or eligibility rather than an Event
	// Capability; those rules stay in the store as domain invariants, and the
	// row records the absence so no reviewer mistakes it for an omission.
	Capabilities []Capability
	// TargetCapabilities are the Capabilities this action's plan may
	// additionally demand once it has loaded its target. Declaring them keeps
	// the table the closed statement of what an action can require.
	TargetCapabilities []Capability
	// Scope is the Event scope dimension the row is judged against, and the
	// dimension of facts the command's plan must supply.
	Scope Scope
	// Code is the durable rejection code recorded when a required Capability
	// is missing. It is the code the imperative guard this row mirrors
	// produces today; parity forbids changing it.
	Code string
	// ScopeCode is the durable rejection code recorded when the Capability is
	// held but the target lies outside the actor's scope.
	ScopeCode string
	// Note records a Stage 1 draft discrepancy this row deliberately encodes
	// rather than resolves. A row with a Note states today's behavior and
	// waits on a decision.
	Note string
}

// Durable rejection codes the table produces. Every one except
// codeManageResultsRequired is a code some imperative guard already produces;
// parity forbids changing them.
const (
	codeAdministratorRequired    = "administrator_required"
	codeEventAccessDenied        = "event_access_denied"
	codeProducerRequired         = "producer_required"
	codeOperatorRequired         = "operator_required"
	codeProgramOperatorRequired  = "program_operator_required"
	codeSessionScopeRequired     = "session_scope_required"
	codeOverrideScopeDenied      = "override_scope_denied"
	codeManageResultsRequired    = "manage_results_required"
	noteProgramChannelScope      = "D3, resolved: a Program Channel command is judged by the Display Group keys of the Displays currently consuming the channel, resolved at plan time, so repointing the channel changes who may operate it. A channel feeding no keyed Display is operable only by a Producer, matching the D6 override rule over the same targets."
	noteCompetitionEntryScope    = "D4, resolved: a live Competition Entry action is judged by the Lanes of the Entry's Competition Session, the same dimension the analogous Session actions use."
	noteCompetitionEntryProducer = "D4, resolved: the CanProduceEvent guard this action carried is gone, so an Operator whose Lane grant covers the Competition Session may act, which is the authority the row's Capability always named. The durable code is unchanged for parity."
	noteOverrideTargetKey        = "D6, resolved: a Program Channel Override target expands at plan time to the Display Group keys of the Displays consuming it, so repointing the channel changes who may override it. Location and Display targets remain judged by the synthetic key displayOverrideTargetKey builds, which the decision left unchanged."
	noteOverrideTargetRule       = "D7: Stage Message and Technical Difficulties targets are judged by literal Display Group key today, Urgent Notice and Clear by target. Both are literal keys in practice, so one rule here is parity-preserving."
	noteDegradedEmergency        = "D8: the degraded Emergency Alert path decides this rule a second time in memory at capture time and persists only the outcome. One row now covers both paths; the in-memory copy is deleted with its area in Stage 3."
	noteUploadCallers            = "D8/D9: UploadAttachment is reached by a crew caller guarded by CanProduceEvent before Execute and by an account holder whose rule is upload-target ownership. One action name takes one row, and the row must admit the account holder, so the crew Capability stays with the pre-Execute guard until Stage 3."
	noteResultsProducerGuard     = "D13: this action is guarded by CanProduceEvent rather than by the ManageResults Capability the row names, so an Operator holding a ManageResults grant is refused by the imperative check. The row is no stricter than today."
	noteEvidenceGap              = "D9: refused before Execute today, so the refusal leaves no Command Receipt and no Audit Entry. The row makes the refusal evidential once Stage 3 deletes the pre-Execute guard."
)

// table is the Capability Table. Rows are grouped by area in the same order as
// the Stage 1 draft so the two can be read side by side.
var table = []Row{
	// Installation administration. Six Capabilities where the code has one
	// Administrator boolean; role expansion maps the flag to all six.
	{Action: "CreateAccount", Capabilities: []Capability{AdministerAccounts}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "DisableAccount", Capabilities: []Capability{AdministerAccounts}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "IssueAccountRecoveryToken", Capabilities: []Capability{AdministerAccounts}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "CreateEvent", Capabilities: []Capability{AdministerEvents}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "CreateEventGrant", Capabilities: []Capability{AdministerEvents}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "PruneEventSlugAlias", Capabilities: []Capability{AdministerEvents}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "ActivateEvent", Capabilities: []Capability{AdministerActiveEvent}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "EnrollDisplay", Capabilities: []Capability{AdministerDisplays}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "AssignDisplay", Capabilities: []Capability{AdministerDisplays}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "CreateInstallationThemeRevision", Capabilities: []Capability{AdministerInstallationThemes}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "ActivateInstallationThemeRevision", Capabilities: []Capability{AdministerInstallationThemes}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},
	{Action: "ImportEventInterchange", Capabilities: []Capability{AdministerInterchange}, Scope: ScopeNone, Code: codeAdministratorRequired, ScopeCode: codeAdministratorRequired},

	// Event configuration.
	{Action: "UpdateEvent", Capabilities: []Capability{ConfigureEvent}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "ConfigureDisplays", Capabilities: []Capability{ConfigureEvent}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "ExportEventInterchange", Capabilities: []Capability{ExportInterchange}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "CreateEventThemeRevision", Capabilities: []Capability{ManageEventThemes}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ActivateEventThemeRevision", Capabilities: []Capability{ManageEventThemes}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "CapturePublicScheduleBaseline", Capabilities: []Capability{CaptureScheduleBaseline}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},

	// Rundown. DiscardDraftChanges and RevertDraftChange abort Apply with a
	// plain error today, so their refusals become evidential with this row.
	{Action: "EditDraft", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "DeleteDraftSession", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "DiscardDraftChanges", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "RevertDraftChange", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "Publish", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "ImportCSV", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},
	{Action: "ImportICalendar", Capabilities: []Capability{ConfigureRundown}, Scope: ScopeEvent, Code: codeEventAccessDenied, ScopeCode: codeEventAccessDenied},

	// Live Session control. The store already loads the Session's Lanes inside
	// the transaction, which is exactly where the scope facts come from.
	{Action: "StartSession", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "EndSession", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "CancelSession", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "AdjustTarget", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "PullForward", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "CorrectLiveDetails", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},
	{Action: "ReinstateSession", Capabilities: []Capability{OperateSession}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeSessionScopeRequired},

	// Program Channel control.
	{Action: "TakeProgramOutput", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "SelectProgramPreview", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ChangeProgramControlClaim", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ChangeProgramControlRequestHandover", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ChangeProgramControlHandover", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ChangeProgramControlTakeover", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ChangeProgramControlDisconnect", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ActOnPrizegivingResultReveal", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ActOnPrizegivingResultReplayReveal", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ActOnPrizegivingResultSkipToFinal", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "ActOnPrizegivingResultSkipFromStage", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},
	{Action: "DeferCompetitionEntry", Capabilities: []Capability{OperateCompetitionEntry}, Scope: ScopeLanes, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteCompetitionEntryScope},
	{Action: "ReconcileProgressiveResultsPublication", Capabilities: []Capability{OperateProgramChannel}, Scope: ScopeDisplayGroups, Code: codeProgramOperatorRequired, ScopeCode: codeProgramOperatorRequired, Note: noteProgramChannelScope},

	// Display Overrides.
	{Action: "ConfigureStageMessages", Capabilities: []Capability{ConfigureOverrides}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "SendStageMessage", Capabilities: []Capability{OperateDisplayOverride}, Scope: ScopeDisplayGroups, Code: codeOverrideScopeDenied, ScopeCode: codeOverrideScopeDenied, Note: noteOverrideTargetRule},
	{Action: "ActivateTechnicalDifficulties", Capabilities: []Capability{OperateDisplayOverride}, Scope: ScopeDisplayGroups, Code: codeOverrideScopeDenied, ScopeCode: codeOverrideScopeDenied, Note: noteOverrideTargetRule},
	{Action: "ActivateUrgentNotice", Capabilities: []Capability{OperateDisplayOverride}, Scope: ScopeDisplayGroups, Code: codeOverrideScopeDenied, ScopeCode: codeOverrideScopeDenied, Note: noteOverrideTargetKey},
	{Action: "ActivateEmergencyAlert", Capabilities: []Capability{OperateDisplayOverride, EmergencyAlert}, Scope: ScopeDisplayGroups, Code: codeOverrideScopeDenied, ScopeCode: codeOverrideScopeDenied, Note: noteDegradedEmergency},
	{Action: "ClearDisplayOverride", Capabilities: []Capability{OperateDisplayOverride}, TargetCapabilities: []Capability{EmergencyAlert}, Scope: ScopeDisplayGroups, Code: codeOverrideScopeDenied, ScopeCode: codeOverrideScopeDenied, Note: noteDegradedEmergency},

	// Competition.
	{Action: "ConfigureCompetitionReadiness", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ConfigureCompetitionSubmissionEligibility", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ConfigureCompetitionEntryOrder", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "SetEntryAttachmentReadiness", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "CreateCompetitionEntry", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "UpdateCompetitionEntry", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "AssignCompetitionEntrySubmitter", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ChangeCompetitionEntryDisposition", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ReviewCompetitionEntry", Capabilities: []Capability{ConfigureCompetition}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ResolveCompetitionEntry", Capabilities: []Capability{OperateCompetitionEntry}, Scope: ScopeLanes, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteCompetitionEntryProducer},
	{Action: "SetCompetitionEntryReleaseHold", Capabilities: []Capability{OperateCompetitionEntry}, Scope: ScopeLanes, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteCompetitionEntryProducer},
	{Action: "RecordCompetitionTechnicalFailure", Capabilities: []Capability{OperateCompetitionEntry}, Scope: ScopeLanes, Code: codeOperatorRequired, ScopeCode: codeOperatorRequired, Note: noteCompetitionEntryScope},
	{Action: "CreateSubmittedCompetitionEntry", Scope: ScopeNone},
	{Action: "UpdateSubmittedCompetitionEntry", Scope: ScopeNone},

	// Results. Every row names ManageResults; the rows whose guard is
	// CanProduceEvent today carry the D13 note. The three Manage Results
	// saves carried the D9 evidence-gap note until Stage 3 deleted their
	// pre-Execute guards, and their refusals are now evidential.
	{Action: "SaveCompetitionResultsDraft", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired},
	{Action: "SaveEventAwardsDraft", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired},
	{Action: "SaveCompetitionAwards", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired},
	{Action: "MarkCompetitionResultsReady", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired, Note: noteResultsProducerGuard},
	{Action: "MarkEventAwardsReady", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired, Note: noteResultsProducerGuard},
	{Action: "DesignatePrizegiving", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired, Note: noteResultsProducerGuard},
	{Action: "SavePrizegivingPlan", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired, Note: noteResultsProducerGuard},
	{Action: "RunPrizegivingPreflight", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeManageResultsRequired, ScopeCode: codeManageResultsRequired, Note: noteResultsProducerGuard},
	{Action: "FirePrizegivingResultsCue", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},
	{Action: "ReleaseStandaloneResults", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},
	{Action: "ReleaseStandaloneEventAwards", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},
	{Action: "SaveResultsCorrection", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},
	{Action: "PublishResultsCorrection", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},
	{Action: "ReviewResultsCorrection", Capabilities: []Capability{ManageResults}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteResultsProducerGuard},

	// Attachments and Reopen Windows.
	{Action: "UploadAttachment", Scope: ScopeNone, Note: noteUploadCallers},
	{Action: "ConfigureEventAttachmentRelease", Capabilities: []Capability{ManageAttachments}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "ConfigureCompetitionAttachmentRelease", Capabilities: []Capability{ManageAttachments}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "SetAttachmentVersionRelease", Capabilities: []Capability{ManageAttachments}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "FireEventAttachmentReleaseCue", Capabilities: []Capability{ManageAttachments}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "CreateReopenWindow", Capabilities: []Capability{ManagePresentations}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "ExtendReopenWindow", Capabilities: []Capability{ManagePresentations}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "CloseReopenWindow", Capabilities: []Capability{ManagePresentations}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},

	// Presentations.
	{Action: "AssignPresentationSubmitter", Capabilities: []Capability{ManagePresentations}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired},
	{Action: "UpdatePresentationSubmission", Scope: ScopeNone},

	// Voting.
	{Action: "IssueVotingKeys", Capabilities: []Capability{ManageVoting}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "RevokeVotingKey", Capabilities: []Capability{ManageVoting}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "ConfigureCompetitionVoting", Capabilities: []Capability{ManageVoting}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "OpenCompetitionVoting", Capabilities: []Capability{ManageVoting}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "CloseCompetitionVoting", Capabilities: []Capability{ManageVoting}, Scope: ScopeEvent, Code: codeProducerRequired, ScopeCode: codeProducerRequired, Note: noteEvidenceGap},
	{Action: "RedeemVotingKey", Scope: ScopeNone},
	{Action: "CastCompetitionVote", Scope: ScopeNone},

	// Account self-service. These rows exist because the actions are
	// state-changing commands, not because they need an Event Capability:
	// their rule is ownership, enforced in the store. A row saying so is a
	// declaration; an absent row would be a hole.
	{Action: "UpdateAccountProfile", Scope: ScopeNone},
	{Action: "ReplaceRecoveryCodes", Scope: ScopeNone},
	{Action: "RegisterWebAuthnCredential", Scope: ScopeNone},
	{Action: "RemovePasswordCredential", Scope: ScopeNone},
	{Action: "RevokeWebAuthnCredential", Scope: ScopeNone},
	{Action: "LinkFederatedIdentity", Scope: ScopeNone},
	{Action: "RecoverAccount", Scope: ScopeNone},
}

// rows indexes the table by action name for evaluation.
var rows = func() map[string]Row {
	indexed := make(map[string]Row, len(table))
	for _, row := range table {
		indexed[row.Action] = row
	}
	return indexed
}()

// Table returns every row, in declaration order, for walking tests.
func Table() []Row {
	return table
}

// Lookup returns the row declaring action.
func Lookup(action string) (Row, bool) {
	row, found := rows[action]
	return row, found
}
