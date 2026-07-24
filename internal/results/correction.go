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
			!validCorrectedPublicItem(proposal.Items[index]) ||
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
	if item.Kind != ref.Kind || publicItemArmCount(item) != 1 {
		return false
	}
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

func publicItemArmCount(item PublicResultsItem) int {
	count := 0
	if item.Competition != nil {
		count++
	}
	if item.NoPublicResults != nil {
		count++
	}
	if item.Award != nil {
		count++
	}
	return count
}

func retractsPublicCoverage(current, proposed PublicResultsItem) bool {
	switch {
	case current.Competition != nil && proposed.Competition != nil:
		return publicEntryCount(proposed.Competition) <
			publicEntryCount(current.Competition) ||
			len(proposed.Competition.Awards) < len(current.Competition.Awards) ||
			awardRecipientCount(proposed.Competition.Awards) <
				awardRecipientCount(current.Competition.Awards) ||
			retractsPublicEntries(current.Competition, proposed.Competition) ||
			retractsPublicAwards(
				current.Competition.Awards,
				proposed.Competition.Awards,
			)
	case current.NoPublicResults != nil && proposed.NoPublicResults != nil:
		return false
	case current.Award != nil && proposed.Award != nil:
		return len(proposed.Award.Recipients) < len(current.Award.Recipients) ||
			!containsAllStrings(
				proposed.Award.Recipients,
				current.Award.Recipients,
			)
	default:
		return true
	}
}

func validCorrectedPublicItem(item PublicResultsItem) bool {
	switch {
	case item.Competition != nil:
		if !boundedPublicText(item.Competition.Title, 200, true) {
			return false
		}
		for _, entry := range allPublicEntries(item.Competition) {
			if !boundedPublicText(entry.Name, 200, true) ||
				!boundedPublicText(entry.Score, 200, false) ||
				!boundedPublicText(entry.Message, 10_000, false) {
				return false
			}
		}
		return validCorrectedAwards(item.Competition.Awards)
	case item.NoPublicResults != nil:
		return boundedPublicText(item.NoPublicResults.Title, 200, true) &&
			boundedPublicText(item.NoPublicResults.Explanation, 10_000, true)
	case item.Award != nil:
		return validCorrectedAwards([]PublicResultsAward{*item.Award})
	default:
		return false
	}
}

func validCorrectedAwards(awards []PublicResultsAward) bool {
	for _, award := range awards {
		if !boundedPublicText(award.Name, 200, true) ||
			len(award.Recipients) == 0 {
			return false
		}
		for _, recipient := range award.Recipients {
			if !boundedPublicText(recipient, 200, true) {
				return false
			}
		}
	}
	return true
}

func boundedPublicText(value string, maximum int, required bool) bool {
	trimmed := strings.TrimSpace(value)
	return (!required || trimmed != "") &&
		utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}

func retractsPublicEntries(
	current, proposed *PublicCompetitionResults,
) bool {
	currentEntries := allPublicEntries(current)
	proposedEntries := allPublicEntries(proposed)
	proposedByID := make(map[int]PublicResultEntry, len(proposedEntries))
	for _, entry := range proposedEntries {
		if entry.EntryID > 0 {
			proposedByID[entry.EntryID] = entry
		}
	}
	for _, entry := range currentEntries {
		if entry.EntryID == 0 {
			continue
		}
		next, ok := proposedByID[entry.EntryID]
		if !ok ||
			entry.Score != "" && next.Score == "" ||
			entry.Message != "" && next.Message == "" {
			return true
		}
	}
	return false
}

func allPublicEntries(
	competition *PublicCompetitionResults,
) []PublicResultEntry {
	result := make(
		[]PublicResultEntry,
		0,
		publicEntryCount(competition),
	)
	result = append(result, competition.Placed...)
	result = append(result, competition.Unplaced...)
	result = append(result, competition.Disqualified...)
	return result
}

func retractsPublicAwards(
	current, proposed []PublicResultsAward,
) bool {
	for index, award := range current {
		var next *PublicResultsAward
		if award.Key != "" {
			for proposedIndex := range proposed {
				if proposed[proposedIndex].Key == award.Key {
					next = &proposed[proposedIndex]
					break
				}
			}
		} else if index < len(proposed) {
			next = &proposed[index]
		}
		if next == nil ||
			!containsAllStrings(next.Recipients, award.Recipients) {
			return true
		}
	}
	return false
}

func containsAllStrings(values, required []string) bool {
	counts := make(map[string]int, len(values))
	for _, value := range values {
		counts[value]++
	}
	for _, value := range required {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
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
