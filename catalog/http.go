package catalog

import (
	"encoding/json"
	"net/http"
)

// HTTP API
//
// NewHTTPHandler exposes the whole catalog over plain HTTP/JSON so it
// can be driven without writing Go code:
//
//	POST   /api/entities                              register a report
//	GET    /api/entities?tag=&owner=&keyword=         search
//	GET    /api/entities/{kind}/{name}                current merged state
//	GET    /api/entities/{kind}/{name}/history        version history
//	GET    /api/entities/{kind}/{name}/sources        per-source contributions
//	GET    /api/entities/{kind}/{name}/downstream     impact analysis
//	GET    /api/entities/{kind}/{name}/upstream       reverse impact analysis
//	POST   /api/lineage                               register a lineage edge
//
// All endpoints are served by the same mutex-protected Catalog, so
// concurrent requests always observe a fully committed, self
// consistent state.
//
// Errors are JSON: {"reason": "...", "message": "...", "conflicts":
// [...]} with a status code derived from the reason.

// registerRequest is the body of POST /api/entities: an EntitySpec
// plus an optional optimistic-concurrency token.
type registerRequest struct {
	EntitySpec
	// ExpectedVersion follows RegisterAt semantics: omitted means
	// unconditional, 0 create-only, >0 require exactly that version.
	ExpectedVersion *int `json:"expected_version"`
}

// lineageRequest is the body of POST /api/lineage.
type lineageRequest struct {
	UpstreamID   string `json:"upstream_id"`
	DownstreamID string `json:"downstream_id"`
	Relation     string `json:"relation"`
}

// errorResponse is the JSON shape of every API error.
type errorResponse struct {
	Reason    Reason     `json:"reason"`
	Message   string     `json:"message"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// NewHTTPHandler returns an http.Handler serving the catalog API.
func NewHTTPHandler(c *Catalog) http.Handler {
	mux := http.NewServeMux()
	h := &httpAPI{c: c}

	mux.HandleFunc("POST /api/entities", h.register)
	mux.HandleFunc("GET /api/entities", h.search)
	mux.HandleFunc("GET /api/entities/{kind}/{name}", h.get)
	mux.HandleFunc("GET /api/entities/{kind}/{name}/history", h.history)
	mux.HandleFunc("GET /api/entities/{kind}/{name}/sources", h.sources)
	mux.HandleFunc("GET /api/entities/{kind}/{name}/downstream", h.downstream)
	mux.HandleFunc("GET /api/entities/{kind}/{name}/upstream", h.upstream)
	mux.HandleFunc("POST /api/lineage", h.addLineage)
	return mux
}

type httpAPI struct {
	c *Catalog
}

func (h *httpAPI) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errf(ReasonInvalidSpec, "invalid JSON body: %v", err))
		return
	}
	expected := -1
	if req.ExpectedVersion != nil {
		expected = *req.ExpectedVersion
	}
	e, err := h.c.RegisterAt(req.EntitySpec, expected)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, statusForReason("", true), e)
}

func (h *httpAPI) search(w http.ResponseWriter, r *http.Request) {
	q := SearchQuery{
		Keyword: r.URL.Query().Get("keyword"),
		Tag:     r.URL.Query().Get("tag"),
		Owner:   r.URL.Query().Get("owner"),
	}
	res, err := h.c.Search(q)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *httpAPI) get(w http.ResponseWriter, r *http.Request) {
	e, err := h.c.Get(entityID(r.PathValue("kind"), r.PathValue("name")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *httpAPI) history(w http.ResponseWriter, r *http.Request) {
	hist, err := h.c.History(entityID(r.PathValue("kind"), r.PathValue("name")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

func (h *httpAPI) sources(w http.ResponseWriter, r *http.Request) {
	contribs, err := h.c.Contributions(entityID(r.PathValue("kind"), r.PathValue("name")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contribs)
}

func (h *httpAPI) downstream(w http.ResponseWriter, r *http.Request) {
	rep, err := h.c.Downstream(entityID(r.PathValue("kind"), r.PathValue("name")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *httpAPI) upstream(w http.ResponseWriter, r *http.Request) {
	rep, err := h.c.Upstream(entityID(r.PathValue("kind"), r.PathValue("name")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *httpAPI) addLineage(w http.ResponseWriter, r *http.Request) {
	var req lineageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errf(ReasonInvalidSpec, "invalid JSON body: %v", err))
		return
	}
	if err := h.c.AddLineage(req.UpstreamID, req.DownstreamID, req.Relation); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "registered"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	ce, ok := AsCatalogError(err)
	if !ok {
		ce = &CatalogError{Reason: "internal", Message: err.Error()}
	}
	writeJSON(w, statusForReason(ce.Reason, false), errorResponse{
		Reason:    ce.Reason,
		Message:   ce.Message,
		Conflicts: ce.Conflicts,
	})
}

// statusForReason maps a failure reason to an HTTP status. With
// ok=true it returns the success status for register (201).
func statusForReason(r Reason, ok bool) int {
	if ok {
		return http.StatusCreated
	}
	switch r {
	case ReasonInvalidSpec, ReasonSelfEdge:
		return http.StatusBadRequest
	case ReasonEntityNotFound:
		return http.StatusNotFound
	case ReasonDuplicateReport, ReasonVersionConflict, ReasonDuplicateEdge:
		return http.StatusConflict
	case ReasonIncompatibleChange, ReasonSourceConflict:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
