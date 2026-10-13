package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"
)

// maxAttachmentUpload bounds a single attachments request (all files combined).
// Generous: these stream straight to the project working dir on disk, not
// through the 8MB WebSocket frame, so large archives (zip/tgz) are fine.
const maxAttachmentUpload = 200 << 20 // 200 MiB

// attachmentResult is one saved file: its base name, the path relative to the
// project working dir (where the running claude can read it), and its size.
type attachmentResult struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// uploadAttachment streams uploaded files into the session's project working dir
// (.hub-uploads/<ts>/) so the running claude process can process them with its
// tools (unzip, tar, Read). It returns the saved paths; the composer references
// them by path in the next prompt. Images and small PDFs are sent inline as
// content blocks instead (see sessions_ws.go) and do not hit this endpoint.
func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no files"))
		return
	}
	// One subdir per request so a multi-file upload stays grouped and names from
	// different uploads never collide.
	batch := fmt.Sprintf("%d", time.Now().UTC().Unix())
	out := make([]attachmentResult, 0, len(files))
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		rel, err := s.mgr.SaveAttachment(sessionID, batch, fh.Filename, f)
		f.Close()
		if err != nil {
			writeNotFoundOr500(w, err)
			return
		}
		out = append(out, attachmentResult{Name: filepath.Base(fh.Filename), Path: rel, Size: fh.Size})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}
