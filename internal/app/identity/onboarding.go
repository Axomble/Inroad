package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// ErrWorkspaceNotFound is returned by CompleteOnboarding when no workspace has
// the given id. The handler maps it to 404.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// Onboarding is a workspace's post-signup onboarding state.
type Onboarding struct {
	WorkspaceID uuid.UUID
	Name        string
	CompletedAt time.Time // zero when onboarding is still pending
}

// CompleteOnboarding names a workspace and marks its onboarding finished. The
// rename and the stamp happen in ONE statement (Store.CompleteOnboarding), so
// there is no window in which a workspace could be renamed but left unstamped, or
// the reverse.
//
// Idempotent: calling it on an already-completed workspace returns the existing
// row untouched rather than renaming. A completed onboarding is a one-time event,
// and a replayed request (a double-click, a retried fetch) must not silently
// overwrite a name the workspace has since been given deliberately.
//
// ws comes from the caller's JWT, never a path or body value — the handler only
// checks the path segment for agreement (see pathWorkspaceID).
func (s *Service) CompleteOnboarding(ctx context.Context, ws uuid.UUID, name string) (Onboarding, error) {
	row, err := s.store.CompleteOnboarding(ctx, ws, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Onboarding{}, ErrWorkspaceNotFound
	}
	if err != nil {
		return Onboarding{}, err
	}
	return Onboarding{
		WorkspaceID: row.ID,
		Name:        row.Name,
		CompletedAt: pgxTime(row.OnboardingCompletedAt),
	}, nil
}

// onboardingDTO mirrors the shape the auth responses use: the stamp alone, no
// separate boolean. In practice a successful response always carries it, since
// completing onboarding is what this endpoint does.
type onboardingDTO struct {
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	CompletedAt *string `json:"onboarding_completed_at"`
}

// completeOnboarding names the caller's workspace and marks onboarding done.
// RequireRole("admin") gates the route (owners rank above admins), and
// pathWorkspaceID pins the write to the workspace in the caller's token.
func (h *Handler) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	wsID, ok := pathWorkspaceID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Checked by hand rather than through validate.Struct: this message is rendered
	// verbatim next to the input the user just typed, and the tag-keyed form
	// ("validation failed: Name: max") is not something to show a person. The RULE is
	// the same one register applies to workspace_name, so a name accepted here is one
	// signup would also have accepted.
	name := strings.TrimSpace(body.Name)
	switch {
	case name == "":
		httpx.Error(w, http.StatusBadRequest, "Enter a name for your workspace.")
		return
	case len([]rune(name)) > maxWorkspaceNameLen:
		httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("That name is too long — please use %d characters or fewer.", maxWorkspaceNameLen))
		return
	}
	res, err := h.svc.CompleteOnboarding(r.Context(), wsID, name)
	if err != nil {
		if errors.Is(err, ErrWorkspaceNotFound) {
			httpx.Error(w, http.StatusNotFound, "workspace not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not complete onboarding")
		return
	}
	httpx.JSON(w, http.StatusOK, onboardingDTO{
		WorkspaceID: res.WorkspaceID.String(), Name: res.Name,
		CompletedAt: rfc3339OrNil(res.CompletedAt),
	})
}
