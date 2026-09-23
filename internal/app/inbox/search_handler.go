package inbox

import (
	"errors"
	"net/http"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/cursor"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// highlightSegmentResponse is one run of snippet text — the
// InboxSearchHighlightSegment schema. Plain text, never HTML: a client renders
// a matched run by wrapping it (e.g. <mark>), with the text itself escaped.
type highlightSegmentResponse struct {
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

// searchSnippetResponse is the InboxSearchSnippet schema.
type searchSnippetResponse struct {
	Direction  string                     `json:"direction"`
	OccurredAt string                     `json:"occurred_at"`
	Subject    []highlightSegmentResponse `json:"subject"`
	Body       []highlightSegmentResponse `json:"body"`
}

// searchHitResponse is the InboxSearchHit schema: the thread exactly as the
// list endpoint renders it, plus why it matched.
type searchHitResponse struct {
	Thread      threadSummaryResponse  `json:"thread"`
	MatchedLegs []string               `json:"matched_legs"`
	Snippet     *searchSnippetResponse `json:"snippet"`
}

// searchPageResponse is GET /inbox/search — the InboxSearchPage schema.
type searchPageResponse struct {
	Items      []searchHitResponse `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

// The names matched_legs reports, in this fixed order. inbound/outbound are
// the legs whose TEXT matched; contact means the query matched an address —
// the thread's contact's email or an inbound message's From address.
const (
	legInbound  = "inbound"
	legOutbound = "outbound"
	legContact  = "contact"
)

// search handles GET /inbox/search.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	filter, err := parseSearchFilter(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := h.svc.SearchThreads(r.Context(), wid, filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSearchPageResponse(page))
}

// parseSearchFilter reads ?q= plus every facet and scope the thread list
// accepts, and the opaque ?cursor=. The list's own ?q= substring match and its
// raw before_* keyset are deliberately not accepted here: this endpoint has one
// query and one cursor, and a second of each would only invite the two to
// disagree.
func parseSearchFilter(r *http.Request) (SearchFilter, error) {
	q := r.URL.Query()
	facets, err := parseFacets(q)
	if err != nil {
		return SearchFilter{}, err
	}
	filter := SearchFilter{ListFilter: facets, Text: q.Get("q")}
	if raw := q.Get("cursor"); raw != "" {
		c, err := cursor.Decode(raw, cursor.SortNewest)
		// Forward-only: a search page links to the next one, never back, so a
		// "before" cursor is not one this endpoint minted.
		if err != nil || c.Direction != cursor.After {
			return SearchFilter{}, errors.New("cursor is malformed or was not issued by this endpoint")
		}
		// The cursor package's time key is named for the contact sort that
		// introduced it; here it carries the row's last_message_at.
		at, id := c.CreatedAt, c.ID
		filter.BeforeLastMessageAt, filter.BeforeID = &at, &id
	}
	if err := applyScope(&filter.ListFilter, q.Get("scope"), true, r); err != nil {
		return SearchFilter{}, err
	}
	return filter, nil
}

func toSearchPageResponse(page SearchPage) searchPageResponse {
	out := searchPageResponse{Items: make([]searchHitResponse, 0, len(page.Items))}
	for _, hit := range page.Items {
		out.Items = append(out.Items, toSearchHitResponse(hit))
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1].Thread
		next := cursor.NewTime(cursor.SortNewest, cursor.After, last.LastMessageAt, last.ID).Encode()
		out.NextCursor = &next
	}
	return out
}

func toSearchHitResponse(hit SearchHit) searchHitResponse {
	legs := make([]string, 0, 3)
	if hit.MatchedInbound {
		legs = append(legs, legInbound)
	}
	if hit.MatchedOutbound {
		legs = append(legs, legOutbound)
	}
	if hit.MatchedAddress {
		legs = append(legs, legContact)
	}
	out := searchHitResponse{Thread: toThreadSummaryResponse(hit.Thread), MatchedLegs: legs}
	if s := hit.Snippet; s != nil {
		out.Snippet = &searchSnippetResponse{
			Direction:  s.Direction,
			OccurredAt: s.OccurredAt.UTC().Format(time.RFC3339),
			Subject:    toHighlightSegmentResponses(s.Subject),
			Body:       toHighlightSegmentResponses(s.Body),
		}
	}
	return out
}

// toHighlightSegmentResponses always returns a non-nil slice, so an empty body
// serializes as [] and a client can map over it unconditionally.
func toHighlightSegmentResponses(segments []HighlightSegment) []highlightSegmentResponse {
	out := make([]highlightSegmentResponse, 0, len(segments))
	for _, s := range segments {
		out = append(out, highlightSegmentResponse(s))
	}
	return out
}
