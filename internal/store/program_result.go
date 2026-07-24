package store

import (
	"math/big"
	"slices"
)

func programScoreBars(found CompetitionResultsDraft) []ProgramScoreBar {
	type score struct {
		entryID int
		value   *big.Rat
	}
	scores := make([]score, 0, len(found.Standings))
	for _, standing := range found.Standings {
		value := new(big.Rat)
		switch {
		case standing.DecimalScore != nil:
			if _, ok := value.SetString(*standing.DecimalScore); !ok {
				return nil
			}
		case standing.DurationScoreNanos != nil:
			value.SetInt64(*standing.DurationScoreNanos)
		default:
			return nil
		}
		scores = append(scores, score{entryID: standing.EntryID, value: value})
	}
	if len(scores) == 0 {
		return nil
	}
	minimum := new(big.Rat).Set(scores[0].value)
	maximum := new(big.Rat).Set(scores[0].value)
	for _, value := range scores[1:] {
		if value.value.Cmp(minimum) < 0 {
			minimum.Set(value.value)
		}
		if value.value.Cmp(maximum) > 0 {
			maximum.Set(value.value)
		}
	}
	span := new(big.Rat).Sub(maximum, minimum)
	bars := make([]ProgramScoreBar, 0, len(scores))
	for _, value := range scores {
		basisPoints := uint32(10000)
		if span.Sign() != 0 {
			numerator := new(big.Rat)
			if found.ScoreInterpretation == "LowerWins" {
				numerator.Sub(maximum, value.value)
			} else {
				numerator.Sub(value.value, minimum)
			}
			scaled := new(big.Rat).Mul(
				numerator,
				big.NewRat(7500, 1),
			)
			scaled.Quo(scaled, span)
			scaled.Add(scaled, big.NewRat(2500, 1))
			rounded, _ := scaled.Float64()
			basisPoints = uint32(rounded + 0.5)
		}
		bars = append(bars, ProgramScoreBar{
			EntryID: value.entryID, BasisPoints: basisPoints,
		})
	}
	return bars
}

func programCompetitionResults(
	found CompetitionResultsDraft,
	ref PrizegivingResultItemRef,
) CompetitionResultsDraft {
	found.NoPublicCrewReason = ""
	found.ReadyByAccountID = 0
	found.CreatedByAccountID = 0
	switch ref.Kind {
	case "NoPublicResults":
		found.Standings = nil
		found.Awards = nil
	case "CompetitionAward":
		found.Standings = nil
		selected := make([]CompetitionAward, 0, 1)
		for _, award := range found.Awards {
			if award.Key == ref.AwardKey {
				selected = append(selected, award)
				break
			}
		}
		found.Awards = selected
	default:
		standings := slices.Clone(found.Standings)
		if found.ScoreVisibility != "Public" {
			for index := range standings {
				standings[index].DecimalScore = nil
				standings[index].DurationScoreNanos = nil
			}
		}
		found.Standings = standings
		awards := make([]CompetitionAward, 0, len(found.Awards))
		for _, award := range found.Awards {
			if !award.Promoted {
				awards = append(awards, award)
			}
		}
		found.Awards = awards
	}
	return found
}
