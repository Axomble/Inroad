// Package track rewrites outgoing HTML email bodies to route link clicks
// and opens through the tracking endpoints, using the token codec in
// internal/platform/track.
package track

import (
	"strings"

	"golang.org/x/net/html"

	plattrack "github.com/inroad/inroad/internal/platform/track"
)

// unsubMarker identifies the unsubscribe link so it is never rewritten into
// a click-tracked URL (an unsubscribe click must land the recipient on the
// unsubscribe page directly, not bounce through a redirect).
const unsubMarker = "/u/"

// RewriteHTML rewrites every http(s) <a href> in htmlBody into a click-
// tracking URL and appends an invisible open-tracking pixel.
//
// Rationale for using html.NewTokenizer instead of parsing the document
// into a tree and re-rendering it: a full parse + render pass normalizes
// the document (closes/reorders tags, rewrites attribute quoting, may hoist
// content out of <head>/<body>, etc.), which is safe for well-formed pages
// but routinely corrupts the fragile, hand-rolled markup that real-world
// email templates ship (Outlook conditional comments, unclosed tags,
// table-based layouts). Token-by-token rewriting lets us re-emit every
// token's raw bytes verbatim and touch only the one thing we need to
// change: the href attribute value on anchor tags.
func RewriteHTML(htmlBody, baseURL, sendID string, secret []byte) string {
	if htmlBody == "" {
		return htmlBody
	}

	var out strings.Builder
	z := html.NewTokenizer(strings.NewReader(htmlBody))

	bodyCloseWritten := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // io.EOF or a tokenizer error — either way, stop and finish below.
		}

		// z.Token() is only called for tag tokens whose name we need, and at
		// most once per token: the Tokenizer's TagName()/TagAttr() cursor is
		// consumed as it's read, so calling it a second time on the same
		// token (e.g. once to peek the name, again inside Token()) silently
		// returns no name/attributes on the second call.
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data == "a" {
				if rewritten, ok := rewriteAnchorTag(tok, baseURL, sendID, secret); ok {
					out.WriteString(rewritten)
					continue
				}
				// No trackable href on this anchor — fall through and emit
				// z.Raw() below so the tag is untouched byte-for-byte.
			}
		case html.EndTagToken:
			tok := z.Token()
			if tok.Data == "body" && !bodyCloseWritten {
				out.WriteString(openPixelTag(baseURL, sendID, secret))
				bodyCloseWritten = true
			}
		}

		out.Write(z.Raw()) // verbatim passthrough — see package doc for rationale.
	}

	if !bodyCloseWritten {
		out.WriteString(openPixelTag(baseURL, sendID, secret))
	}

	return out.String()
}

// rewriteAnchorTag re-serializes a single <a> tag, replacing its href value
// with a click-tracking URL, when the tag has an href that is http(s) and
// is not the unsubscribe link (ok is false and the tag should be emitted
// raw/untouched otherwise). Every other attribute (and its order) is
// preserved; the tag is rebuilt via Token.String() rather than raw-byte
// substring substitution so the href we compare is the already-unescaped
// attribute value the tokenizer parsed (avoiding a mismatch when the
// source uses an HTML entity such as &amp; in the URL).
//
// Token.String() lowercases attribute keys and expands boolean attributes
// to their explicit form (e.g. `download` becomes `download=""`); no
// attribute is dropped or reordered. This only affects the one anchor
// being rewritten and is harmless for email HTML, so it's an acceptable
// tradeoff for the simplicity of reusing Token.String() here.
func rewriteAnchorTag(tok html.Token, baseURL, sendID string, secret []byte) (rewritten string, ok bool) {
	for i, attr := range tok.Attr {
		if attr.Key != "href" || !shouldRewrite(attr.Val) {
			continue
		}
		token := plattrack.MakeClickToken(secret, sendID, attr.Val)
		tok.Attr[i].Val = baseURL + "/t/c/" + token
		return tok.String(), true
	}
	return "", false
}

// shouldRewrite reports whether href is a link we track: absolute http(s),
// and not the unsubscribe link. Both checks run against the lowercased
// href so an uppercase scheme or an uppercase unsub path segment (e.g.
// "/U/...") are still recognized.
func shouldRewrite(href string) bool {
	lower := strings.ToLower(href)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	if strings.Contains(lower, unsubMarker) {
		return false
	}
	return true
}

// openPixelTag renders the invisible 1x1 open-tracking pixel.
func openPixelTag(baseURL, sendID string, secret []byte) string {
	token := plattrack.MakeOpenToken(secret, sendID)
	return `<img src="` + baseURL + `/t/o/` + token + `.gif" width="1" height="1" alt="" style="display:none">`
}
