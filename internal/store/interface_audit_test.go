package store

import (
	"testing"

	"github.com/dotwaffle/beamers/ent/auditentry"
)

func TestHostInterfaceModeAuditRecordsOnlyTransitions(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}

	for _, enabled := range []bool{false, true, true, false, false} {
		if err := installation.RecordHostInterfaceMode(
			hostMaintenanceContext(t.Context()),
			"Crew",
			enabled,
		); err != nil {
			t.Fatalf("record insecure Crew mode %t: %v", enabled, err)
		}
	}
	entries, err := client.AuditEntry.Query().
		Where(auditentry.ActionEQ("ConfigureInsecureCrew")).
		Order(auditentry.ByID()).
		All(hostMaintenanceContext(t.Context()))
	if err != nil {
		t.Fatalf("read insecure Crew Audit: %v", err)
	}
	if len(entries) != 2 ||
		entries[0].ActorKind != auditentry.ActorKindHost ||
		entries[0].Note != "enabled" ||
		entries[1].Note != "disabled" {
		t.Fatalf("insecure Crew Audit transitions = %+v", entries)
	}
}
