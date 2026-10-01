// Package server exposes the metadata catalog over HTTP so the
// catalog can run as a local daemon usable without writing Go code.
//
// All handlers delegate to catalog.Catalog, which serializes commits
// under a single mutex: concurrent submissions are never lost and
// every response reflects one fully committed, self-consistent state.
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

// New returns an http.Handler serving the catalog's HTTP API.
//
//	POST   /v1/reports                    submit an EntitySpec report
//	GET    /v1/entities                   search (?tag=&owner=&keyword=)
//	GET    /v1/entities/{id}              current merged state
//	GET    /v1/entities/{id}/versions     version history
//	GET    /v1/entities/{id}/reports      per-source report log (?source=)
//	GET    /v1/entities/{id}/downstream   impact analysis
//	GET    /v1/entities/{id}/upstream     reverse impact analysis
//	POST   /v1/lineage                    register a lineage edge
func New(c *catalog.Catalog) http.Handler {
	h := &handler{c: c}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/reports", h.postReport)
	mux.HandleFunc("GET /v1/entities", h.search)
	mux.HandleFunc("GET /v1/entities/{id}", h.getEntity)
	mux.HandleFunc("GET /v1/entities/{id}/versions", h.versions)
	mux.HandleFunc("GET /v1/entities/{id}/reports", h.reports)
	mux.HandleFunc("GET /v1/entities/{id}/downstream", h.downstream)
	mux.HandleFunc("GET /v1/entities/{id}/upstream", h.upstream)
	mux.HandleFunc("POST /v1/lineage", h.postLineage)
	return mux
}

type handler struct {
	c *catalog.Catalog
}

// errorBody is the JSON shape of every error response; it mirrors
// catalog.CatalogError so clients can branch on reason and inspect
// conflicts without parsing messages.
type errorBody struct {
	Reason    string             `json:"reason"`
	Message   string             `json:"message"`
	Conflicts []catalog.Conflict `json:"conflicts,omitempty"`
}

func (h *handler) postReport(w http.ResponseWriter, r *http.Request) {
	var spec catalog.EntitySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cannot decode report: "+err.Error())
		return
	}
	e, err := h.c.Register(spec)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	q := catalog.SearchQuery{
		Keyword: r.URL.Query().Get("keyword"),
		Tag:     r.URL.Query().Get("tag"),
		Owner:   r.URL.Query().Get("owner"),
	}
	es, err := h.c.Search(q)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	if es == nil {
		es = []*catalog.Entity{}
	}
	writeJSON(w, http.StatusOK, es)
}

func (h *handler) getEntity(w http.ResponseWriter, r *http.Request) {
	e, err := h.c.Get(r.PathValue("id"))
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *handler) versions(w http.ResponseWriter, r *http.Request) {
	vs, err := h.c.History(r.PathValue("id"))
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

func (h *handler) reports(w http.ResponseWriter, r *http.Request) {
	rs, err := h.c.Reports(r.PathValue("id"), r.URL.Query().Get("source"))
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	if rs == nil {
		rs = []catalog.SourceReport{}
	}
	writeJSON(w, http.StatusOK, rs)
}

func (h *handler) downstream(w http.ResponseWriter, r *http.Request) {
	h.impact(w, r, "downstream")
}

func (h *handler) upstream(w http.ResponseWriter, r *http.Request) {
	h.impact(w, r, "upstream")
}

func (h *handler) impact(w http.ResponseWriter, r *http.Request, direction string) {
	id := r.PathValue("id")
	var (
		rep *catalog.ImpactReport
		err error
	)
	if direction == "downstream" {
		rep, err = h.c.Downstream(id)
	} else {
		rep, err = h.c.Upstream(id)
	}
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	if rep.EntityIDs == nil {
		rep.EntityIDs = []string{}
	}
	if rep.Edges == nil {
		rep.Edges = []catalog.LineageEdge{}
	}
	writeJSON(w, http.StatusOK, rep)
}

type lineageRequest struct {
	UpstreamID   string `json:"upstream_id"`
	DownstreamID string `json:"downstream_id"`
	Relation     string `json:"relation"`
}

func (h *handler) postLineage(w http.ResponseWriter, r *http.Request) {
	var req lineageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cannot decode lineage: "+err.Error())
		return
	}
	if err := h.c.AddLineage(req.UpstreamID, req.DownstreamID, req.Relation); err != nil {
		writeCatalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, reason, message string) {
	writeJSON(w, status, errorBody{Reason: reason, Message: message})
}

// writeCatalogError maps a catalog failure reason to a distinguishable
// HTTP status and preserves the structured conflicts in the body.
func writeCatalogError(w http.ResponseWriter, err error) {
	var ce *catalog.CatalogError
	if !errors.As(err, &ce) {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	status := http.StatusConflict
	switch ce.Reason {
	case catalog.ReasonInvalidSpec:
		status = http.StatusBadRequest
	case catalog.ReasonEntityNotFound:
		status = http.StatusNotFound
	case catalog.ReasonSelfEdge:
		status = http.StatusBadRequest
	}
	writeJSON(w, status, errorBody{
		Reason:    string(ce.Reason),
		Message:   ce.Message,
		Conflicts: ce.Conflicts,
	})
}
