package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
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
		EventNames: map[int]string{
			1: "First Event",
			2: "Second Event",
			3: "Third Event",
			4: "Fourth Event",
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

	navigation := backstageNavigation(account, "")
	if !navigation.Administrator {
		t.Fatal("Administrator section hidden")
	}
	if got := backstageEventIDs(navigation); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("Event order = %v, want [1 2 3 4]", got)
	}
	if navigation.Events[0].Name != "First Event" {
		t.Fatalf("Event name = %q, want First Event", navigation.Events[0].Name)
	}
	if got := backstageSectionLabels(navigation.Events[0]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Event settings",
		"Event Displays",
		"Sessions and Competitions",
		"Plan and publish",
		"Event Theme",
		"Voting Keys",
		"Live operations",
		"Program Output and Overrides",
		"Emergency Alerts",
		"Results and Prizegiving",
		"Final Files Export",
	}) {
		t.Fatalf("Producer sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[1]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Sessions and Competitions",
		"Live operations",
		"Program Output and Overrides",
		"Emergency Alerts",
		"Results and Prizegiving",
		"Final Files Export",
	}) {
		t.Fatalf("scoped Operator sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[2]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Sessions and Competitions",
		"Final Files Export",
	}) {
		t.Fatalf("Observer sections = %v", got)
	}
	if got := backstageSectionLabels(navigation.Events[3]); !reflect.DeepEqual(got, []string{
		"Event overview",
		"Sessions and Competitions",
		"Live operations",
		"Program Output and Overrides",
		"Emergency Alerts",
		"Final Files Export",
	}) {
		t.Fatalf("capability-only Operator sections = %v", got)
	}
	for _, section := range navigation.Events[3].Sections[2:5] {
		if section.UnavailableReason == "" || section.Href != "" {
			t.Errorf("blocked section = %+v, want reason without link", section)
		}
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
	if navigation := backstageNavigation(auth.Account{}, ""); navigation.Administrator ||
		len(navigation.Events) != 0 {
		t.Fatalf("attendee navigation = %+v", navigation)
	}

	routes := newRouteMux()
	if err := registerFrontendRoutes(routes, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("register Frontend routes: %v", err)
	}
	registerPlanningRoutes(routes, nil, nil, nil, nil, nil, nil, nil)
	registerAdministrationRoutes(routes, nil, nil, nil, nil, nil)
	registerOperationRoutes(routes, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	registerControlRoutes(routes, nil, nil, nil, nil, nil, "", nil)
	registerVotingRoutes(routes, nil, nil, nil, nil, nil, nil)
	for path, want := range map[string]interfaceKind{
		"/profile":                                     publicInterface,
		"/backstage":                                   crewInterface,
		"/backstage/administration":                    crewInterface,
		"/admin/registration":                          crewInterface,
		"/backstage/events/1":                          crewInterface,
		"/backstage/events/1/sessions":                 crewInterface,
		"/backstage/events/1/settings":                 crewInterface,
		"/backstage/events/1/planning":                 crewInterface,
		"/backstage/events/1/operations":               crewInterface,
		"/backstage/events/1/control":                  crewInterface,
		"/backstage/events/1/control/emergency-alerts": crewInterface,
		"/backstage/events/1/voting-keys":              crewInterface,
		"/voting":                                      publicInterface,
		"/backstage/events/new":                        crewInterface,
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

func TestBackstageControlNavigationUsesDedicatedRoute(t *testing.T) {
	navigation := backstageNavigation(auth.Account{
		EventRoles: map[int]viewer.Role{7: viewer.Producer},
	}, "")
	for _, section := range navigation.Events[0].Sections {
		if section.Label == "Program Output and Overrides" {
			if section.Href != "/backstage/events/7/control" {
				t.Fatalf("control href = %q", section.Href)
			}
			return
		}
	}
	t.Fatal("Program Output and Overrides navigation missing")
}

func TestBackstageNavigationLinksOwningWorkflows(t *testing.T) {
	navigation := backstageNavigation(auth.Account{
		EventRoles: map[int]viewer.Role{7: viewer.Producer},
	}, "")
	want := map[string]string{
		"Sessions and Competitions":    "/backstage/events/7/sessions",
		"Plan and publish":             "/backstage/events/7/planning",
		"Live operations":              "/backstage/events/7/operations",
		"Program Output and Overrides": "/backstage/events/7/control",
		"Emergency Alerts":             "/backstage/events/7/control/emergency-alerts#emergency-alerts",
		"Results and Prizegiving":      "/backstage/events/7/results",
	}
	for _, section := range navigation.Events[0].Sections {
		href, ok := want[section.Label]
		if !ok {
			continue
		}
		if section.Href != href {
			t.Errorf("%s href = %q, want %q", section.Label, section.Href, href)
		}
		delete(want, section.Label)
	}
	if len(want) != 0 {
		t.Fatalf("missing workflow links: %v", want)
	}
}

func TestBackstageNavigationRendersEquivalentActiveViewsAndBreadcrumbs(t *testing.T) {
	navigation := backstageNavigation(auth.Account{
		EventRoles: map[int]viewer.Role{7: viewer.Producer},
		EventNames: map[int]string{7: "Revision"},
	}, "/backstage/events/7/results")

	var rendered bytes.Buffer
	if err := frontend.BackstageNavigationViews(navigation).Render(
		t.Context(),
		&rendered,
	); err != nil {
		t.Fatalf("render Backstage navigation: %v", err)
	}
	html := rendered.String()
	if got := strings.Count(html, `href="/backstage/events/7/results"`); got != 2 {
		t.Fatalf("Results links = %d, want mobile and desktop", got)
	}
	if got := strings.Count(html, `aria-current="page"`); got != 3 {
		t.Fatalf("current markers = %d, want mobile, desktop, and breadcrumb", got)
	}
	for _, label := range []string{"Revision", "Producer", "Backstage", "Results and Prizegiving"} {
		if !strings.Contains(html, label) {
			t.Errorf("rendered navigation missing %q", label)
		}
	}
}

func TestBackstageEmergencyNavigationMarksEmergencyTaskCurrent(t *testing.T) {
	navigation := backstageNavigation(auth.Account{
		EventRoles: map[int]viewer.Role{7: viewer.Producer},
		EventNames: map[int]string{7: "Revision"},
	}, "/backstage/events/7/control/emergency-alerts")

	for _, section := range navigation.Events[0].Sections {
		if section.Label == "Emergency Alerts" && section.Current {
			if got := navigation.Breadcrumbs[len(navigation.Breadcrumbs)-1].Label; got != section.Label {
				t.Fatalf("current breadcrumb = %q, want %q", got, section.Label)
			}
			return
		}
	}
	t.Fatal("Emergency Alerts task is not current")
}

func TestBackstageControlHidesUnavailableActions(t *testing.T) {
	for _, test := range []struct {
		name         string
		canControl   bool
		canEmergency bool
	}{
		{name: "unscoped"},
		{name: "scoped", canControl: true},
		{name: "emergency", canControl: true, canEmergency: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := frontend.Control(frontend.ControlPage{
				CanControl:        test.canControl,
				CanEmergencyAlert: test.canEmergency,
			}).Render(t.Context(), &rendered); err != nil {
				t.Fatalf("render controls: %v", err)
			}
			html := rendered.String()
			if got := strings.Contains(html, "Preview Stage Message"); got != test.canControl {
				t.Fatalf("Override controls visible = %t, want %t", got, test.canControl)
			}
			if got := strings.Contains(html, `id="emergency-alerts"`); got != test.canEmergency {
				t.Fatalf(
					"Emergency Alert controls visible = %t, want %t",
					got,
					test.canEmergency,
				)
			}
			if got := strings.Contains(
				html,
				"No Lane or Display Group scope assigned.",
			); got == test.canControl {
				t.Fatalf("blocked reason visible = %t, can control = %t", got, test.canControl)
			}
		})
	}
}

func TestBackstagePlanningUsesNativeTaskDisclosure(t *testing.T) {
	var rendered bytes.Buffer
	if err := frontend.Planning(frontend.PlanningPage{}).Render(
		t.Context(),
		&rendered,
	); err != nil {
		t.Fatalf("render planning: %v", err)
	}
	html := rendered.String()
	for _, task := range []string{
		"Edit Draft structure",
		"CSV import",
		"iCalendar import",
	} {
		if !strings.Contains(html, "<summary>"+task+"</summary>") {
			t.Errorf("%s is not a native disclosure", task)
		}
	}
}

func TestBackstageSessionsLinksActualCompetitionWorkflowByRole(t *testing.T) {
	session := rundown.CrewSession{
		ID:    9,
		Title: "Demo Competition",
		Type:  rundown.SessionCompetition,
	}
	for _, test := range []struct {
		name      string
		canManage bool
	}{
		{name: "Observer"},
		{name: "Producer", canManage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := frontend.BackstageSessions(frontend.BackstageSessionsPage{
				Event:    events.Event{ID: 7},
				Sessions: []rundown.CrewSession{session},
				Manage:   map[int]bool{9: test.canManage},
			}).Render(t.Context(), &rendered); err != nil {
				t.Fatalf("render Sessions and Competitions: %v", err)
			}
			html := rendered.String()
			if !strings.Contains(html, "Demo Competition") {
				t.Fatal("Competition name missing")
			}
			hasWorkflow := strings.Contains(
				html,
				`href="/backstage/events/7/competitions/9/entries"`,
			)
			if hasWorkflow != test.canManage {
				t.Fatalf(
					"Competition workflow visible = %t, want %t",
					hasWorkflow,
					test.canManage,
				)
			}
		})
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
