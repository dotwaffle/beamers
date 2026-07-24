package results

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrResultsCorrection means a proposal retracts public truth or is malformed.
	ErrResultsCorrection = errors.New("invalid Results Correction")
)

// CorrectionProposal is one exact reviewed replacement presentation.
type CorrectionProposal struct {
	PublicationOrder []ResultItemRef     `json:"publication_order"`
	Items            []PublicResultsItem `json:"items"`
	Template         TextTemplate        `json:"template"`
	CrewReason       string              `json:"crew_reason"`
	PublicNote       string              `json:"public_note,omitempty"`
}

// BuildCorrectedResultsPublication validates a monotonic correction.
func BuildCorrectedResultsPublication(
	current PublicResultsPublication,
	currentOrder []ResultItemRef,
	proposal CorrectionProposal,
	now time.Time,
) (PublicResultsPublication, error) {
	if !boundedCorrectionText(proposal.CrewReason, 10_000, true) ||
		!boundedCorrectionText(proposal.PublicNote, 10_000, false) ||
		len(current.Items) != len(currentOrder) ||
		len(proposal.Items) != len(proposal.PublicationOrder) ||
		!sameCorrectionItemSet(currentOrder, proposal.PublicationOrder) {
		return PublicResultsPublication{}, ErrResultsCorrection
	}
	currentByIdentity := make(map[resultItemIdentity]PublicResultsItem, len(currentOrder))
	for index, ref := range currentOrder {
		currentByIdentity[resultItemIdentityFromRef(ref)] = current.Items[index]
	}
	for index, ref := range proposal.PublicationOrder {
		if ref.DisplayOrder != index+1 ||
			!publicItemMatchesRef(proposal.Items[index], ref) ||
			retractsPublicCoverage(
				currentByIdentity[resultItemIdentityFromRef(ref)],
				proposal.Items[index],
			) {
			return PublicResultsPublication{}, ErrResultsCorrection
		}
	}
	corrected, err := clonePublicResultsPublication(current)
	if err != nil {
		return PublicResultsPublication{}, err
	}
	corrected.Revision = current.Revision + 1
	corrected.PublishedAt = now
	corrected.Correction = &PublicResultsCorrection{
		PreviousRevision: current.Revision,
		Note:             strings.TrimSpace(proposal.PublicNote),
		CorrectedAt:      now,
	}
	corrected.Items = proposal.Items
	return corrected, nil
}

func sameCorrectionItemSet(current, proposed []ResultItemRef) bool {
	if len(current) != len(proposed) {
		return false
	}
	counts := make(map[resultItemIdentity]int, len(current))
	for _, ref := range current {
		counts[resultItemIdentityFromRef(ref)]++
	}
	for _, ref := range proposed {
		identity := resultItemIdentityFromRef(ref)
		counts[identity]--
		if counts[identity] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func resultItemIdentityFromRef(ref ResultItemRef) resultItemIdentity {
	return resultItemIdentity{
		Kind: ref.Kind, CompetitionSessionID: ref.CompetitionSessionID,
		AwardKey: ref.AwardKey,
	}
}

func publicItemMatchesRef(item PublicResultsItem, ref ResultItemRef) bool {
	switch ref.Kind {
	case ResultItemCompetition:
		return item.Competition != nil &&
			item.Competition.SessionID == ref.CompetitionSessionID
	case ResultItemNoPublicResults:
		return item.NoPublicResults != nil &&
			item.NoPublicResults.SessionID == ref.CompetitionSessionID
	case ResultItemCompetitionAward, ResultItemEventAward:
		return item.Award != nil
	default:
		return false
	}
}

func retractsPublicCoverage(current, proposed PublicResultsItem) bool {
	switch {
	case current.Competition != nil && proposed.Competition != nil:
		return publicEntryCount(proposed.Competition) <
			publicEntryCount(current.Competition) ||
			len(proposed.Competition.Awards) < len(current.Competition.Awards) ||
			awardRecipientCount(proposed.Competition.Awards) <
				awardRecipientCount(current.Competition.Awards)
	case current.NoPublicResults != nil && proposed.NoPublicResults != nil:
		return false
	case current.Award != nil && proposed.Award != nil:
		return len(proposed.Award.Recipients) < len(current.Award.Recipients)
	default:
		return true
	}
}

func publicEntryCount(competition *PublicCompetitionResults) int {
	return len(competition.Placed) +
		len(competition.Unplaced) +
		len(competition.Disqualified)
}

func awardRecipientCount(awards []PublicResultsAward) int {
	total := 0
	for _, award := range awards {
		total += len(award.Recipients)
	}
	return total
}

func boundedCorrectionText(value string, maximum int, required bool) bool {
	trimmed := strings.TrimSpace(value)
	return (!required || trimmed != "") &&
		utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}

func clonePublicResultsPublication(
	value PublicResultsPublication,
) (PublicResultsPublication, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PublicResultsPublication{}, ErrResultsCorrection
	}
	var clone PublicResultsPublication
	if json.Unmarshal(encoded, &clone) != nil {
		return PublicResultsPublication{}, ErrResultsCorrection
	}
	clone.EventTitle = clone.Event.Name
	return clone, nil
}
