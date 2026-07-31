package httpx

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// serverRoutePrefixes are the top-level trees owned by the server, never by the
// SPA. Mounting the SPA as the root NotFound handler makes chi inherit it into
// every mounted subrouter, so an unknown path under these prefixes would answer
// 200 + the app shell to any client that accepts HTML. They stay real 404s.
var serverRoutePrefixes = []string{"/api/", "/oauth/", "/oauth2/", "/u/", "/t/"}

// SPA returns a static handler for a built single-page app. It serves existing
// files directly and falls back to index.html only for browser navigation
// requests. Unknown API paths and missing assets remain real 404s.
func SPA(dir string) (http.Handler, bool) {
	if dir == "" {
		return nil, false
	}
	root := os.DirFS(dir)
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return nil, false
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		clean := path.Clean(r.URL.Path)
		for _, prefix := range serverRoutePrefixes {
			if strings.HasPrefix(clean+"/", prefix) {
				http.NotFound(w, r)
				return
			}
		}

		name := strings.TrimPrefix(clean, "/")
		if name != "." && name != "" {
			if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			if strings.Contains(path.Base(name), ".") {
				http.NotFound(w, r)
				return
			}
		}

		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	}), true
}
