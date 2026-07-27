package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestBackstageNavigationReflectsEffectiveAuthority(t *testing.T) {
	account := auth.Account{
		Administrator: true,
		EventRoles: map[int]viewer.Role{
			3: viewer.Observer,
			1: viewer.Producer,
			2: viewer.Operator,
			4: viewer.Operator,
		},
		EventScopes: map[int]viewer.EventScope{
			2: {
				LaneIDs:          map[int]struct{}{7: {}},
				DisplayGroupKeys: map[string]struct{}{"stage": {}},
				Capabilities: map[viewer.Capability]struct{}{
					viewer.EmergencyAlert: {},
					viewer.ViewResults:    {},
				},
			},
			4: {
				Capabilities: map[viewer.Capability]struct{}{
					viewer.EmergencyAlert: {},
				},
			},
		},
	}

	navigation := backstageNavigation(account)
	if !navigation.Administrator {
		t.Fatal("Administrator section hidden")
	}
	if got := backstageEventIDs(navigation); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("Event order = %v, want [1 2 3 4]", got)
	}
	if got := backstageSectionLabels(navigation.Events[0]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Plan and publish",
		"Sessions and Displays",
		"Program Output and Overrides",
		"Emergency Alerts",
		"Competition Entries and Attachments",
		"Results and Prizegiving",
	}) {
		t.Fatalf("Producer sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[1]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Sessions and Displays",
		"Program Output and Overrides",
		"Emergency Alerts",
		"Competition Entries and Attachments",
		"Results and Prizegiving",
	}) {
		t.Fatalf("scoped Operator sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[2]); !reflect.DeepEqual(got, []string{
		"Event overview",
	}) {
		t.Fatalf("Observer sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[3]); !reflect.DeepEqual(got, []string{
		"Event overview",
	}) {
		t.Fatalf("capability-only Operator sections = %v", got)
	}
}

func TestCompetitionAccessRequiresEverySessionLane(t *testing.T) {
	account := auth.Account{
		EventRoles: map[int]viewer.Role{1: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			1: {LaneIDs: map[int]struct{}{7: {}}},
		},
	}
	session := rundown.CrewSession{LaneIDs: []int{7, 8}}
	if canAccessCompetition(account, 1, session) {
		t.Fatal("Operator with partial Competition Lane scope received access")
	}
	account.EventScopes[1].LaneIDs[8] = struct{}{}
	if !canAccessCompetition(account, 1, session) {
		t.Fatal("Operator with complete Competition Lane scope denied access")
	}
}

func TestBackstageNavigationRejectsAttendeeAndSeparatesRouteInterfaces(t *testing.T) {
	if navigation := backstageNavigation(auth.Account{}); navigation.Administrator ||
		len(navigation.Events) != 0 {
		t.Fatalf("attendee navigation = %+v", navigation)
	}

	routes := newRouteMux()
	if err := registerFrontendRoutes(routes, nil, nil, nil, nil); err != nil {
		t.Fatalf("register Frontend routes: %v", err)
	}
	registerPlanningRoutes(routes, nil, nil, nil, nil, nil, nil)
	registerAdministrationRoutes(routes, nil, nil, nil, nil, nil)
	registerOperationRoutes(routes, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for path, want := range map[string]interfaceKind{
		"/profile":                       publicInterface,
		"/backstage":                     crewInterface,
		"/backstage/administration":      crewInterface,
		"/admin/registration":            crewInterface,
		"/backstage/events/1/planning":   crewInterface,
		"/backstage/events/1/operations": crewInterface,
		"/backstage/events/new":          crewInterface,
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		contract, ok := routes.contract(request)
		if !ok || contract.kind != want {
			t.Errorf("%s contract = %+v, %t; want kind %d", path, contract, ok, want)
		}
		if want == crewInterface && !contract.browserWarningPage {
			t.Errorf("%s does not preserve insecure-mode browser warnings", path)
		}
	}
}

func backstageEventIDs(navigation backstageNavigationModel) []int {
	ids := make([]int, 0, len(navigation.Events))
	for _, event := range navigation.Events {
		ids = append(ids, event.ID)
	}
	return ids
}

func backstageSectionLabels(event backstageEventNavigation) []string {
	labels := make([]string, 0, len(event.Sections))
	for _, section := range event.Sections {
		labels = append(labels, section.Label)
	}
	return labels
}
