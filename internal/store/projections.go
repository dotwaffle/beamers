package store

import "github.com/dotwaffle/beamers/ent"

func eventProjection(found *ent.Event) Event {
	return Event{
		ID:               found.ID,
		Name:             found.Name,
		PlannedStartDate: found.PlannedStartDate,
		PlannedEndDate:   found.PlannedEndDate,
		Timezone:         found.Timezone,
		EventLocale:      found.EventLocale,
		ContentLanguage:  found.ContentLanguage,
		EventDayBoundary: found.EventDayBoundary,
		Revision:         found.Revision,
	}
}
