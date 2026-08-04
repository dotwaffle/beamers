package store

import (
	"context"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/session"
)

// Capacity is the durable installation shape compared with the tested envelope.
type Capacity struct {
	ActiveEventID int `json:"active_event_id"`
	Locations     int `json:"locations"`
	Lanes         int `json:"lanes"`
	Sessions      int `json:"sessions"`
	Entries       int `json:"entries"`
	Displays      int `json:"displays"`
}

// Capacity returns bounded counts used for operational warnings.
func (installationStore *SQLite) Capacity(ctx context.Context) (Capacity, error) {
	displayCount, err := installationStore.client.Display.Query().Count(ctx)
	if err != nil {
		return Capacity{}, opaqueError("count Displays for capacity", err)
	}
	found := Capacity{Displays: displayCount}
	active, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(ctx)
	if ent.IsNotFound(err) {
		return found, nil
	}
	if err != nil {
		return Capacity{}, opaqueError("load Active Event for capacity", err)
	}
	found.ActiveEventID = *active.ActiveEventID
	counts := []struct {
		target *int
		name   string
		count  func() (int, error)
	}{
		{&found.Locations, "Locations", func() (int, error) {
			return installationStore.client.Location.Query().
				Where(location.EventIDEQ(found.ActiveEventID)).
				Count(ctx)
		}},
		{&found.Lanes, "Lanes", func() (int, error) {
			return installationStore.client.Lane.Query().
				Where(lane.EventIDEQ(found.ActiveEventID)).
				Count(ctx)
		}},
		{&found.Sessions, "Sessions", func() (int, error) {
			return installationStore.client.Session.Query().
				Where(session.EventIDEQ(found.ActiveEventID)).
				Count(ctx)
		}},
		{&found.Entries, "Entries", func() (int, error) {
			return installationStore.client.CompetitionEntry.Query().
				Where(competitionentry.EventIDEQ(found.ActiveEventID)).
				Count(ctx)
		}},
	}
	for _, item := range counts {
		*item.target, err = item.count()
		if err != nil {
			return Capacity{}, opaqueError("count "+item.name+" for capacity", err)
		}
	}
	return found, nil
}
