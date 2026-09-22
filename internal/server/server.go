// Package server 把内存目录以 HTTP 形式暴露为本地服务。
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/h3yow/metadata-catalog-go/internal/catalog"
)

// Server 包装目录与 HTTP handler。
type Server struct {
	cat *catalog.Catalog
	mux *http.ServeMux
}

// New 创建 HTTP 服务并注册路由。
func New(c *catalog.Catalog) *Server {
	s := &Server{cat: c, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	s.mux.HandleFunc("POST /v1/entities", s.registerEntity)
	s.mux.HandleFunc("GET /v1/entities/{name}", s.getEntity)
	s.mux.HandleFunc("GET /v1/entities/{name}/history", s.getHistory)
	s.mux.HandleFunc("GET /v1/search", s.search)

	s.mux.HandleFunc("POST /v1/lineage", s.addLineage)
	s.mux.HandleFunc("GET /v1/lineage/{direction}/{name}", s.lineage)
}

// Handler 返回可用于 http.ListenAndServe 的 handler。
func (s *Server) Handler() http.Handler {
	return s.mux
}

type errorBody struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// writeError 把目录的 *catalog.RejectError 映射为稳定的 HTTP 状态与错误体，
// reason 字段与内部 RejectReason 一一对应，调用方可直接断言。
func writeError(w http.ResponseWriter, err error) {
	var re *catalog.RejectError
	if errors.As(err, &re) {
		status := http.StatusBadRequest
		switch re.Reason {
		case catalog.ReasonEntityNotFound, catalog.ReasonFieldNotFound:
			status = http.StatusNotFound
		case catalog.ReasonInvalidInput:
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, errorBody{Reason: string(re.Reason), Detail: re.Detail})
		return
	}
	log.Printf("unexpected error: %v", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{Reason: "internal_error", Detail: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) registerEntity(w http.ResponseWriter, r *http.Request) {
	var spec catalog.EntitySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Reason: string(catalog.ReasonInvalidInput), Detail: "invalid JSON body: " + err.Error()})
		return
	}
	res, err := s.cat.Register(spec)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if res.Change == catalog.ChangeCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	ent, err := s.cat.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ent)
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	hist, err := s.cat.History(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := catalog.Query{
		Keyword: r.URL.Query().Get("keyword"),
		Kind:    r.URL.Query().Get("kind"),
	}
	q.Tags = r.URL.Query()["tag"]
	q.Owners = r.URL.Query()["owner"]
	writeJSON(w, http.StatusOK, s.cat.Search(q))
}

type lineageRequest struct {
	Upstream   string            `json:"upstream"`
	Downstream string            `json:"downstream"`
	Source     string            `json:"source"`
	Properties map[string]string `json:"properties"`
}

func (s *Server) addLineage(w http.ResponseWriter, r *http.Request) {
	var req lineageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Reason: string(catalog.ReasonInvalidInput), Detail: "invalid JSON body: " + err.Error()})
		return
	}
	res, err := s.cat.AddLineage(req.Upstream, req.Downstream, req.Source, req.Properties)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) lineage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	switch r.PathValue("direction") {
	case "downstream":
		nodes, err := s.cat.Downstream(name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entity": name, "downstream": nodes})
	case "upstream":
		nodes, err := s.cat.Upstream(name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entity": name, "upstream": nodes})
	default:
		writeJSON(w, http.StatusBadRequest, errorBody{Reason: string(catalog.ReasonInvalidInput), Detail: "direction must be upstream or downstream"})
	}
}
