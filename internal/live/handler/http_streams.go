package handler

import (
	"context"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/live"
)

func (s *Server) listStreams(w http.ResponseWriter, r *http.Request) {
	limit, offset := live.ClampPage(pagingFrom(r))

	found, total, err := s.svc.ListLive(r.Context(), limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	streams := make([]streamResponse, 0, len(found))
	for _, st := range found {
		streams = append(streams, toStreamResponse(st))
	}
	writeJSON(w, http.StatusOK, listResponse{Streams: streams, Total: total, Limit: limit, Offset: offset})
}

func (s *Server) getStream(w http.ResponseWriter, r *http.Request) {
	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStreamResponse(found))
}

func (s *Server) scheduleStream(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a stream belongs to a shop"})
		return
	}

	var req scheduleRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	created, err := s.svc.Schedule(r.Context(), host, req.Title)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toStreamResponse(created))
}

func (s *Server) startStream(w http.ResponseWriter, r *http.Request) {
	s.hostAction(w, r, s.svc.Start)
}

func (s *Server) endStream(w http.ResponseWriter, r *http.Request) {
	s.hostAction(w, r, s.svc.End)
}

// hostAction is shared so the two transitions cannot drift apart on who is
// allowed to perform them.
func (s *Server) hostAction(
	w http.ResponseWriter, r *http.Request,
	move func(ctx context.Context, id, hostUserID string) (live.Stream, error),
) {
	host, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a stream belongs to a shop"})
		return
	}

	moved, err := move(r.Context(), r.PathValue("id"), host)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStreamResponse(moved))
}

func (s *Server) featureProduct(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a stream belongs to a shop"})
		return
	}

	var req featureRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	updated, err := s.svc.Feature(r.Context(), r.PathValue("id"), host, req.ProductID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStreamResponse(updated))
}
