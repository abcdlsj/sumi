package server

import (
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
)

// readUpload parses a single-file multipart upload capped at domain.MaxAttachmentBytes.
func readUpload(w http.ResponseWriter, r *http.Request) (name, mediaType string, content io.Reader, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxAttachmentBytes+(1<<20))
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, domain.NewError(domain.CodeInvalidArgument, "multipart field 'file' is required"))
		return "", "", nil, false
	}
	name = header.Filename
	if name == "" || strings.HasSuffix(name, "/") {
		name = "file"
	}
	name = filepath.Base(name)
	return name, header.Header.Get("Content-Type"), file, true
}

func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request, viewer domain.Member, attachmentID string) {
	content, err := s.svc.OpenAttachment(r.Context(), viewer, attachmentID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer content.File.Close()

	info, statErr := content.File.Stat()
	if statErr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.Header().Set("ETag", `"`+attachmentID+`"`)
		w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Content-Type", app.AttachmentContentType(content.Attachment))
	w.Header().Set("Content-Disposition", contentDisposition(content.Attachment.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == `"`+attachmentID+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "", info.ModTime(), content.File)
}

// contentDisposition builds an RFC 6266 header that survives non-ASCII file names.
func contentDisposition(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if safe == "" {
		safe = "attachment"
	}
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, safe)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + mime.QEncoding.Encode("UTF-8", safe)
}

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	name, mediaType, content, ok := readUpload(w, r)
	if !ok {
		return
	}
	att, err := s.svc.UploadAttachment(r.Context(), actor, name, mediaType, content)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": att})
}

func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	_, viewer, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	s.serveAttachment(w, r, viewer, chi.URLParam(r, "attachmentID"))
}

func (s *Server) handleAgentUploadAttachment(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	name, mediaType, content, ok := readUpload(w, r)
	if !ok {
		return
	}
	att, err := s.svc.UploadAttachment(r.Context(), agent, name, mediaType, content)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": att})
}

func (s *Server) handleAgentDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	s.serveAttachment(w, r, agent, chi.URLParam(r, "attachmentID"))
}
