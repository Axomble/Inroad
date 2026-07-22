package track

import (
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/track"
)

var testSecret = []byte("test-secret-key")

const testBaseURL = "https://track.example.com"
const testSendID = "8f14e45f-ceea-467e-adc1-0000000000ab"

func TestRewriteHTML_RewritesHTTPLinks(t *testing.T) {
	html := `<p>Visit <a href="https://example.com/a">A</a> or <a href="http://example.com/b?x=1">B</a>.</p>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	for _, want := range []string{"https://example.com/a", "http://example.com/b?x=1"} {
		token, ok := extractClickToken(t, got, want)
		if !ok {
			t.Fatalf("RewriteHTML() output does not contain a rewritten link for %q; got %q", want, got)
		}
		_, gotURL, ok := track.ParseClickToken(testSecret, token)
		if !ok {
			t.Fatalf("ParseClickToken(%q) ok = false, want true", token)
		}
		if gotURL != want {
			t.Errorf("decoded click token url = %q, want %q", gotURL, want)
		}
	}
	if strings.Contains(got, `href="https://example.com/a"`) || strings.Contains(got, `href="http://example.com/b?x=1"`) {
		t.Errorf("RewriteHTML() left an original href unrewritten: %q", got)
	}
}

func TestRewriteHTML_LeavesNonHTTPLinksUnchanged(t *testing.T) {
	cases := []string{
		`<a href="mailto:person@example.com">Email</a>`,
		`<a href="#section">Jump</a>`,
		`<a href="/relative/path">Relative</a>`,
		`<a href="javascript:alert(1)">JS</a>`,
	}
	for _, html := range cases {
		// RewriteHTML always appends the open pixel, so assert the anchor
		// itself is untouched rather than exact whole-output equality.
		got := RewriteHTML(html, testBaseURL, testSendID, testSecret)
		if !strings.HasPrefix(got, html) {
			t.Errorf("RewriteHTML(%q) = %q, want unchanged anchor as prefix", html, got)
		}
	}
}

func TestRewriteHTML_LeavesUnsubscribeLinkUnchanged(t *testing.T) {
	html := `<a href="https://track.example.com/u/abc123.def456">Unsubscribe</a>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if !strings.HasPrefix(got, html) {
		t.Errorf("RewriteHTML() rewrote the unsubscribe link: got %q, want unchanged anchor as prefix %q", got, html)
	}
}

func TestRewriteHTML_NonAnchorMarkupIsByteIdentical(t *testing.T) {
	html := `<table style="width:100%"><tr><td><b class="bold" data-x="1">hi</b></td></tr></table>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if !strings.HasPrefix(got, html) {
		t.Errorf("RewriteHTML() altered non-anchor markup: got %q, want prefix %q", got, html)
	}
}

func TestRewriteHTML_AppendsOpenPixel(t *testing.T) {
	html := `<p>hello</p>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if !strings.Contains(got, testBaseURL+"/t/o/") || !strings.Contains(got, ".gif") {
		t.Fatalf("RewriteHTML() output missing open pixel: got %q", got)
	}

	token := extractOpenToken(t, got)
	gotSendID, ok := track.ParseOpenToken(testSecret, token)
	if !ok {
		t.Fatalf("ParseOpenToken(%q) ok = false, want true", token)
	}
	if gotSendID != testSendID {
		t.Errorf("decoded open token sendID = %q, want %q", gotSendID, testSendID)
	}
}

func TestRewriteHTML_PixelPlacedBeforeBodyClose(t *testing.T) {
	html := `<html><body><p>hello</p></body></html>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	bodyClose := strings.Index(got, "</body>")
	pixelIdx := strings.Index(got, testBaseURL+"/t/o/")
	if bodyClose < 0 {
		t.Fatalf("RewriteHTML() output lost </body>: got %q", got)
	}
	if pixelIdx < 0 || pixelIdx > bodyClose {
		t.Errorf("RewriteHTML() pixel not placed before </body>: got %q", got)
	}
}

func TestRewriteHTML_MalformedTwoBodyCloseTagsGetsOnlyOnePixel(t *testing.T) {
	// Malformed HTML with a duplicated </body> must not double-count opens:
	// the pixel is inserted before the first </body> only.
	html := `<html><body><p>hello</p></body>trailing</body></html>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if n := strings.Count(got, testBaseURL+"/t/o/"); n != 1 {
		t.Errorf("RewriteHTML() inserted %d open pixels, want exactly 1: got %q", n, got)
	}
}

func TestRewriteHTML_LeavesUppercaseUnsubscribeLinkUnchanged(t *testing.T) {
	html := `<a href="https://track.example.com/U/abc123.def456">Unsubscribe</a>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if !strings.HasPrefix(got, html) {
		t.Errorf("RewriteHTML() rewrote an uppercase unsubscribe link: got %q, want unchanged anchor as prefix %q", got, html)
	}
}

func TestRewriteHTML_PixelAppendedWhenNoBodyClose(t *testing.T) {
	html := `<p>hello</p>`

	got := RewriteHTML(html, testBaseURL, testSendID, testSecret)

	if !strings.HasSuffix(strings.TrimRight(got, "\n"), ".gif\" width=\"1\" height=\"1\" alt=\"\" style=\"display:none\">") {
		t.Errorf("RewriteHTML() pixel not appended at end: got %q", got)
	}
}

func TestRewriteHTML_MalformedHTMLDoesNotPanic(t *testing.T) {
	cases := []string{
		`<p>unclosed <a href="https://example.com">link`,
		`<div><span>`,
		`<a href="https://example.com"`,
		`not even html`,
	}
	for _, html := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("RewriteHTML(%q) panicked: %v", html, r)
				}
			}()
			RewriteHTML(html, testBaseURL, testSendID, testSecret)
		}()
	}
}

func TestRewriteHTML_EmptyBodyReturnsUnchanged(t *testing.T) {
	got := RewriteHTML("", testBaseURL, testSendID, testSecret)
	if got != "" {
		t.Errorf("RewriteHTML(\"\") = %q, want empty string", got)
	}
}

// extractClickToken finds the rewritten href for originalURL by locating the
// {baseURL}/t/c/{token} URL in the output and returning just the token.
func extractClickToken(t *testing.T, html, originalURL string) (string, bool) {
	t.Helper()
	prefix := testBaseURL + "/t/c/"
	idx := strings.Index(html, prefix)
	for idx >= 0 {
		rest := html[idx+len(prefix):]
		end := strings.IndexAny(rest, `"'`)
		if end < 0 {
			return "", false
		}
		token := rest[:end]
		_, gotURL, ok := track.ParseClickToken(testSecret, token)
		if ok && gotURL == originalURL {
			return token, true
		}
		next := strings.Index(html[idx+1:], prefix)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return "", false
}

func extractOpenToken(t *testing.T, html string) string {
	t.Helper()
	prefix := testBaseURL + "/t/o/"
	idx := strings.Index(html, prefix)
	if idx < 0 {
		t.Fatalf("open pixel prefix %q not found in %q", prefix, html)
	}
	rest := html[idx+len(prefix):]
	end := strings.Index(rest, ".gif")
	if end < 0 {
		t.Fatalf("open pixel missing .gif suffix in %q", html)
	}
	return rest[:end]
}
