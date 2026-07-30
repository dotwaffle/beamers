package server

import "testing"

func TestPublicScheduleOperationNotificationsExcludeDisplayActions(t *testing.T) {
	for _, action := range []string{"enroll-display", "assign-display"} {
		if publicScheduleOperationAction(action) {
			t.Errorf("Operation action %q notifies public Schedule", action)
		}
	}
	if !publicScheduleOperationAction("start-session") {
		t.Error("Session operation does not notify public Schedule")
	}
}
