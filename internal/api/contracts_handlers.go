package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/contracts"
)

// handleContract serves a wire-contract document — the spec this build
// actually implements. ANONYMOUS and read-only: a contract is public by
// nature, and an author integrating against meshcraft must be able to read it
// before they have any credential. The names are the protocol ("mdp",
// "agent") for the OpenAPI, and "<protocol>-schemas" for the generated JSON
// Schema it references.
func (s *Server) handleContract(w http.ResponseWriter, r *http.Request) {
	var name = chi.URLParam(r, "name")
	var doc, ok = contracts.Get(name)
	if !ok {
		var names = contracts.Names()
		sort.Strings(names)
		respondErr(w, http.StatusNotFound,
			"no such contract: known documents are "+strings.Join(names, ", "))
		return
	}
	w.Header().Set("Content-Type", doc.ContentType)
	// A contract changes only on deploy; let intermediaries hold it briefly.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(doc.Bytes)
}
