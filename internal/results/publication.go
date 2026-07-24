package results

import (
	"slices"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

// ReleasePolicy selects when locked Result Items become public.
type ReleasePolicy = prizegivingvalue.ReleasePolicy

const (
	// ResultsAllAtCue releases one complete locked set at an explicit cue.
	ResultsAllAtCue = prizegivingvalue.ReleaseAllAtCue
	// ResultsProgressiveOnReveal releases each completed Reveal independently.
	ResultsProgressiveOnReveal = prizegivingvalue.ReleaseProgressiveOnReveal
	// ResultsAtCeremonyEnd releases one complete resolved set at completion.
	ResultsAtCeremonyEnd = prizegivingvalue.ReleaseAtCeremonyEnd
	// ResultsStandalone releases one unassigned Competition explicitly.
	ResultsStandalone = prizegivingvalue.ReleaseStandalone
)

// PublicationStatus describes a scope's monotonic public state.
type PublicationStatus string

const (
	// ResultsPublicationPartial contains a released subset of one locked scope.
	ResultsPublicationPartial PublicationStatus = "Partial"
	// ResultsPublicationFinal contains the complete released scope.
	ResultsPublicationFinal PublicationStatus = "Final"
)

// Publication is one immutable release-manifest revision.
type Publication struct {
	Revision int               `json:"revision"`
	Status   PublicationStatus `json:"status,omitempty"`
	Items    []ResultItemRef   `json:"items,omitempty"`
}

// PublicationInput is the complete durable truth for one release step.
type PublicationInput struct {
	Policy            ReleasePolicy
	Order             []ResultItemRef
	States            []ResultItemStageState
	Current           Publication
	CueFired          bool
	CeremonyEnded     bool
	StandaloneRelease bool
}

// AdvancePublication returns the next monotonic immutable manifest.
func AdvancePublication(
	input PublicationInput,
) (Publication, bool, error) {
	if input.Current.Status == ResultsPublicationFinal {
		return cloneResultsPublication(input.Current), false, nil
	}
	releaseByIdentity := make(
		map[resultItemIdentity]ResultReleaseState,
		len(input.States),
	)
	for _, state := range input.States {
		releaseByIdentity[resultItemIdentityFromRef(state.Ref)] = state.Release
	}
	items := make([]ResultItemRef, 0, len(input.Order))
	status := ResultsPublicationFinal
	switch input.Policy {
	case ResultsProgressiveOnReveal:
		status = ResultsPublicationPartial
		alreadyReleased := make(
			map[resultItemIdentity]struct{},
			len(input.Current.Items),
		)
		for _, ref := range input.Current.Items {
			alreadyReleased[resultItemIdentityFromRef(ref)] = struct{}{}
		}
		for _, ref := range input.Order {
			identity := resultItemIdentityFromRef(ref)
			release := releaseByIdentity[identity]
			_, wasReleased := alreadyReleased[identity]
			if wasReleased ||
				release == ResultReleaseReady ||
				input.CeremonyEnded && release == ResultReleaseCeremonyEnd {
				items = append(items, ref)
			}
		}
		if input.CeremonyEnded && len(items) == len(input.Order) {
			status = ResultsPublicationFinal
		}
	case ResultsAllAtCue:
		if input.CueFired {
			items = slices.Clone(input.Order)
		}
	case ResultsAtCeremonyEnd:
		if input.CeremonyEnded &&
			allResultsResolved(input.Order, releaseByIdentity) {
			items = slices.Clone(input.Order)
		}
	case ResultsStandalone:
		if input.StandaloneRelease &&
			allResultsReady(input.Order, releaseByIdentity) {
			items = slices.Clone(input.Order)
		}
	}
	if len(items) == 0 ||
		status == input.Current.Status &&
			slices.Equal(items, input.Current.Items) {
		return cloneResultsPublication(input.Current), false, nil
	}
	return Publication{
		Revision: input.Current.Revision + 1,
		Status:   status,
		Items:    items,
	}, true, nil
}

func allResultsResolved(
	order []ResultItemRef,
	releases map[resultItemIdentity]ResultReleaseState,
) bool {
	for _, ref := range order {
		release := releases[resultItemIdentityFromRef(ref)]
		if release != ResultReleaseReady &&
			release != ResultReleaseCeremonyEnd {
			return false
		}
	}
	return true
}

func allResultsReady(
	order []ResultItemRef,
	releases map[resultItemIdentity]ResultReleaseState,
) bool {
	for _, ref := range order {
		if releases[resultItemIdentityFromRef(ref)] != ResultReleaseReady {
			return false
		}
	}
	return true
}

func cloneResultsPublication(value Publication) Publication {
	value.Items = slices.Clone(value.Items)
	return value
}
