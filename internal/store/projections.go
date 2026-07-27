package store

import "github.com/dotwaffle/beamers/ent"

func eventProjection(found *ent.Event) Event {
	return Event{
		ID:                      found.ID,
		Name:                    found.Name,
		PublicSlug:              found.PublicSlug,
		Public:                  found.Public,
		PlannedStartDate:        found.PlannedStartDate,
		PlannedEndDate:          found.PlannedEndDate,
		Timezone:                found.Timezone,
		EventLocale:             found.EventLocale,
		ContentLanguage:         found.ContentLanguage,
		EventDayBoundary:        found.EventDayBoundary,
		EntryDefaultDisposition: string(found.EntryDefaultDisposition),
		SubmissionEligibility:   string(found.SubmissionEligibility),
		TargetAdjustmentPresets: found.TargetAdjustmentPresets,
		Revision:                found.Revision,
	}
}

func publicEventProjection(found *ent.Event) PublicEvent {
	return PublicEvent{
		ID: found.ID, Name: found.Name, PublicSlug: found.PublicSlug,
		PlannedStartDate: found.PlannedStartDate,
		PlannedEndDate:   found.PlannedEndDate,
		EventLocale:      found.EventLocale,
	}
}
