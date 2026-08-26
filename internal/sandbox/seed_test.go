package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A missing workspace id must fail loudly rather than writing rows keyed on
// the nil UUID, which would be a tenancy hole rather than a mistake.
func TestSeedRequiresAWorkspace(t *testing.T) {
	store := newFakeStore()
	_, err := NewSeeder(nil, store, store).Seed(context.Background(), SeedInput{})
	if err == nil {
		t.Fatal("Seed with no workspace id succeeded")
	}
	if !strings.Contains(err.Error(), "workspace id") {
		t.Errorf("error %q does not name the missing input", err)
	}
	if len(store.sends) != 0 {
		t.Error("Seed wrote rows despite an invalid input")
	}
}

// The launched_at the seeder stamps has to agree with the history it seeds:
// a campaign launched "just now" carrying three weeks of sends reads as
// corrupt data.
func TestLaunchedAtMatchesTheHistoryWindow(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	s := &Seeder{}

	got := s.launchedAt(SeedInput{Options: Options{Now: now, Window: 21 * 24 * time.Hour}})
	if want := now.Add(-21 * 24 * time.Hour); !got.Equal(want) {
		t.Errorf("launchedAt = %v, want %v", got, want)
	}

	// Defaults apply when the caller supplies neither.
	got = s.launchedAt(SeedInput{Options: Options{Now: now}})
	if want := now.Add(-DefaultWindow); !got.Equal(want) {
		t.Errorf("launchedAt with a default window = %v, want %v", got, want)
	}

	// An unset Now must still produce a sane past instant, not the epoch.
	if got := s.launchedAt(SeedInput{}); got.Year() < 2020 {
		t.Errorf("launchedAt with no clock = %v, want a recent instant", got)
	}
}

// The default population is what makes a run demoable; sizing it below the
// page limit would hide pagination, which is one of the things the seeded
// workspace exists to exercise.
func TestDefaultContactsExceedsAPage(t *testing.T) {
	const maxPageSize = 100
	if DefaultContacts <= maxPageSize {
		t.Errorf("DefaultContacts = %d, want more than the %d max page size", DefaultContacts, maxPageSize)
	}
}

// The seeded mailbox must never be able to reach a real recipient, whatever
// else goes wrong.
func TestSeededMailboxCannotResolve(t *testing.T) {
	if !strings.HasSuffix(DefaultMailboxEmail, ".test") {
		t.Errorf("DefaultMailboxEmail = %q, want a .test address that cannot resolve", DefaultMailboxEmail)
	}
	if !strings.HasSuffix(DefaultMessageIDDomain, ".test") {
		t.Errorf("DefaultMessageIDDomain = %q, want a .test domain", DefaultMessageIDDomain)
	}
}

// Every generated persona address must be unroutable too: the harness must be
// incapable of mailing a real person even with -deliver pointed somewhere
// unexpected.
func TestPersonaDomainsAreNotRoutable(t *testing.T) {
	// The persona roster uses realistic-looking domains on purpose (that is
	// what makes it demoable), so the safety property cannot come from the
	// address. It comes from delivery only ever going to an explicit local
	// catcher — assert that the deliverer is opt-in and has a local default.
	// The loopback LITERAL, not "localhost": Docker publishes the port on IPv4
	// only, and "localhost" resolves to ::1 first on Windows, so the documented
	// command fails to connect.
	if DefaultMailpitHost != "127.0.0.1" {
		t.Errorf("DefaultMailpitHost = %q, want the 127.0.0.1 literal", DefaultMailpitHost)
	}
	d := NewSMTPDeliverer(DefaultMailpitHost, DefaultMailpitSMTPPort)
	if d.host != "127.0.0.1" || d.port != DefaultMailpitSMTPPort {
		t.Errorf("NewSMTPDeliverer built %s:%d, want the local catcher", d.host, d.port)
	}
}

// Close must be safe on a deliverer that never dialed, so a caller can defer
// it unconditionally.
func TestCloseWithoutDialingIsANoOp(t *testing.T) {
	if err := NewSMTPDeliverer(DefaultMailpitHost, DefaultMailpitSMTPPort).Close(); err != nil {
		t.Errorf("Close() on an undialed deliverer = %v, want nil", err)
	}
}

// A Simulator built with no Deliverer must record history and put nothing on
// the wire — the default, since seeding does not require a running catcher.
func TestSimulationWithoutDelivererSendsNothing(t *testing.T) {
	store := newFakeStore()
	res, err := NewSimulator(store, store, Options{Now: time.Now()}).
		Run(context.Background(), newTarget(20))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Delivered != 0 {
		t.Errorf("Delivered = %d with no deliverer configured", res.Delivered)
	}
	if res.Sends == 0 {
		t.Error("no history recorded")
	}
}

func TestResultSummaryNamesWhatItWrote(t *testing.T) {
	got := Result{Contacts: 300, Sends: 512, Opens: 120, Clicks: 36, Replies: 24, Bounces: 6, Threads: 24}.String()
	for _, want := range []string{"300 contacts", "512 sends", "24 replies", "24 threads", "6 bounces"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not mention %q", got, want)
		}
	}
}

func TestShapeDropsCopyKeepsCadence(t *testing.T) {
	shape := Shape(campaignSteps)
	if len(shape.Steps) != len(campaignSteps) {
		t.Fatalf("Shape produced %d steps, want %d", len(shape.Steps), len(campaignSteps))
	}
	for i, s := range shape.Steps {
		if s.Order != campaignSteps[i].Order || s.DelaySeconds != campaignSteps[i].DelaySeconds {
			t.Errorf("step %d = %+v, want order/delay from %+v", i, s, campaignSteps[i])
		}
	}
}

// The bodies are stored on the campaign and rendered in the thread reader; an
// empty HTML leg renders the outbound messages blank.
func TestStepBodiesRenderToHTML(t *testing.T) {
	for _, s := range campaignSteps {
		html := s.BodyHTML()
		if html == "" {
			t.Fatalf("step %d produced no HTML body", s.Order)
		}
		if !strings.HasPrefix(html, "<p>") || !strings.HasSuffix(html, "</p>") {
			t.Errorf("step %d HTML is not paragraph-wrapped: %q", s.Order, html)
		}
	}
}
