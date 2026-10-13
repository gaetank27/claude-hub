package scriptstatus

// Screenshots: a script whose key has allow_images may upload an image next to
// its status reports (POST /webhook/script-status/{script}/image, same key, same
// IP allow-list, same bans). An image can show anything that was on the screen,
// so everything here is deliberately tight: opt-in per key, PNG/JPEG only (real
// type by magic bytes, decoded and re-encoded, which drops metadata and any
// appended payload), 1 MiB, a few per minute, the last imagesKeep per script and
// imageMaxAge at most, readable only through the cookie-gated /api/. The bytes
// are files (not database rows) so that they never end up in a database copy.
// An image is data for a human to look at: it is never handed to the assistant.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"system/internal/imgsafe"
)

const (
	maxImageBody   = 1 << 20 // upload cap (the proxies in front apply the same)
	maxImagePixels = 16_000_000
	maxImageDim    = 4096 // longest side kept; larger images are scaled down
	imagesKeep     = 240
	imageMaxAge    = 24 * time.Hour
	imageRate      = 6 // uploads/min/script
)

var imagePolicy = &imgsafe.Policy{MaxBytes: maxImageBody, MaxPixels: maxImagePixels, MaxDim: maxImageDim}

// ImageInfo describes a stored screenshot (never its bytes).
type ImageInfo struct {
	ID         int64  `json:"id"`
	Mime       string `json:"mime"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Size       int64  `json:"size"`
	ReceivedAt int64  `json:"receivedAt"`
}

func imageExt(mime string) string {
	if mime == "image/png" {
		return "png"
	}
	return "jpg"
}

// imagePath is built from a validated script name and a database id only.
func (s *Service) imagePath(script string, id int64, mime string) string {
	return filepath.Join(s.imgDir, script, fmt.Sprintf("%d.%s", id, imageExt(mime)))
}

// ReceiveImage handles POST /webhook/script-status/{script}/image: the raw image
// as the body, Content-Type image/jpeg or image/png.
func (s *Service) ReceiveImage(w http.ResponseWriter, r *http.Request) {
	script, match, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !match.AllowImages {
		jsonErr(w, http.StatusForbidden, "images not allowed for this key")
		return
	}
	s.mu.Lock()
	allowed := s.allow(s.irate, script, imageRate)
	s.mu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "60")
		jsonErr(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if ct != "image/jpeg" && ct != "image/png" {
		jsonErr(w, http.StatusUnsupportedMediaType, "content-type must be image/jpeg or image/png")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImageBody))
	if err != nil {
		jsonErr(w, http.StatusRequestEntityTooLarge, "image too large")
		return
	}
	// The declared type is not trusted: the magic bytes decide, and must agree.
	if f := imgsafe.Format(body); f == "" || "image/"+f != ct {
		jsonErr(w, http.StatusBadRequest, "invalid image")
		return
	}
	s.imgMu.Lock() // one decode at a time (bounded memory)
	img, err := imagePolicy.PrepareBytes(body)
	s.imgMu.Unlock()
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid image")
		return
	}
	info, err := s.storeImage(script, img)
	if err != nil {
		log.Printf("script-status: image %s: %v", script, err)
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_, _ = s.db.Exec(`UPDATE script_keys SET last_used=? WHERE id=?`, s.now().UnixMilli(), match.ID)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"ok":true,"id":%d,"width":%d,"height":%d,"keep":%d,"maxAgeHours":%d}`+"\n",
		info.ID, info.Width, info.Height, imagesKeep, int(imageMaxAge.Hours()))
}

func (s *Service) storeImage(script string, img *imgsafe.Image) (ImageInfo, error) {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	now := s.now().UnixMilli()
	info := ImageInfo{Mime: img.Mime, Width: img.Width, Height: img.Height, Size: int64(len(img.Data)), ReceivedAt: now}
	dir := filepath.Join(s.imgDir, script)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return info, err
	}
	res, err := s.db.Exec(`INSERT INTO script_images(script,mime,width,height,size,received_at) VALUES(?,?,?,?,?,?)`,
		script, info.Mime, info.Width, info.Height, info.Size, now)
	if err != nil {
		return info, err
	}
	info.ID, _ = res.LastInsertId()
	path := s.imagePath(script, info.ID, info.Mime)
	if err := os.WriteFile(path, img.Data, 0o600); err != nil {
		_ = os.Remove(path)
		_, _ = s.db.Exec(`DELETE FROM script_images WHERE id=?`, info.ID)
		return info, err
	}
	s.purgeImagesLocked(script)
	return info, nil
}

// purgeImagesLocked drops what is past imageMaxAge or beyond the imagesKeep
// newest (script "" = every script, age only), then the files left without a row.
func (s *Service) purgeImagesLocked(script string) {
	cutoff := s.now().Add(-imageMaxAge).UnixMilli()
	q, args := `SELECT id,script,mime FROM script_images WHERE received_at<?`, []any{cutoff}
	if script != "" {
		q = `SELECT id,script,mime FROM script_images WHERE script=? AND (received_at<? OR id NOT IN
			(SELECT id FROM script_images WHERE script=? ORDER BY id DESC LIMIT ?))`
		args = []any{script, cutoff, script, imagesKeep}
	}
	s.deleteImages(q, args...)
	s.sweepOrphans(script)
}

func (s *Service) deleteImages(query string, args ...any) int {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return 0
	}
	type gone struct {
		id           int64
		script, mime string
	}
	var del []gone
	for rows.Next() {
		var g gone
		if rows.Scan(&g.id, &g.script, &g.mime) == nil {
			del = append(del, g)
		}
	}
	rows.Close()
	for _, g := range del {
		if ValidScript(g.script) {
			_ = os.Remove(s.imagePath(g.script, g.id, g.mime))
		}
		_, _ = s.db.Exec(`DELETE FROM script_images WHERE id=?`, g.id)
	}
	return len(del)
}

// sweepOrphans removes files that have no row any more (a crash between the two
// deletions, a restored database…), so nothing outlives its retention on disk.
func (s *Service) sweepOrphans(script string) {
	dirs := []string{script}
	if script == "" {
		dirs = dirs[:0]
		entries, _ := os.ReadDir(s.imgDir)
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	for _, d := range dirs {
		dir := filepath.Join(s.imgDir, d)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		known := map[string]bool{}
		if ValidScript(d) {
			infos, _ := s.imageRows(d, false)
			for _, i := range infos {
				known[filepath.Base(s.imagePath(d, i.ID, i.Mime))] = true
			}
		}
		left := 0
		for _, f := range files {
			if known[f.Name()] {
				left++
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, f.Name()))
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
}

// PurgeImages applies the age limit to every script (called every minute by Run,
// so screenshots also expire when their script has stopped sending).
func (s *Service) PurgeImages() {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	s.purgeImagesLocked("")
}

func (s *Service) imageRows(script string, freshOnly bool) ([]ImageInfo, error) {
	cutoff := int64(0)
	if freshOnly {
		cutoff = s.now().Add(-imageMaxAge).UnixMilli()
	}
	rows, err := s.db.Query(`SELECT id,mime,width,height,size,received_at FROM script_images
		WHERE script=? AND received_at>=? ORDER BY id DESC LIMIT ?`, script, cutoff, imagesKeep*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImageInfo{}
	for rows.Next() {
		var i ImageInfo
		if err := rows.Scan(&i.ID, &i.Mime, &i.Width, &i.Height, &i.Size, &i.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ListImages returns the screenshots of a script, newest first. Expired ones are
// never listed, even if the purge has not run yet.
func (s *Service) ListImages(script string) ([]ImageInfo, error) {
	if !ValidScript(script) {
		return []ImageInfo{}, nil
	}
	out, err := s.imageRows(script, true)
	if err == nil && len(out) > imagesKeep {
		out = out[:imagesKeep]
	}
	return out, err
}

// DeleteImages removes every screenshot of a script now.
func (s *Service) DeleteImages(script string) (int, error) {
	if !ValidScript(script) {
		return 0, errors.New("invalid script")
	}
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	n := s.deleteImages(`SELECT id,script,mime FROM script_images WHERE script=?`, script)
	s.sweepOrphans(script)
	return n, nil
}

// Admin handlers: mounted under /api/ (Hub login required), never public.

// ImageListHandler: GET /api/script-images/{script}.
func (s *Service) ImageListHandler(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListImages(r.PathValue("script"))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// ImageGetHandler: GET /api/script-images/{script}/{id} — the image itself.
func (s *Service) ImageGetHandler(w http.ResponseWriter, r *http.Request) {
	script := r.PathValue("script")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if !ValidScript(script) || err != nil || id <= 0 {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	var mime string
	var recv int64
	if err := s.db.QueryRow(`SELECT mime,received_at FROM script_images WHERE id=? AND script=?`, id, script).Scan(&mime, &recv); err != nil ||
		recv < s.now().Add(-imageMaxAge).UnixMilli() || (mime != "image/jpeg" && mime != "image/png") {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	data, err := os.ReadFile(s.imagePath(script, id, mime))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s-%d.%s"`, script, id, imageExt(mime)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ImageDeleteHandler: DELETE /api/script-images/{script} — forget them all now.
func (s *Service) ImageDeleteHandler(w http.ResponseWriter, r *http.Request) {
	n, err := s.DeleteImages(r.PathValue("script"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, map[string]int{"deleted": n})
}
