package adminapi

import (
	"net/http"
	"strconv"

	"github.com/kube-dosh/crproxy/internal/store"
)

func (h *Handler) handleAuditList(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var beforeID int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		beforeID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || beforeID < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "before must be a non-negative integer")
			return
		}
	}
	events, err := h.Store.ListAuditEvents(r.Context(), store.AuditFilter{
		BeforeID: beforeID,
		Limit:    limit,
		Action:   r.URL.Query().Get("action"),
		ActorID:  r.URL.Query().Get("actor_id"),
		Result:   r.URL.Query().Get("result"),
	})
	if err != nil {
		h.Logger.Error("list audit events failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := map[string]any{"events": events}
	if len(events) == limit {
		response["nextBefore"] = events[len(events)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}
