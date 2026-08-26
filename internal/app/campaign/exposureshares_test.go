package campaign

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// The exposure budget is a traffic limit paired with current usage. The selector
// enforces the limit; these cover the usage half — what the operator can see next to
// it, without which the number is one nobody can act on.

// poolOf builds a fake store holding one campaign and the given pool members.
func poolOf(ws, id uuid.UUID, senders ...Sender) *fakeStore {
	return &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{
			{ws, id}: {ID: id, WorkspaceID: ws, RotationMode: rotation.ModeWeighted},
		},
		senders: senders,
	}
}

// member is one pool row with only the fields concentration is computed from.
func member(email string, assigned int64) Sender {
	return Sender{MailboxID: uuid.New(), Email: email, Weight: 1, Enabled: true, AssignedCount: assigned}
}

// sharesByDomain indexes a reported distribution for assertions that do not care
// about ordering.
func sharesByDomain(shares []rotation.FaultDomainShare) map[string]rotation.FaultDomainShare {
	out := make(map[string]rotation.FaultDomainShare, len(shares))
	for _, s := range shares {
		out[s.Domain] = s
	}
	return out
}

// The headline number: how much of this campaign rests on each thing that can fail
// all at once, worst first, with the one that is over the limit flagged.
func TestGetSendersReportsFaultDomainConcentration(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id,
		member("a@acme.test", 60),
		member("b@mail.acme.test", 8), // same organizational domain, same fault
		member("c@other.test", 32),
	)

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(pool.FaultDomainShares) != 2 {
		t.Fatalf("shares = %+v, want two domains: the subdomain shares acme.test's fate", pool.FaultDomainShares)
	}
	if got := pool.FaultDomainShares[0].Domain; got != "acme.test" {
		t.Errorf("first reported domain = %q, want acme.test — worst first, so the operator reads the "+
			"risk before the detail", got)
	}
	byDomain := sharesByDomain(pool.FaultDomainShares)
	if got := byDomain["acme.test"]; got.Assigned != 68 || got.Share != 0.68 || !got.OverBudget {
		t.Errorf("acme.test = %+v, want 68 assignments at 0.68, over budget", got)
	}
	if got := byDomain["other.test"]; got.Assigned != 32 || got.Share != 0.32 || got.OverBudget {
		t.Errorf("other.test = %+v, want 32 assignments at 0.32, within budget", got)
	}
}

// The limit travels with the usage. Without it a client has to hard-code 0.6, and
// the number it renders drifts the moment the constant moves.
func TestGetSendersReportsTheLimitTheSharesAreMeasuredAgainst(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("a@acme.test", 1))

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if pool.MaxFaultDomainShare != rotation.MaxFaultDomainShare {
		t.Errorf("limit = %v, want rotation.MaxFaultDomainShare %v",
			pool.MaxFaultDomainShare, rotation.MaxFaultDomainShare)
	}
}

// The single-domain workspace: selection is deliberately unchanged there, but the
// concentration is real and must still be VISIBLE. Reporting only what the selector
// acts on would hide the one case where an operator most needs to be told to buy a
// second domain.
func TestGetSendersReportsConcentrationItCannotActOn(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("a@acme.test", 90), member("b@acme.test", 10))

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(pool.FaultDomainShares) != 1 {
		t.Fatalf("shares = %+v, want the one domain", pool.FaultDomainShares)
	}
	got := pool.FaultDomainShares[0]
	if got.Domain != "acme.test" || got.Share != 1 || !got.OverBudget {
		t.Errorf("share = %+v, want acme.test at 1.0 and over budget: the whole campaign rests on it, "+
			"and the selector's refusal to act on that is not a reason to hide it", got)
	}
}

// Two strangers on gmail.com are not one fault domain — the same rule the selector
// groups by, so the panel cannot show a concentration the rotation does not act on
// or vice versa.
func TestGetSendersDoesNotReportConsumerProvidersAsAFaultDomain(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("alice@gmail.com", 90), member("b@acme.test", 10))

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	byDomain := sharesByDomain(pool.FaultDomainShares)
	if _, reported := byDomain["gmail.com"]; reported {
		t.Errorf("shares = %+v, want no gmail.com row: Google does not fail alice because bob sent spam, "+
			"and the selector does not group them either", pool.FaultDomainShares)
	}
	// The real domain is still reported, so this cannot pass by reporting nothing.
	if got := byDomain["acme.test"]; got.Assigned != 10 {
		t.Errorf("acme.test = %+v, want the 10 assignments it actually holds", got)
	}
}

// A pool with nothing groupable reports an empty distribution, not a nil one: the
// wire shape is a list, and `null` where the client expects `[]` is a client-side
// crash rather than an empty panel.
func TestGetSendersReportsAnEmptyDistributionRatherThanNil(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("no-domain-at-all", 5))

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if pool.FaultDomainShares == nil {
		t.Fatal("shares = nil, want an empty list")
	}
	if len(pool.FaultDomainShares) != 0 {
		t.Errorf("shares = %+v, want none: an address with no domain shares a fault with nothing",
			pool.FaultDomainShares)
	}
}

// The implicit one-mailbox pool a never-configured campaign sends from is reported
// the same way a real pool is — it is 100% concentrated by definition, and a campaign
// does not stop having an exposure profile because nobody opened the panel.
func TestGetSendersReportsConcentrationForTheFallbackPool(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id)
	store.fallbackSender = Sender{
		MailboxID: uuid.New(), Email: "solo@acme.test", Status: "active",
		Weight: 1, Enabled: true, AssignedCount: 4,
	}

	pool, err := NewService(store, okChecker{active: true}).GetSenders(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(pool.FaultDomainShares) != 1 || pool.FaultDomainShares[0].Domain != "acme.test" {
		t.Fatalf("shares = %+v, want acme.test", pool.FaultDomainShares)
	}
	if !pool.FaultDomainShares[0].OverBudget {
		t.Error("the fallback's sole domain is not flagged: it carries the entire campaign")
	}
}

// The wire contract, asserted on field NAMES and types rather than on the decoded
// Go struct: the frontend is generated from these names, and a rename that still
// compiles here would break it silently.
func TestGetSendersEmitsTheExposureContract(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("a@acme.test", 68), member("b@other.test", 32))
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	w := httptest.NewRecorder()
	req := newSendersRequest(t, secret, ws, id, http.MethodGet, "")
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.getSenders)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw struct {
		MaxFaultDomainShare *float64         `json:"max_fault_domain_share"`
		FaultDomainShares   []map[string]any `json:"fault_domain_shares"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw.MaxFaultDomainShare == nil || *raw.MaxFaultDomainShare != rotation.MaxFaultDomainShare {
		t.Errorf("max_fault_domain_share = %v, want %v", raw.MaxFaultDomainShare, rotation.MaxFaultDomainShare)
	}
	if len(raw.FaultDomainShares) != 2 {
		t.Fatalf("fault_domain_shares = %v, want two entries", raw.FaultDomainShares)
	}
	first := raw.FaultDomainShares[0]
	for _, key := range []string{"domain", "assigned", "share", "over_budget"} {
		if _, ok := first[key]; !ok {
			t.Errorf("fault_domain_shares[0] is missing %q; got %v", key, first)
		}
	}
	if first["domain"] != "acme.test" || first["assigned"] != float64(68) ||
		first["share"] != 0.68 || first["over_budget"] != true {
		t.Errorf("fault_domain_shares[0] = %v, want acme.test 68 @ 0.68 over budget", first)
	}
}

// An empty distribution must serialize as [] and not null: a generated client types
// this as an array, and null is a crash rather than an empty panel.
func TestExposureSharesSerializeAsAnEmptyArray(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := poolOf(ws, id, member("no-domain-at-all", 3))
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	w := httptest.NewRecorder()
	req := newSendersRequest(t, secret, ws, id, http.MethodGet, "")
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.getSenders)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"fault_domain_shares":[]`) {
		t.Errorf("body = %s, want fault_domain_shares as []", w.Body.String())
	}
}
