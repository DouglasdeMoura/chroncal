package testutil

import (
	"fmt"
	"io"
	"net/http"
	"sync"
)

// GenericCalDAV is a CalDAV test server handler that keeps a UID unique per
// collection only, as most servers do (iCloud keeps it unique per
// account). A PUT stores the object, and a DELETE removes it. Every other
// method answers 405.
type GenericCalDAV struct {
	mu      sync.Mutex
	objects map[string]bool
	tag     int
}

// NewGenericCalDAV returns an empty GenericCalDAV.
func NewGenericCalDAV() *GenericCalDAV {
	return &GenericCalDAV{objects: map[string]bool{}}
}

func (g *GenericCalDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		_, _ = io.ReadAll(r.Body)
		g.objects[r.URL.Path] = true
		g.tag++
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, g.tag))
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if !g.objects[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(g.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Paths returns the paths of the stored objects, in no fixed order.
func (g *GenericCalDAV) Paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.objects))
	for p := range g.objects {
		out = append(out, p)
	}
	return out
}
