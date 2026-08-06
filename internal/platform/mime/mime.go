// Package mime extracts human-readable text from a raw email body. Nothing
// upstream of this parses MIME today (internal/platform/mail's providers hand
// back the byte-for-byte body after the top-level headers — fine for DSN
// parsing and keyword-based reply classification, useless for rendering a
// message a human reads). Pure: no I/O, no network, safe on attacker-supplied
// bytes (bounded recursion, never panics on malformed input).
package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"strings"
)

const maxRecursionDepth = 8

// Extract returns the plain-text and HTML parts of a message body. contentType
// is the raw Content-Type header value the message arrived with. A single-part
// text/plain or text/html body is handled directly; a multipart body (nested
// to maxRecursionDepth) is walked for the first text/plain and first text/html
// part it contains, decoding quoted-printable/base64 per-part
// Content-Transfer-Encoding. A body whose declared boundary is missing or
// otherwise unparseable is NOT an error condition — Extract falls back to
// treating the raw body as plain text so a reply is never dropped over odd
// MIME, and returns a nil error in that case too. err is reserved for
// programmer-facing signals only; as implemented, Extract never returns one,
// but keeps the return slot so a future stricter mode has somewhere to put it
// without an API break.
func Extract(contentType string, body []byte) (plainText, html string, err error) {
	mediaType, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || !strings.HasPrefix(mediaType, "multipart/") {
		// A Content-Type this package cannot parse is not a failure, it is a
		// single-part body. Propagating parseErr would drop a real reply from a
		// client that sent an odd header, which is strictly worse than treating
		// the bytes as plain text. See the doc comment above.
		//nolint:nilerr // documented fallback: unparseable MIME is not an error
		return string(body), "", nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return string(body), "", nil
	}
	plain, htm := walkMultipart(bytes.NewReader(body), boundary, 0)
	if plain == "" && htm == "" {
		// A multipart envelope that yielded nothing recognizable (all parts
		// were attachments, or the boundary was declared but never matched a
		// real part) — fall back to the raw body rather than storing nothing.
		return string(body), "", nil
	}
	return plain, htm, nil
}

// walkMultipart returns the first text/plain and first text/html leaf part it
// finds, recursing into nested multipart parts up to maxRecursionDepth.
func walkMultipart(r io.Reader, boundary string, depth int) (plainText, html string) {
	if depth >= maxRecursionDepth {
		return "", ""
	}
	mr := multipart.NewReader(r, boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return plainText, html
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			partType = "text/plain"
		}
		if strings.HasPrefix(partType, "multipart/") {
			if nb := partParams["boundary"]; nb != "" {
				raw, _ := io.ReadAll(part)
				np, nh := walkMultipart(bytes.NewReader(raw), nb, depth+1)
				if plainText == "" {
					plainText = np
				}
				if html == "" {
					html = nh
				}
			}
			continue
		}
		decoded, err := decodePart(part)
		if err != nil {
			continue
		}
		switch {
		case plainText == "" && partType == "text/plain":
			plainText = string(decoded)
		case html == "" && partType == "text/html":
			html = string(decoded)
		}
	}
}

// decodePart applies this part's own Content-Transfer-Encoding. An unknown or
// absent encoding is treated as identity (7bit/8bit/binary all read as-is).
func decodePart(part *multipart.Part) ([]byte, error) {
	switch strings.ToLower(part.Header.Get("Content-Transfer-Encoding")) {
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(part))
	case "base64":
		raw, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		return decodeBase64Loose(raw)
	default:
		return io.ReadAll(part)
	}
}

func decodeBase64Loose(raw []byte) ([]byte, error) {
	// Real-world base64 bodies wrap at 76 chars with CRLF; strip whitespace
	// before decoding rather than requiring the caller's encoder to have been
	// well-behaved.
	cleaned := make([]byte, 0, len(raw))
	for _, b := range raw {
		if b != '\r' && b != '\n' && b != ' ' {
			cleaned = append(cleaned, b)
		}
	}
	dst := make([]byte, base64.StdEncoding.DecodedLen(len(cleaned)))
	n, err := base64.StdEncoding.Decode(dst, cleaned)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}
