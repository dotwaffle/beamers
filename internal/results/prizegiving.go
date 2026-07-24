package results

import (
	"crypto/sha256"
	"encoding/binary"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode/utf8"
)

// ResultItemKind identifies one presentable Prizegiving unit.
type ResultItemKind string

const (
	// ResultItemCompetition presents one Competition's reviewed results.
	ResultItemCompetition ResultItemKind = "CompetitionResults"
	// ResultItemNoPublicResults presents one resolved non-public status.
	ResultItemNoPublicResults ResultItemKind = "NoPublicResults"
	// ResultItemCompetitionAward presents one promoted Competition Award.
	ResultItemCompetitionAward ResultItemKind = "CompetitionAward"
	// ResultItemEventAward presents one Event Award.
	ResultItemEventAward ResultItemKind = "EventAward"
)

// RevealMethod controls presentation without changing immutable result truth.
type RevealMethod string

const (
	// RevealStatic immediately presents the final result.
	RevealStatic RevealMethod = "StaticResult"
	// RevealSequentialPodium presents placements in podium order.
	RevealSequentialPodium RevealMethod = "SequentialPodium"
	// RevealAnimatedScoreBars presents exact reviewed numeric scores.
	RevealAnimatedScoreBars RevealMethod = "AnimatedScoreBars"
)

// ResultItemRef identifies one Result Item and its order in one list.
type ResultItemRef struct {
	Kind                 ResultItemKind `json:"kind"`
	CompetitionSessionID int            `json:"competition_session_id,omitempty"`
	AwardKey             string         `json:"award_key,omitempty"`
	DisplayOrder         int            `json:"display_order"`
}

type resultItemIdentity struct {
	Kind                 ResultItemKind
	CompetitionSessionID int
	AwardKey             string
}

// ResultItem is one staged Result Item with its Reveal Method.
type ResultItem struct {
	Kind                 ResultItemKind `json:"kind"`
	CompetitionSessionID int            `json:"competition_session_id,omitempty"`
	AwardKey             string         `json:"award_key,omitempty"`
	DisplayOrder         int            `json:"display_order"`
	RevealMethod         RevealMethod   `json:"reveal_method"`
}

// Ref returns the same Result Item identity at one independent order.
func (item ResultItem) Ref(displayOrder int) ResultItemRef {
	return ResultItemRef{
		Kind: item.Kind, CompetitionSessionID: item.CompetitionSessionID,
		AwardKey: item.AwardKey, DisplayOrder: displayOrder,
	}
}

// TextTemplate is one versioned, preflight-validated Results Text Template.
type TextTemplate struct {
	Revision int    `json:"revision"`
	Source   string `json:"source"`
}

// PrizegivingCompetitionSource is one assigned Competition's current state.
type PrizegivingCompetitionSource struct {
	Draft              Draft
	ResolutionRequired bool
}

// PrizegivingEventAwardsSource is the current Event Awards state for one path.
type PrizegivingEventAwardsSource struct {
	DraftRevision int
	PathRevision  int
	Ready         bool
	Awards        []EventAward
}

// PrizegivingPreflightInput contains the complete state reviewed for a lock.
type PrizegivingPreflightInput struct {
	EventID               int
	CeremonySessionID     int
	PlanRevision          int
	CompetitionSessionIDs []int
	Sequence              []ResultItem
	PublicationOrder      []ResultItemRef
	Template              TextTemplate
	Competitions          []PrizegivingCompetitionSource
	EventAwards           PrizegivingEventAwardsSource
}

// PrizegivingPreflightFinding is one stable blocking result.
type PrizegivingPreflightFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PrizegivingCompetitionLock identifies one exact immutable Results source.
type PrizegivingCompetitionLock struct {
	SessionID     int         `json:"session_id"`
	DraftID       int         `json:"draft_id"`
	DraftRevision int         `json:"draft_revision"`
	Disposition   Disposition `json:"disposition"`
}

// LockedResultItem is one staged item bound to a reproducible Reveal Seed.
type LockedResultItem struct {
	ResultItem
	RevealSeed uint64 `json:"reveal_seed"`
}

// PrizegivingPreflightLock is the complete immutable reviewed release input.
type PrizegivingPreflightLock struct {
	EventID                  int                          `json:"event_id"`
	CeremonySessionID        int                          `json:"ceremony_session_id"`
	PlanRevision             int                          `json:"plan_revision"`
	CompetitionSources       []PrizegivingCompetitionLock `json:"competition_sources"`
	EventAwardsDraftRevision int                          `json:"event_awards_draft_revision"`
	EventAwardsPathRevision  int                          `json:"event_awards_path_revision"`
	Sequence                 []LockedResultItem           `json:"sequence"`
	PublicationOrder         []ResultItemRef              `json:"publication_order"`
	Template                 TextTemplate                 `json:"template"`
}

// BuildPrizegivingPreflight freezes exact source revisions and presentation
// configuration after validating all blockers.
func BuildPrizegivingPreflight(
	input PrizegivingPreflightInput,
	seedNamespace string,
) (PrizegivingPreflightLock, []PrizegivingPreflightFinding) {
	findings := prizegivingReleaseFindings(input)
	if len(findings) != 0 {
		return PrizegivingPreflightLock{}, findings
	}
	locked := PrizegivingPreflightLock{
		EventID: input.EventID, CeremonySessionID: input.CeremonySessionID,
		PlanRevision:             input.PlanRevision,
		EventAwardsDraftRevision: input.EventAwards.DraftRevision,
		EventAwardsPathRevision:  input.EventAwards.PathRevision,
		PublicationOrder:         append([]ResultItemRef(nil), input.PublicationOrder...),
		Template:                 input.Template,
	}
	for _, source := range input.Competitions {
		locked.CompetitionSources = append(
			locked.CompetitionSources,
			PrizegivingCompetitionLock{
				SessionID: source.Draft.SessionID, DraftID: source.Draft.ID,
				DraftRevision: source.Draft.Revision,
				Disposition:   source.Draft.Disposition,
			},
		)
	}
	for _, item := range input.Sequence {
		locked.Sequence = append(locked.Sequence, LockedResultItem{
			ResultItem: item,
			RevealSeed: prizegivingRevealSeed(seedNamespace, item),
		})
	}
	return locked, nil
}

func prizegivingReleaseFindings(
	input PrizegivingPreflightInput,
) []PrizegivingPreflightFinding {
	var findings []PrizegivingPreflightFinding
	for _, source := range input.Competitions {
		switch source.Draft.Disposition {
		case Pending:
			findings = append(findings, prizegivingFinding(
				"pending_disposition",
				"Competition "+strconv.Itoa(source.Draft.SessionID)+" has unresolved Results",
			))
		case Publish:
			if !source.Draft.Ready {
				findings = append(findings, prizegivingFinding(
					"results_not_ready",
					"Competition "+strconv.Itoa(source.Draft.SessionID)+" lacks Ready Results",
				))
			}
		case NoPublicResults:
		default:
			findings = append(findings, prizegivingFinding(
				"pending_disposition",
				"Competition "+strconv.Itoa(source.Draft.SessionID)+" has invalid Results disposition",
			))
		}
		if source.ResolutionRequired {
			findings = append(findings, prizegivingFinding(
				"resolution_required",
				"Competition "+strconv.Itoa(source.Draft.SessionID)+" has unresolved Entries",
			))
		}
		if source.Draft.Disposition == Publish &&
			source.Draft.Score.Requirement == ScoreRequired &&
			prizegivingMissingRequiredScore(source.Draft.Standings) {
			findings = append(findings, prizegivingFinding(
				"required_score_missing",
				"Competition "+strconv.Itoa(source.Draft.SessionID)+" lacks required Scores",
			))
		}
	}
	for _, item := range input.Sequence {
		if !validRevealMethodForItem(item, input.Competitions) {
			findings = append(findings, prizegivingFinding(
				"invalid_reveal_method",
				"Result Item "+strconv.Itoa(item.DisplayOrder)+" has an invalid Reveal Method",
			))
		}
	}
	expectedItems := prizegivingExpectedItems(input)
	if !exactPrizegivingSequence(input.Sequence, expectedItems) {
		findings = append(findings, prizegivingFinding(
			"results_sequence_invalid",
			"Results Sequence does not contain each assigned Result Item exactly once",
		))
	}
	if !exactPrizegivingPublicationOrder(input.PublicationOrder, expectedItems) {
		findings = append(findings, prizegivingFinding(
			"publication_order_invalid",
			"Results Publication Order does not contain each assigned Result Item exactly once",
		))
	}
	if len(input.EventAwards.Awards) != 0 && !input.EventAwards.Ready {
		findings = append(findings, prizegivingFinding(
			"event_awards_not_ready",
			"Prizegiving Event Awards lack Ready review",
		))
	}
	if !safeResultsTextTemplate(input.Template) {
		findings = append(findings, prizegivingFinding(
			"unsafe_results_template",
			"Results Text Template is invalid or unsafe",
		))
	}
	return findings
}

func prizegivingFinding(code, message string) PrizegivingPreflightFinding {
	return PrizegivingPreflightFinding{Code: code, Message: message}
}

func prizegivingMissingRequiredScore(standings []Standing) bool {
	if len(standings) == 0 {
		return true
	}
	for _, standing := range standings {
		if standing.Score.Decimal == nil && standing.Score.Duration == nil {
			return true
		}
	}
	return false
}

func validRevealMethod(method RevealMethod) bool {
	return method == RevealStatic ||
		method == RevealSequentialPodium ||
		method == RevealAnimatedScoreBars
}

func validRevealMethodForItem(
	item ResultItem,
	sources []PrizegivingCompetitionSource,
) bool {
	if !validRevealMethod(item.RevealMethod) {
		return false
	}
	switch item.RevealMethod {
	case RevealStatic:
		return true
	case RevealSequentialPodium:
		return item.Kind == ResultItemCompetition
	case RevealAnimatedScoreBars:
		if item.Kind != ResultItemCompetition {
			return false
		}
		for _, source := range sources {
			if source.Draft.SessionID != item.CompetitionSessionID {
				continue
			}
			return source.Draft.Score.Type != None &&
				len(source.Draft.Standings) != 0 &&
				!prizegivingMissingRequiredScore(source.Draft.Standings)
		}
	}
	return false
}

func prizegivingExpectedItems(
	input PrizegivingPreflightInput,
) map[resultItemIdentity]int {
	expected := make(map[resultItemIdentity]int)
	for _, source := range input.Competitions {
		kind := ResultItemCompetition
		if source.Draft.Disposition == NoPublicResults {
			kind = ResultItemNoPublicResults
		}
		expected[resultItemIdentity{
			Kind: kind, CompetitionSessionID: source.Draft.SessionID,
		}]++
		for _, award := range source.Draft.Awards {
			if award.Promoted {
				expected[resultItemIdentity{
					Kind:                 ResultItemCompetitionAward,
					CompetitionSessionID: source.Draft.SessionID,
					AwardKey:             award.Key,
				}]++
			}
		}
	}
	for _, award := range input.EventAwards.Awards {
		expected[resultItemIdentity{
			Kind: ResultItemEventAward, AwardKey: award.Key,
		}]++
	}
	return expected
}

func exactPrizegivingSequence(
	items []ResultItem,
	expected map[resultItemIdentity]int,
) bool {
	refs := make([]ResultItemRef, 0, len(items))
	for _, item := range items {
		refs = append(refs, item.Ref(item.DisplayOrder))
	}
	return exactPrizegivingItemRefs(refs, expected)
}

func exactPrizegivingPublicationOrder(
	items []ResultItemRef,
	expected map[resultItemIdentity]int,
) bool {
	return exactPrizegivingItemRefs(items, expected)
}

func exactPrizegivingItemRefs(
	items []ResultItemRef,
	expected map[resultItemIdentity]int,
) bool {
	if len(items) != resultItemCount(expected) {
		return false
	}
	remaining := maps.Clone(expected)
	for index, item := range items {
		if item.DisplayOrder != index+1 || !validResultItemRef(item) {
			return false
		}
		identity := resultItemIdentity{
			Kind: item.Kind, CompetitionSessionID: item.CompetitionSessionID,
			AwardKey: item.AwardKey,
		}
		if remaining[identity] == 0 {
			return false
		}
		remaining[identity]--
	}
	for _, count := range remaining {
		if count != 0 {
			return false
		}
	}
	return true
}

func validResultItemRef(item ResultItemRef) bool {
	switch item.Kind {
	case ResultItemCompetition, ResultItemNoPublicResults:
		return item.CompetitionSessionID > 0 && item.AwardKey == ""
	case ResultItemCompetitionAward:
		return item.CompetitionSessionID > 0 && validAwardKey(item.AwardKey)
	case ResultItemEventAward:
		return item.CompetitionSessionID == 0 && validAwardKey(item.AwardKey)
	default:
		return false
	}
}

func resultItemCount(items map[resultItemIdentity]int) int {
	total := 0
	for _, count := range items {
		total += count
	}
	return total
}

func safeResultsTextTemplate(value TextTemplate) bool {
	if !boundedResultsTextTemplate(value) {
		return false
	}
	parsed, err := template.New("results").
		Option("missingkey=error").
		Parse(value.Source)
	if err != nil {
		return false
	}
	for _, defined := range parsed.Templates() {
		if unsafeTemplateNode(defined.Root) {
			return false
		}
	}
	return true
}

func boundedResultsTextTemplate(value TextTemplate) bool {
	return value.Revision > 0 &&
		value.Source != "" &&
		len(value.Source) <= 100_000 &&
		utf8.ValidString(value.Source) &&
		!strings.ContainsRune(value.Source, '\x00')
}

func unsafeTemplateNode(node parse.Node) bool {
	switch value := node.(type) {
	case nil:
		return false
	case *parse.ListNode:
		return slices.ContainsFunc(value.Nodes, unsafeTemplateNode)
	case *parse.ActionNode:
		return unsafeTemplateNode(value.Pipe)
	case *parse.IfNode:
		return unsafeTemplateNode(value.Pipe) ||
			unsafeTemplateNode(value.List) ||
			unsafeTemplateNode(value.ElseList)
	case *parse.RangeNode:
		return unsafeTemplateNode(value.Pipe) ||
			unsafeTemplateNode(value.List) ||
			unsafeTemplateNode(value.ElseList)
	case *parse.WithNode:
		return unsafeTemplateNode(value.Pipe) ||
			unsafeTemplateNode(value.List) ||
			unsafeTemplateNode(value.ElseList)
	case *parse.TemplateNode:
		return unsafeTemplateNode(value.Pipe)
	case *parse.PipeNode:
		for _, command := range value.Cmds {
			if unsafeTemplateNode(command) {
				return true
			}
		}
	case *parse.CommandNode:
		return slices.ContainsFunc(value.Args, unsafeTemplateNode)
	case *parse.IdentifierNode:
		return value.Ident == "call"
	}
	return false
}

func prizegivingRevealSeed(namespace string, item ResultItem) uint64 {
	sum := sha256.Sum256([]byte(
		namespace + "\x00" + string(item.Kind) + "\x00" +
			strconv.Itoa(item.CompetitionSessionID) + "\x00" + item.AwardKey,
	))
	seed := binary.BigEndian.Uint64(sum[:8])
	if seed == 0 {
		return 1
	}
	return seed
}
