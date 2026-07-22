package viewer

import "testing"

func TestEventScopeAuthorityFailsClosed(t *testing.T) {
	identity := Identity{
		EventRoles: map[int]Role{1: Operator, 2: Observer, 3: Producer, 5: Observer},
		EventScopes: map[int]EventScope{
			1: {
				LaneIDs:          map[int]struct{}{10: {}},
				DisplayGroupKeys: map[string]struct{}{"stage:left": {}},
				Capabilities:     map[Capability]struct{}{EmergencyAlert: {}},
			},
			2: {Capabilities: map[Capability]struct{}{ViewResults: {}}},
			5: {Capabilities: map[Capability]struct{}{EmergencyAlert: {}, ManageResults: {}}},
		},
	}

	checks := []struct {
		name string
		got  bool
		want bool
	}{
		{name: "Operator assigned Lane", got: identity.CanOperateLane(1, 10), want: true},
		{name: "Operator other Lane", got: identity.CanOperateLane(1, 11), want: false},
		{name: "Operator assigned Display Group", got: identity.CanOperateDisplayGroup(1, "stage:left"), want: true},
		{name: "Operator other Display Group", got: identity.CanOperateDisplayGroup(1, "stage:right"), want: false},
		{name: "Operator assigned capability", got: identity.HasCapability(1, EmergencyAlert), want: true},
		{name: "Operator other capability", got: identity.HasCapability(1, ManageResults), want: false},
		{name: "Observer read capability", got: identity.HasCapability(2, ViewResults), want: true},
		{name: "Observer Emergency Alert rejected", got: identity.HasCapability(5, EmergencyAlert), want: false},
		{name: "Observer Manage Results rejected", got: identity.HasCapability(5, ManageResults), want: false},
		{name: "Observer live control", got: identity.CanOperateLane(2, 10), want: false},
		{name: "Producer any Lane", got: identity.CanOperateLane(3, 99), want: true},
		{name: "Producer any Display Group", got: identity.CanOperateDisplayGroup(3, "any"), want: true},
		{name: "Producer any capability", got: identity.HasCapability(3, ManageResults), want: true},
		{name: "Producer unknown capability", got: identity.HasCapability(3, Capability("Unknown")), want: false},
		{name: "Producer invalid Lane", got: identity.CanOperateLane(3, 0), want: false},
		{name: "Producer empty Display Group", got: identity.CanOperateDisplayGroup(3, ""), want: false},
		{name: "missing Event Grant", got: identity.HasCapability(4, ViewResults), want: false},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if check.got != check.want {
				t.Errorf("authority = %t, want %t", check.got, check.want)
			}
		})
	}
}
