package store

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

const (
	draftTargetLocation = "Location"
	draftTargetLane     = "Lane"
	draftTargetTrack    = "Track"
	draftTargetSession  = "Session"

	draftFactEntity             = "entity"
	draftFactName               = "name"
	draftFactLocation           = "location"
	draftFactTitle              = "title"
	draftFactSpeaker            = "speaker"
	draftFactType               = "type"
	draftFactAudienceVisibility = "audience_visibility"
	draftFactPublicDetails      = "public_details"
	draftFactCrewNotes          = "crew_notes"
	draftFactPlannedStart       = "planned_start"
	draftFactPlannedEnd         = "planned_end"
	draftFactTimingPolicy       = "timing_policy"
	draftFactMinimumDuration    = "minimum_duration"
	draftFactStartBoundary      = "start_boundary"
	draftFactEndBoundary        = "end_boundary"
	draftFactLanes              = "lanes"
	draftFactLocations          = "locations"
	draftFactTracks             = "tracks"
)

// rundownDraftFacts owns the structural fact vocabulary and every workflow
// that interprets it. Its seam remains private to the concrete Ent store.
type rundownDraftFacts struct {
	transaction *CommandTx
}

func (transaction *CommandTx) draftFacts() rundownDraftFacts {
	return rundownDraftFacts{transaction: transaction}
}

func (facts rundownDraftFacts) validate(target, factKey string) error {
	if facts.transaction == nil {
		return errors.New("draft fact transaction is required")
	}
	if !validRundownDraftFact(target, factKey) {
		return errors.New("unsupported Rundown Draft fact")
	}
	return nil
}

// RundownDraftUpdateFields returns the complete accepted update vocabulary for
// one structural Rundown target.
func RundownDraftUpdateFields(target string) []string {
	switch target {
	case draftTargetLocation, draftTargetTrack:
		return []string{draftFactName}
	case draftTargetLane:
		return []string{draftFactName, draftFactLocation}
	case draftTargetSession:
		return []string{
			draftFactTitle, draftFactSpeaker, draftFactType, draftFactAudienceVisibility,
			draftFactPublicDetails, draftFactCrewNotes, draftFactPlannedStart,
			draftFactPlannedEnd, draftFactTimingPolicy, draftFactMinimumDuration,
			draftFactStartBoundary, draftFactEndBoundary,
			"add_lanes", "remove_lanes", "add_locations", "remove_locations",
			"add_tracks", "remove_tracks",
		}
	default:
		return nil
	}
}

func validRundownDraftFact(target, factKey string) bool {
	fields := RundownDraftUpdateFields(target)
	if fields == nil {
		return false
	}
	if factKey == draftFactEntity {
		return true
	}
	if target == draftTargetSession {
		family, encodedID, membership := strings.Cut(factKey, ":")
		if membership {
			id, err := strconv.Atoi(encodedID)
			return err == nil && id > 0 && slices.Contains(
				[]string{draftFactLanes, draftFactLocations, draftFactTracks}, family,
			)
		}
		if slices.Contains([]string{draftFactLanes, draftFactLocations, draftFactTracks}, factKey) {
			return true
		}
	}
	return slices.Contains(fields, factKey)
}
