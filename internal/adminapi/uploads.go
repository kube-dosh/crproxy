package adminapi

import (
	"errors"
	"net/http"

	"crproxy/internal/store"
)

func (h *Handler) handleUploadsActive(w http.ResponseWriter, r *http.Request, _ *principal) {
	h.respondUploads(w, r, true)
}

func (h *Handler) handleUploadsList(w http.ResponseWriter, r *http.Request, _ *principal) {
	h.respondUploads(w, r, false)
}

func (h *Handler) respondUploads(w http.ResponseWriter, r *http.Request, activeOnly bool) {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	uploads, err := h.Store.ListUploads(r.Context(), activeOnly, limit)
	if err != nil {
		h.Logger.Error("list uploads failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": uploads, "total": len(uploads)})
}

func (h *Handler) handleUploadDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	uploadID := r.PathValue("id")
	upload, err := h.Store.UploadByID(r.Context(), uploadID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "upload_not_found", "upload not found")
		return
	}
	if err != nil {
		h.Logger.Error("read upload failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.Store.DeleteUpload(r.Context(), uploadID); err != nil {
		h.Logger.Error("delete upload record failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.cas.DeleteUpload(uploadID); err != nil {
		h.Logger.Error("delete upload file failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "upload.cancel", "upload", uploadID, "success", "")
	writeJSON(w, http.StatusAccepted, map[string]any{"deleted": true, "uploadId": upload.ID})
}
