package authz_test

import (
	"errors"
	"testing"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const testEventID = 7

func producer() viewer.Identity {
	return viewer.Identity{
		AccountID:  1,
		EventRoles: map[int]viewer.Role{testEventID: viewer.Producer},
	}
}

func operator(scope viewer.EventScope) viewer.Identity {
	return viewer.Identity{
		AccountID:   2,
		EventRoles:  map[int]viewer.Role{testEventID: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{testEventID: scope},
	}
}

func observer(capabilities ...viewer.Capability) viewer.Identity {
	granted := make(map[viewer.Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		granted[capability] = struct{}{}
	}
	return viewer.Identity{
		AccountID:   3,
		EventRoles:  map[int]viewer.Role{testEventID: viewer.Observer},
		EventScopes: map[int]viewer.EventScope{testEventID: {Capabilities: granted}},
	}
}

func administrator() viewer.Identity {
	return viewer.Identity{AccountID: 4, Administrator: true}
}

func TestEvaluateExpandsRolesToCapabilities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		request  authz.Request
		wantCode string
	}{
		{
			name: "administrator holds installation capabilities",
			request: authz.Request{
				Identity: administrator(), Authenticated: true,
				Action: "CreateEvent", Facts: authz.Installation(),
			},
		},
		{
			name: "producer without the administrator flag does not",
			request: authz.Request{
				Identity: producer(), Authenticated: true,
				Action: "CreateEvent", Facts: authz.Installation(),
			},
			wantCode: "administrator_required",
		},
		{
			name: "producer holds every Event capability",
			request: authz.Request{
				Identity: producer(), Authenticated: true,
				Action: "Publish", Facts: authz.Event(testEventID),
			},
		},
		{
			name: "operator does not hold producer capabilities",
			request: authz.Request{
				Identity: operator(viewer.EventScope{}), Authenticated: true,
				Action: "Publish", Facts: authz.Event(testEventID),
			},
			wantCode: "event_access_denied",
		},
		{
			name: "operator holds the scoped operation capabilities",
			request: authz.Request{
				Identity:      operator(viewer.EventScope{LaneIDs: map[int]struct{}{4: {}}}),
				Authenticated: true, Action: "StartSession",
				Facts: authz.Lanes(testEventID, []int{4}),
			},
		},
		{
			name: "observer holds no operation capability",
			request: authz.Request{
				Identity: observer(), Authenticated: true,
				Action: "StartSession", Facts: authz.Lanes(testEventID, []int{4}),
			},
			wantCode: "operator_required",
		},
		{
			name: "observer holds a granted ViewResults but not ManageResults",
			request: authz.Request{
				Identity: observer(viewer.ViewResults), Authenticated: true,
				Action: "SaveEventAwardsDraft", Facts: authz.Event(testEventID),
			},
			wantCode: "manage_results_required",
		},
		{
			name: "operator holds an explicitly granted capability",
			request: authz.Request{
				Identity: operator(viewer.EventScope{
					DisplayGroupKeys: map[string]struct{}{"stage": {}},
					Capabilities:     map[viewer.Capability]struct{}{viewer.EmergencyAlert: {}},
				}),
				Authenticated: true, Action: "ActivateEmergencyAlert",
				Facts: authz.DisplayGroups(testEventID, []string{"stage"}),
			},
		},
		{
			name: "operator without the grant is refused the emergency surface",
			request: authz.Request{
				Identity: operator(viewer.EventScope{
					DisplayGroupKeys: map[string]struct{}{"stage": {}},
				}),
				Authenticated: true, Action: "ActivateEmergencyAlert",
				Facts: authz.DisplayGroups(testEventID, []string{"stage"}),
			},
			wantCode: "override_scope_denied",
		},
		{
			name: "an action whose rule is ownership admits an unauthenticated caller",
			request: authz.Request{
				Action: "RecoverAccount", Facts: authz.Installation(),
			},
		},
		{
			name: "an action needing a capability refuses an unauthenticated caller",
			request: authz.Request{
				Action: "Publish", Facts: authz.Event(testEventID),
			},
			wantCode: "event_access_denied",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertRefusal(t, authz.Evaluate(testCase.request), testCase.wantCode)
		})
	}
}

func TestEvaluateAppliesScopeDimensions(t *testing.T) {
	t.Parallel()

	laneOperator := operator(viewer.EventScope{LaneIDs: map[int]struct{}{4: {}}})
	groupOperator := operator(viewer.EventScope{DisplayGroupKeys: map[string]struct{}{"stage": {}}})

	cases := []struct {
		name     string
		request  authz.Request
		wantCode string
	}{
		{
			name: "every Lane of the target must be granted",
			request: authz.Request{
				Identity: laneOperator, Authenticated: true, Action: "EndSession",
				Facts: authz.Lanes(testEventID, []int{4, 5}),
			},
			wantCode: "session_scope_required",
		},
		{
			name: "a target with no Lanes refuses rather than allows",
			request: authz.Request{
				Identity: laneOperator, Authenticated: true, Action: "EndSession",
				Facts: authz.Lanes(testEventID, nil),
			},
			wantCode: "session_scope_required",
		},
		{
			name: "a Producer passes a Lane-scoped row whatever the target",
			request: authz.Request{
				Identity: producer(), Authenticated: true, Action: "EndSession",
				Facts: authz.Lanes(testEventID, nil),
			},
		},
		{
			name: "an Override outside the granted Display Groups refuses",
			request: authz.Request{
				Identity: groupOperator, Authenticated: true, Action: "SendStageMessage",
				Facts: authz.DisplayGroups(testEventID, []string{"foyer"}),
			},
			wantCode: "override_scope_denied",
		},
		{
			name: "a Lane-typed Override target is judged by Lane grant",
			request: authz.Request{
				Identity: laneOperator, Authenticated: true, Action: "ActivateUrgentNotice",
				Facts: authz.DisplayGroupsOfLane(testEventID, 4),
			},
		},
		{
			name: "an Event-wide row needs an Event",
			request: authz.Request{
				Identity: producer(), Authenticated: true, Action: "Publish",
				Facts: authz.Event(0),
			},
			wantCode: "event_access_denied",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertRefusal(t, authz.Evaluate(testCase.request), testCase.wantCode)
		})
	}
}

func TestEvaluateFailsClosedOnBadDeclarations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request authz.Request
		want    error
	}{
		{
			name:    "an action with no row",
			request: authz.Request{Action: "InventNewCommand", Facts: authz.Installation()},
			want:    authz.ErrUnknownAction,
		},
		{
			name: "a scoped row evaluated with no facts at all",
			request: authz.Request{
				Identity: producer(), Authenticated: true, Action: "StartSession",
			},
			want: authz.ErrScopeFactsMismatch,
		},
		{
			name: "a Lane-scoped row handed Event-wide facts",
			request: authz.Request{
				Identity: producer(), Authenticated: true, Action: "StartSession",
				Facts: authz.Event(testEventID),
			},
			want: authz.ErrScopeFactsMismatch,
		},
		{
			name: "a plan demanding a capability its row does not declare",
			request: authz.Request{
				Identity: producer(), Authenticated: true, Action: "SendStageMessage",
				Facts: authz.DisplayGroups(testEventID, []string{"stage"}).
					Demanding(authz.EmergencyAlert),
			},
			want: authz.ErrUndeclaredDemand,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := authz.Evaluate(testCase.request); !errors.Is(err, testCase.want) {
				t.Fatalf("Evaluate = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestEvaluateAppliesTargetDemandedCapabilities(t *testing.T) {
	t.Parallel()

	scoped := operator(viewer.EventScope{DisplayGroupKeys: map[string]struct{}{"stage": {}}})
	facts := authz.DisplayGroups(testEventID, []string{"stage"})

	if err := authz.Evaluate(authz.Request{
		Identity: scoped, Authenticated: true, Action: "ClearDisplayOverride", Facts: facts,
	}); err != nil {
		t.Fatalf("clearing an ordinary Override: %v", err)
	}

	assertRefusal(t, authz.Evaluate(authz.Request{
		Identity: scoped, Authenticated: true, Action: "ClearDisplayOverride",
		Facts: facts.Demanding(authz.EmergencyAlert),
	}), "override_scope_denied")
}

func assertRefusal(t *testing.T, err error, wantCode string) {
	t.Helper()

	var refusal *authz.RefusalError
	switch {
	case wantCode == "" && err != nil:
		t.Fatalf("Evaluate = %v, want admitted", err)
	case wantCode == "":
	case !errors.As(err, &refusal):
		t.Fatalf("Evaluate = %v, want a refusal coded %s", err, wantCode)
	case refusal.Code != wantCode:
		t.Fatalf("refusal code = %s, want %s", refusal.Code, wantCode)
	}
}
