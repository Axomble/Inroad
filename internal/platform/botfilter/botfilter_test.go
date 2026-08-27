package botfilter

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

// sentAt is the reference send time every timing case is measured against.
var sentAt = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// realBrowserUserAgents is a corpus of User-Agents genuine mail clients and
// browsers actually send. It serves two tests: that none of them classifies as
// a proxy, and that no marker anyone adds later is a substring of one.
var realBrowserUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 Edg/128.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 OPR/114.0.0.0",
	"Outlook-iOS/731.0.0 (iPhone; iOS 17.6)",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36",
}

// A human open, well after the send, from an ordinary browser, is the baseline
// every machine case is a departure from. If this ever flips, the feature is
// deleting real engagement rather than filtering noise.
func TestPlainBrowserOpenIsHuman(t *testing.T) {
	for _, ua := range realBrowserUserAgents {
		hit := Hit{Kind: KindOpen, UserAgent: ua, At: sentAt.Add(3 * time.Hour), SentAt: sentAt}
		if v, r := Classify(hit, Prior{}); v != Human || r != ReasonNone {
			t.Errorf("Classify(%q) = %v/%v, want human/none — a real reader must never be filtered out", ua, v, r)
		}
	}
}

func TestClassify(t *testing.T) {
	const browser = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

	tests := []struct {
		name       string
		hit        Hit
		prior      Prior
		wantVerd   Verdict
		wantReason Reason
	}{
		// --- Known proxy / scanner User-Agents ---
		{
			name:       "gmail image proxy open",
			hit:        Hit{Kind: KindOpen, UserAgent: "Mozilla/5.0 (Windows NT 5.1; rv:11.0) Gecko Firefox/11.0 (via ggpht.com GoogleImageProxy)", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "outlook safelinks click",
			hit:        Hit{Kind: KindClick, UserAgent: "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1) SafeLinks", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "proofpoint scanner",
			hit:        Hit{Kind: KindOpen, UserAgent: "Proofpoint-URLDefense/2.0", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "barracuda appliance",
			hit:        Hit{Kind: KindOpen, UserAgent: "BarracudaCentral-LinkProtect/1.1", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "mimecast appliance",
			hit:        Hit{Kind: KindOpen, UserAgent: "Mimecast-URL-Protect/3", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "marker match is case insensitive",
			hit:        Hit{Kind: KindOpen, UserAgent: "GOOGLEIMAGEPROXY", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:       "generic http client",
			hit:        Hit{Kind: KindOpen, UserAgent: "curl/8.6.0", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:     "empty user agent is not machine on its own",
			hit:      Hit{Kind: KindOpen, UserAgent: "", At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},

		// --- Timing: sub-window open ---
		{
			name:       "open one second after send is a prefetch",
			hit:        Hit{Kind: KindOpen, UserAgent: browser, At: sentAt.Add(time.Second), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonPrefetchWindow,
		},
		{
			name:       "open in the same instant as the send is a prefetch",
			hit:        Hit{Kind: KindOpen, UserAgent: browser, At: sentAt, SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonPrefetchWindow,
		},
		{
			name:     "open exactly at the window boundary is human",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, At: sentAt.Add(PrefetchWindow), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			name:     "unknown send time disables the timing rule",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, At: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			name:     "hit before the send is clock skew not evidence",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, At: sentAt.Add(-time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},

		// --- Ordering: click without a preceding open ---
		{
			name:       "instant click with no open at all is a scanner",
			hit:        Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(500 * time.Millisecond), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonClickWithoutOpen,
		},
		{
			name:       "click at the same instant as the open",
			hit:        Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(time.Hour), SentAt: sentAt},
			prior:      Prior{HumanOpens: 1, LastHumanOpenAt: sentAt.Add(time.Hour)},
			wantVerd:   Machine,
			wantReason: ReasonClickWithoutOpen,
		},
		{
			name:       "click before the recorded open",
			hit:        Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(time.Hour), SentAt: sentAt},
			prior:      Prior{HumanOpens: 1, LastHumanOpenAt: sentAt.Add(2 * time.Hour)},
			wantVerd:   Machine,
			wantReason: ReasonClickWithoutOpen,
		},
		{
			name:     "click after a human open is human",
			hit:      Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(2 * time.Hour), SentAt: sentAt},
			prior:    Prior{HumanOpens: 1, LastHumanOpenAt: sentAt.Add(time.Hour)},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			// The images-blocked reader: no open will EVER be recorded, so a
			// late click with no open must stay human or this feature deletes
			// the engagement of every privacy-conscious recipient.
			name:     "late click with no open is human (images blocked)",
			hit:      Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(6 * time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			// A machine open does not vouch for the click that follows it.
			name:       "click after only a MACHINE open is still a scanner",
			hit:        Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(time.Second), SentAt: sentAt},
			prior:      Prior{HumanOpens: 0},
			wantVerd:   Machine,
			wantReason: ReasonClickWithoutOpen,
		},

		// --- Volume: burst from one subnet ---
		{
			name:       "fifth open of one send from one subnet",
			hit:        Hit{Kind: KindOpen, UserAgent: browser, At: sentAt.Add(time.Hour), SentAt: sentAt},
			prior:      Prior{OpensFromSubnet: BurstSubnetOpenThreshold - 1},
			wantVerd:   Machine,
			wantReason: ReasonBurstFromSubnet,
		},
		{
			name:     "one below the burst threshold is human",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, At: sentAt.Add(time.Hour), SentAt: sentAt},
			prior:    Prior{OpensFromSubnet: BurstSubnetOpenThreshold - 2},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			// A burst is an OPEN-volume signal; it must not condemn a click
			// that a genuine human open already vouched for.
			name:     "burst count does not apply to a vouched click",
			hit:      Hit{Kind: KindClick, UserAgent: browser, At: sentAt.Add(2 * time.Hour), SentAt: sentAt},
			prior:    Prior{HumanOpens: 1, LastHumanOpenAt: sentAt.Add(time.Hour), OpensFromSubnet: 99},
			wantVerd: Human, wantReason: ReasonNone,
		},

		// --- Datacenter IP ---
		{
			name:       "open from a public resolver range",
			hit:        Hit{Kind: KindOpen, UserAgent: browser, IP: netip.MustParseAddr("8.8.8.8"), At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonDatacenterIP,
		},
		{
			name:     "residential address is human",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, IP: netip.MustParseAddr("203.0.113.7"), At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			// A misconfigured reverse proxy makes EVERY hit look private.
			// Calling those machine would zero out a deployment's open rate.
			name:     "private address behind a proxy is not machine",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, IP: netip.MustParseAddr("10.0.0.5"), At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
		{
			name:     "invalid address is missing data not a verdict",
			hit:      Hit{Kind: KindOpen, UserAgent: browser, IP: netip.Addr{}, At: sentAt.Add(time.Hour), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},

		// --- Precedence ---
		{
			// A UA that self-identifies is stronger evidence than timing, so
			// the stored reason must name the UA.
			name:       "self-identification outranks the timing rule",
			hit:        Hit{Kind: KindOpen, UserAgent: "GoogleImageProxy", At: sentAt.Add(time.Second), SentAt: sentAt},
			wantVerd:   Machine,
			wantReason: ReasonProxyUserAgent,
		},
		{
			name:     "unknown kind is not condemned by default",
			hit:      Hit{Kind: Kind("subscribe"), UserAgent: browser, At: sentAt.Add(time.Second), SentAt: sentAt},
			wantVerd: Human, wantReason: ReasonNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVerd, gotReason := Classify(tc.hit, tc.prior)
			if gotVerd != tc.wantVerd || gotReason != tc.wantReason {
				t.Errorf("Classify() = %v/%v, want %v/%v", gotVerd, gotReason, tc.wantVerd, tc.wantReason)
			}
		})
	}
}

// A marker that appears inside an ordinary browser UA would classify every
// human as a machine and silently zero out the open rate — the worst failure
// this package has. This is the guard the "TO EXTEND" comment points at.
func TestProxyUserAgentMarkersAreDistinctive(t *testing.T) {
	for _, ua := range realBrowserUserAgents {
		lower := strings.ToLower(ua)
		for _, marker := range proxyUserAgentMarkers {
			if strings.Contains(lower, marker) {
				t.Errorf("marker %q appears in the genuine client UA %q — it would classify every "+
					"such reader as a machine and delete their engagement", marker, ua)
			}
		}
		if isProxyUserAgent(ua) {
			t.Errorf("isProxyUserAgent(%q) = true, want false", ua)
		}
	}
}

// Markers are compared against a lowercased UA, so an uppercase character in
// the table can never match anything.
func TestProxyUserAgentMarkersAreLowercase(t *testing.T) {
	for _, marker := range proxyUserAgentMarkers {
		if marker != strings.ToLower(marker) {
			t.Errorf("marker %q is not lowercase — it can never match, since the UA is lowercased first", marker)
		}
		if marker == "" {
			t.Error("an empty marker matches every UA")
		}
	}
}

func TestSubnetKey(t *testing.T) {
	tests := []struct {
		name string
		addr netip.Addr
		want string
		ok   bool
	}{
		{"ipv4 groups to a /24", netip.MustParseAddr("203.0.113.7"), "203.0.113.0/24", true},
		{"ipv4 neighbours share a key", netip.MustParseAddr("203.0.113.200"), "203.0.113.0/24", true},
		{"ipv6 groups to a /48", netip.MustParseAddr("2001:db8:1:2::5"), "2001:db8:1::/48", true},
		{"invalid address has no key", netip.Addr{}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SubnetKey(tc.addr)
			if ok != tc.ok {
				t.Fatalf("SubnetKey() ok = %v, want %v", ok, tc.ok)
			}
			if ok && got.String() != tc.want {
				t.Errorf("SubnetKey() = %s, want %s", got, tc.want)
			}
		})
	}
}

// Classify must be a pure function of its inputs: the same facts must always
// yield the same verdict, or a stored classification stops meaning anything.
func TestClassifyIsDeterministic(t *testing.T) {
	hit := Hit{Kind: KindOpen, UserAgent: "GoogleImageProxy", IP: netip.MustParseAddr("8.8.8.8"), At: sentAt.Add(time.Second), SentAt: sentAt}
	wantV, wantR := Classify(hit, Prior{})
	for range 100 {
		if v, r := Classify(hit, Prior{}); v != wantV || r != wantR {
			t.Fatalf("Classify() returned %v/%v then %v/%v for identical input", wantV, wantR, v, r)
		}
	}
}
