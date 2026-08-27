package coordinator

import (
	"reflect"
	"testing"
)

// The payload types are pinned field by field.
//
// This is not a test of the struct definitions for their own sake. These five
// types ARE the tenancy boundary: an Advertisement is published about every member
// of a pool to every peer for as long as the membership lasts, and an Assignment
// discloses one peer to one sender for the life of one lease. Every field added to
// either is a disclosure, and the pressure to add one always arrives with a good
// local reason — allocation would be smarter with the sender's route coverage, the
// From: header would look more natural with the partner's display name.
//
// So widening has to be deliberate rather than incidental. Adding a field fails
// here, and the failure names the document that says which fields may exist and
// why each one is necessary. Deleting a field fails here too, which is the same
// service in the other direction.
func TestPayloadShapesAreDeliberate(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  []string
	}{
		{
			name:  "Participant",
			value: Participant{},
			want:  []string{"WorkspaceID string", "ID string", "Lane string", "IsSentinel bool"},
		},
		{
			name:  "Advertisement",
			value: Advertisement{},
			want:  []string{"Participant coordinator.Participant", "Scope coordinator.Scope"},
		},
		{
			name:  "Partner",
			value: Partner{},
			want:  []string{"ID string", "Address string"},
		},
		{
			name:  "Lease",
			value: Lease{},
			want:  []string{"ID string", "Terms warmup.Lease"},
		},
		{
			name:  "Assignment",
			value: Assignment{},
			want:  []string{"Partner coordinator.Partner", "Lease coordinator.Lease"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ := reflect.TypeOf(tt.value)
			got := make([]string, 0, typ.NumField())
			for i := range typ.NumField() {
				f := typ.Field(i)
				got = append(got, f.Name+" "+f.Type.String())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s is now %v, was %v.\n"+
					"These fields cross a tenancy boundary. Before changing this list, read "+
					"docs/superpowers/specs/2026-08-27-warmup-coordinator-seam-design.md §4 "+
					"(minimum routing data) and record why the new field is necessary.",
					tt.name, got, tt.want)
			}
		})
	}
}
