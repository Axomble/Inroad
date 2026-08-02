package sendingdomain

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/dnsauth"
)

var testWS = uuid.New()

// fakeStore records what the service asked for. Nothing here touches Postgres.
type fakeStore struct {
	list    []Domain
	get     map[string]Domain
	listErr error
	getErr  error

	recorded  []dnsauth.Result
	recordErr error
	// getCalls proves the ownership check ran (and how many times).
	getCalls []string
}

func (f *fakeStore) List(context.Context, uuid.UUID) ([]Domain, error) {
	return f.list, f.listErr
}

func (f *fakeStore) Get(_ context.Context, _ uuid.UUID, domain string) (Domain, error) {
	f.getCalls = append(f.getCalls, domain)
	if f.getErr != nil {
		return Domain{}, f.getErr
	}
	d, ok := f.get[domain]
	if !ok {
		return Domain{}, ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) Record(_ context.Context, _ uuid.UUID, res dnsauth.Result) (time.Time, error) {
	f.recorded = append(f.recorded, res)
	if f.recordErr != nil {
		return time.Time{}, f.recordErr
	}
	return recordedAt, nil
}

var recordedAt = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// fakeResolver answers from a map and counts every lookup, so a test can assert
// that no lookup happened at all.
type fakeResolver struct {
	txt   map[string][]string
	errs  map[string]error
	calls []string
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if txt, ok := f.txt[name]; ok {
		return txt, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

const ownDomain = "acme.com"

func authenticated() map[string][]string {
	return map[string][]string{
		ownDomain:             {"v=spf1 include:_spf.google.com ~all"},
		"_dmarc." + ownDomain: {"v=DMARC1; p=none"},
	}
}

// The security property: a domain the workspace does not send from is a 404 and
// NO DNS lookup happens, so the endpoint cannot resolve arbitrary names through
// our resolver.
func TestCheckRejectsAForeignDomainBeforeAnyLookup(t *testing.T) {
	store := &fakeStore{get: map[string]Domain{ownDomain: {Domain: ownDomain, MailboxCount: 2}}}
	res := &fakeResolver{txt: authenticated()}
	svc := NewService(store, res)

	for _, domain := range []string{"victim.example", "169.254.169.254", "internal.corp", ""} {
		if _, err := svc.Check(context.Background(), testWS, domain); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Check(%q) error = %v, want ErrNotFound", domain, err)
		}
	}
	if len(res.calls) != 0 {
		t.Fatalf("lookups performed for foreign domains: %v", res.calls)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("wrote %d rows for foreign domains", len(store.recorded))
	}
}

func TestCheckPersistsACompletedCheck(t *testing.T) {
	store := &fakeStore{get: map[string]Domain{ownDomain: {Domain: ownDomain, MailboxCount: 3}}}
	svc := NewService(store, &fakeResolver{txt: authenticated()})

	got, err := svc.Check(context.Background(), testWS, ownDomain)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.State != dnsauth.StatePassing {
		t.Fatalf("State = %q, want passing", got.State)
	}
	if got.MailboxCount != 3 {
		t.Fatalf("MailboxCount = %d, want 3 (carried from the derived row)", got.MailboxCount)
	}
	if got.DMARCPolicy != "none" {
		t.Fatalf("DMARCPolicy = %q, want none", got.DMARCPolicy)
	}
	if got.CheckedAt == nil || !got.CheckedAt.Equal(recordedAt) {
		t.Fatalf("CheckedAt = %v, want %v", got.CheckedAt, recordedAt)
	}
	if len(store.recorded) != 1 || store.recorded[0].State() != dnsauth.StatePassing {
		t.Fatalf("recorded = %+v, want one passing result", store.recorded)
	}
}

// Invariant 1: a resolver blip must not overwrite a known-good verdict, and must
// not stamp checked_at — otherwise the sweep would treat the domain as freshly
// checked and skip it for a whole staleness window.
func TestCheckDoesNotPersistAnUnknownResult(t *testing.T) {
	lastChecked := recordedAt.Add(-48 * time.Hour)
	store := &fakeStore{get: map[string]Domain{ownDomain: {
		Domain:       ownDomain,
		MailboxCount: 1,
		State:        dnsauth.StatePassing,
		SPFFound:     true,
		SPFRecord:    "v=spf1 -all",
		DMARCFound:   true,
		DMARCPolicy:  "reject",
		CheckedAt:    &lastChecked,
	}}}
	res := &fakeResolver{
		txt:  authenticated(),
		errs: map[string]error{ownDomain: &net.DNSError{Err: "i/o timeout", IsTimeout: true}},
	}
	svc := NewService(store, res)

	got, err := svc.Check(context.Background(), testWS, ownDomain)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.State != dnsauth.StateUnknown {
		t.Fatalf("State = %q, want unknown", got.State)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("an unknown result was persisted: %+v", store.recorded)
	}
	// The stored detail and its timestamp survive untouched: we learned nothing.
	if got.CheckedAt == nil || !got.CheckedAt.Equal(lastChecked) {
		t.Fatalf("CheckedAt = %v, want the previous %v", got.CheckedAt, lastChecked)
	}
	if !got.SPFFound || got.DMARCPolicy != "reject" {
		t.Fatalf("stored detail was clobbered by a failed lookup: %+v", got)
	}
}

func TestCheckNormalizesTheDomainBeforeItReachesTheStoreOrResolver(t *testing.T) {
	store := &fakeStore{get: map[string]Domain{ownDomain: {Domain: ownDomain, MailboxCount: 1}}}
	res := &fakeResolver{txt: authenticated()}
	svc := NewService(store, res)

	if _, err := svc.Check(context.Background(), testWS, "ACME.com."); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(store.getCalls) != 1 || store.getCalls[0] != ownDomain {
		t.Fatalf("store saw %v, want [%s]", store.getCalls, ownDomain)
	}
}

func TestCheckPropagatesStoreFailures(t *testing.T) {
	boom := errors.New("db down")
	svc := NewService(&fakeStore{getErr: boom}, &fakeResolver{})
	if _, err := svc.Check(context.Background(), testWS, ownDomain); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}

	store := &fakeStore{
		get:       map[string]Domain{ownDomain: {Domain: ownDomain}},
		recordErr: boom,
	}
	svc = NewService(store, &fakeResolver{txt: authenticated()})
	if _, err := svc.Check(context.Background(), testWS, ownDomain); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func TestListPassesThroughEveryDerivedDomain(t *testing.T) {
	never := Domain{Domain: "new.test", MailboxCount: 1, State: dnsauth.StateUnknown}
	store := &fakeStore{list: []Domain{never, {Domain: ownDomain, MailboxCount: 2, State: dnsauth.StatePassing}}}
	got, err := NewService(store, &fakeResolver{}).List(context.Background(), testWS)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].CheckedAt != nil || got[0].State != dnsauth.StateUnknown {
		t.Fatalf("List = %+v, want the never-checked domain present as unknown", got)
	}
}
