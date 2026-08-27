package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/platform/warmup"
)

func advertisement(p Participant, scope Scope) Advertisement {
	return Advertisement{Participant: p, Scope: scope}
}

func TestLocalCoordinatorAdmission(t *testing.T) {
	sentinelOnWatch := participant("ws-1", "mb-s", warmup.LaneWatch)
	sentinelOnWatch.IsSentinel = true

	tests := []struct {
		name string
		ad   Advertisement
		want Admission
	}{
		{
			name: "a healthy mailbox joins its own workspace pool",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LaneHealthy), ScopeWorkspace),
			want: Admission{Admitted: true, ReasonCode: AdmissionOK},
		},
		{
			// A degrading mailbox is still a pool member: it may only pair
			// within its lane, and that containment is warmup.Pairable's job,
			// not admission's.
			name: "a watched mailbox is still admitted",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LaneWatch), ScopeWorkspace),
			want: Admission{Admitted: true, ReasonCode: AdmissionOK},
		},
		{
			name: "a sentinel is admitted like any other member",
			ad:   advertisement(sentinelOnWatch, ScopeWorkspace),
			want: Admission{Admitted: true, ReasonCode: AdmissionOK},
		},
		{
			name: "a quarantined mailbox is refused",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LaneQuarantine), ScopeWorkspace),
			want: Admission{Admitted: false, ReasonCode: AdmissionLaneMayNotSend},
		},
		{
			name: "an unauthenticated mailbox is refused",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LanePendingAuth), ScopeWorkspace),
			want: Admission{Admitted: false, ReasonCode: AdmissionLaneMayNotSend},
		},
		{
			// The whole of Phase 3 is unbuilt. Consenting to a shared pool must
			// not quietly resolve to the local one, or an operator believes a
			// membership is live that nothing implements.
			name: "a shared-pool advertisement is refused, not downgraded",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LaneHealthy), ScopeShared),
			want: Admission{Admitted: false, ReasonCode: AdmissionSharedPoolUnavailable},
		},
		{
			// Which refusal comes first matters: the pool this participant asked
			// for does not exist, and reporting its lane instead would suggest a
			// healthier mailbox would have got in.
			name: "the missing shared pool outranks the lane refusal",
			ad:   advertisement(participant("ws-1", "mb-a", warmup.LaneQuarantine), ScopeShared),
			want: Admission{Admitted: false, ReasonCode: AdmissionSharedPoolUnavailable},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewLocal(&fakePool{}).Advertise(context.Background(), tt.ad)
			if err != nil {
				t.Fatalf("Advertise: %v", err)
			}
			if got != tt.want {
				t.Errorf("Advertise = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLocalCoordinatorRefusesAMalformedAdvertisement(t *testing.T) {
	tests := []struct {
		name string
		ad   Advertisement
	}{
		{"no workspace", advertisement(participant("", "mb-a", warmup.LaneHealthy), ScopeWorkspace)},
		{"no participant id", advertisement(participant("ws-1", "", warmup.LaneHealthy), ScopeWorkspace)},
		{"no lane", advertisement(participant("ws-1", "mb-a", ""), ScopeWorkspace)},
		// No default scope. Consent to a shared pool is the one thing §14 forbids
		// happening by omission, so it must be stated rather than inferred from a
		// zero value that a later refactor could redefine.
		{"no scope", advertisement(participant("ws-1", "mb-a", warmup.LaneHealthy), "")},
		{"an unrecognized scope", advertisement(participant("ws-1", "mb-a", warmup.LaneHealthy), Scope("federated"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewLocal(&fakePool{}).Advertise(context.Background(), tt.ad)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
			if got != (Admission{}) {
				t.Errorf("a rejected advertisement returned %+v, want the zero admission", got)
			}
		})
	}
}
