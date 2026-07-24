package results

import (
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"
)

// PublicResultsSource contains the exact facts captured for one release.
type PublicResultsSource struct {
	EventName   string
	Revision    int
	Status      PublicationStatus
	PublishedAt time.Time
	Items       []PublicResultsSourceItem
}

// PublicResultsSourceItem contains one released item's immutable source facts.
type PublicResultsSourceItem struct {
	Ref               ResultItemRef
	CompetitionTitle  string
	PublicExplanation string
	Score             ScorePolicy
	Entries           []PublicResultsSourceEntry
	Awards            []PublicResultsSourceAward
	Award             *PublicResultsSourceAward
}

// PublicResultsSourceEntry contains public identity and locked result facts.
type PublicResultsSourceEntry struct {
	Name                          string
	ResultDisposition             string
	PublicDisqualificationMessage string
	LockedOrder                   int
	Standing                      ResultStanding
	Placement                     int
	DisplayOrder                  int
	DecimalScore                  string
	DurationScore                 *time.Duration
}

// PublicResultsSourceAward contains one resolved Award snapshot.
type PublicResultsSourceAward struct {
	Name       string
	Recipients []string
}

// BuildPublicResultsModel projects one release into the cross-format model.
func BuildPublicResultsModel(
	source PublicResultsSource,
) (PublicResultsPublication, error) {
	if strings.TrimSpace(source.EventName) == "" ||
		source.Revision <= 0 ||
		source.Status != ResultsPublicationPartial &&
			source.Status != ResultsPublicationFinal {
		return PublicResultsPublication{}, ErrResultsRendering
	}
	model := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         PublicResultsEvent{Name: source.EventName},
		EventTitle:    source.EventName,
		Revision:      source.Revision,
		Status:        source.Status,
		PublishedAt:   source.PublishedAt,
		Items:         make([]PublicResultsItem, 0, len(source.Items)),
	}
	for index, sourceItem := range source.Items {
		if sourceItem.Ref.DisplayOrder != index+1 {
			return PublicResultsPublication{}, ErrResultsRendering
		}
		item, err := buildPublicResultsItem(sourceItem)
		if err != nil {
			return PublicResultsPublication{}, err
		}
		model.Items = append(model.Items, item)
	}
	return model, nil
}

func buildPublicResultsItem(
	source PublicResultsSourceItem,
) (PublicResultsItem, error) {
	item := PublicResultsItem{Kind: source.Ref.Kind}
	switch source.Ref.Kind {
	case ResultItemCompetition:
		competition, err := buildPublicCompetitionResults(source)
		item.Competition = competition
		return item, err
	case ResultItemNoPublicResults:
		explanation := strings.TrimSpace(source.PublicExplanation)
		if explanation == "" {
			explanation = "No results published."
		}
		if strings.TrimSpace(source.CompetitionTitle) == "" {
			return PublicResultsItem{}, ErrResultsRendering
		}
		item.NoPublicResults = &PublicNoResults{
			SessionID: source.Ref.CompetitionSessionID,
			Title:     source.CompetitionTitle, Explanation: explanation,
		}
		return item, nil
	case ResultItemCompetitionAward, ResultItemEventAward:
		if source.Award == nil {
			return PublicResultsItem{}, ErrResultsRendering
		}
		award, err := publicResultsAward(*source.Award)
		item.Award = &award
		return item, err
	default:
		return PublicResultsItem{}, ErrResultsRendering
	}
}

func buildPublicCompetitionResults(
	source PublicResultsSourceItem,
) (*PublicCompetitionResults, error) {
	if strings.TrimSpace(source.CompetitionTitle) == "" {
		return nil, ErrResultsRendering
	}
	result := &PublicCompetitionResults{
		SessionID: source.Ref.CompetitionSessionID,
		Title:     source.CompetitionTitle,
	}
	entries := slices.Clone(source.Entries)
	sort.SliceStable(entries, func(first, second int) bool {
		left, right := entries[first], entries[second]
		leftSection := publicResultSection(left)
		rightSection := publicResultSection(right)
		if leftSection != rightSection {
			return leftSection < rightSection
		}
		if leftSection == 0 {
			return left.DisplayOrder < right.DisplayOrder
		}
		return left.LockedOrder < right.LockedOrder
	})
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" {
			return nil, ErrResultsRendering
		}
		publicEntry := PublicResultEntry{Name: entry.Name}
		switch entry.ResultDisposition {
		case "Withheld":
			continue
		case "Disqualified":
			publicEntry.Message = entry.PublicDisqualificationMessage
			result.Disqualified = append(result.Disqualified, publicEntry)
		case "Eligible":
			switch entry.Standing {
			case Placed:
				publicEntry.Placement = entry.Placement
				publicEntry.Score = formatPublicScore(source.Score, entry)
				result.Placed = append(result.Placed, publicEntry)
			case Unplaced:
				publicEntry.Score = formatPublicScore(source.Score, entry)
				result.Unplaced = append(result.Unplaced, publicEntry)
			default:
				return nil, ErrResultsRendering
			}
		default:
			return nil, ErrResultsRendering
		}
	}
	for _, sourceAward := range source.Awards {
		award, err := publicResultsAward(sourceAward)
		if err != nil {
			return nil, err
		}
		result.Awards = append(result.Awards, award)
	}
	return result, nil
}

func publicResultSection(entry PublicResultsSourceEntry) int {
	switch {
	case entry.ResultDisposition == "Eligible" && entry.Standing == Placed:
		return 0
	case entry.ResultDisposition == "Eligible":
		return 1
	case entry.ResultDisposition == "Disqualified":
		return 2
	default:
		return 3
	}
}

func publicResultsAward(
	source PublicResultsSourceAward,
) (PublicResultsAward, error) {
	if strings.TrimSpace(source.Name) == "" || len(source.Recipients) == 0 {
		return PublicResultsAward{}, ErrResultsRendering
	}
	for _, recipient := range source.Recipients {
		if strings.TrimSpace(recipient) == "" {
			return PublicResultsAward{}, ErrResultsRendering
		}
	}
	return PublicResultsAward{
		Name: source.Name, Recipients: slices.Clone(source.Recipients),
	}, nil
}

func formatPublicScore(
	policy ScorePolicy,
	entry PublicResultsSourceEntry,
) string {
	if policy.Visibility != ScorePublic {
		return ""
	}
	var value string
	switch {
	case policy.Type == Decimal && entry.DecimalScore != "":
		value = entry.DecimalScore
	case policy.Type == Duration && entry.DurationScore != nil:
		seconds := new(big.Rat).SetFrac(
			big.NewInt(int64(*entry.DurationScore)),
			big.NewInt(int64(time.Second)),
		)
		value = seconds.FloatString(policy.Precision)
	default:
		return ""
	}
	if policy.Unit != "" {
		value += " " + policy.Unit
	}
	return value
}
