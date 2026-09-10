package worker

import "testing"

func TestParseRoleDefaultsToAllSoSelfHostSeesNoNewConcept(t *testing.T) {
	got, err := ParseRole("")
	if err != nil {
		t.Fatalf("ParseRole(%q): %v", "", err)
	}
	if got != RoleAll {
		t.Errorf("ParseRole(\"\") = %q, want %q — an unset role must mean today's single-process behaviour", got, RoleAll)
	}
}

func TestParseRoleAcceptsTheThreeRoles(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Role
	}{
		{"all", RoleAll},
		{"control", RoleControl},
		{"send", RoleSend},
		{"CONTROL", RoleControl}, // operators type env vars by hand
	} {
		got, err := ParseRole(tc.in)
		if err != nil {
			t.Fatalf("ParseRole(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRoleRejectsAnUnknownValueRatherThanDefaulting(t *testing.T) {
	// A typo must not silently degrade to "all": a host the operator believed
	// was send-only would quietly run every sweep, which is the exact exposure
	// the split exists to prevent.
	if _, err := ParseRole("sned"); err == nil {
		t.Fatal("ParseRole(\"sned\") = nil error, want an error naming the valid values")
	}
}

func TestRolePredicates(t *testing.T) {
	for _, tc := range []struct {
		role                  Role
		scheduled, perMessage bool
	}{
		{RoleAll, true, true},
		{RoleControl, true, false},
		{RoleSend, false, true},
		// The ZERO VALUE, which is neither RoleAll nor any parsed role: a Deps
		// literal that omits Role, or any other struct field of type Role left
		// unset. It must mean "everything", the same rule ParseRole applies to an
		// unset INROAD_WORKER_ROLE. The predicates own that normalisation because
		// they are the only place all three gates — registration, the scheduler
		// and the heartbeat — agree by construction; when Register normalised its
		// own copy instead, the two gates in cmd/worker did not, and a zero-value
		// role registered every handler while scheduling nothing and never
		// heartbeating.
		{Role(""), true, true},
	} {
		if got := tc.role.RunsScheduledWork(); got != tc.scheduled {
			t.Errorf("%q.RunsScheduledWork() = %v, want %v", tc.role, got, tc.scheduled)
		}
		if got := tc.role.RunsPerMessageWork(); got != tc.perMessage {
			t.Errorf("%q.RunsPerMessageWork() = %v, want %v", tc.role, got, tc.perMessage)
		}
	}
}
