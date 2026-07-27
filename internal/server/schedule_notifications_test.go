package server

import "testing"

func TestPublicScheduleNotificationsExcludeCrewOnlyActions(t *testing.T) {
	for _, action := range []string{
		"review-entry",
		"configure-readiness",
		"attachment-readiness",
		"claim-control",
	} {
		if publicScheduleEntryAction(action) {
			t.Errorf("Entry action %q notifies public Schedule", action)
		}
	}
	for _, action := range []string{"create-entry", "update-entry", "change-disposition", "resolve-entry"} {
		if !publicScheduleEntryAction(action) {
			t.Errorf("Entry action %q does not notify public Schedule", action)
		}
	}
	for _, action := range []string{"enroll-display", "assign-display"} {
		if publicScheduleOperationAction(action) {
			t.Errorf("Operation action %q notifies public Schedule", action)
		}
	}
	if !publicScheduleOperationAction("start-session") {
		t.Error("Session operation does not notify public Schedule")
	}
}
