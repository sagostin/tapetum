package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/transcode"
)

// parseTimeParam parses an RFC 3339 query param.
func parseTimeParam(r *http.Request, key string) (time.Time, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return time.Time{}, fmt.Errorf("missing %s", key)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %v", key, err)
	}
	return t, nil
}

// --- timeline / availability ----------------------------------------------

type timeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// mergeRanges collapses overlapping/adjacent segment ranges into a coverage
// mask (docs/03-api.md timeline response).
func mergeRanges(segs []*record.Segment) []timeRange {
	out := []timeRange{}
	for _, s := range segs {
		if len(out) > 0 && !s.Start.After(out[len(out)-1].End) {
			if s.End.After(out[len(out)-1].End) {
				out[len(out)-1].End = s.End
			}
			continue
		}
		out = append(out, timeRange{Start: s.Start, End: s.End})
	}
	return out
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	camID := r.URL.Query().Get("camera")
	if camID == "" {
		Error(w, http.StatusBadRequest, "bad_request", "camera is required")
		return
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	from, err := parseTimeParam(r, "from")
	if err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	to, err := parseTimeParam(r, "to")
	if err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	buckets := 200
	if b := r.URL.Query().Get("buckets"); b != "" {
		if n, err := strconv.Atoi(b); err == nil && n > 0 && n <= 2000 {
			buckets = n
		}
	}

	segs, err := s.segs.SegmentsInRange(r.Context(), camID, from, to)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// density: fraction of each bucket covered by has_motion segments (0–1).
	density := make([]float64, buckets)
	span := to.Sub(from)
	if span <= 0 {
		span = time.Second
	}
	bucketDur := span / time.Duration(buckets)
	for _, seg := range segs {
		if !seg.HasMotion {
			continue
		}
		a := seg.Start
		if a.Before(from) {
			a = from
		}
		b := seg.End
		if b.After(to) {
			b = to
		}
		first := int(a.Sub(from) / bucketDur)
		last := int((b.Sub(from) - 1) / bucketDur)
		if last >= buckets {
			last = buckets - 1
		}
		for i := first; i <= last && i >= 0; i++ {
			bs := from.Add(time.Duration(i) * bucketDur)
			be := bs.Add(bucketDur)
			ovA := a
			if bs.After(ovA) {
				ovA = bs
			}
			ovB := b
			if be.Before(ovB) {
				ovB = be
			}
			if ovB.After(ovA) {
				density[i] += ovB.Sub(ovA).Seconds() / bucketDur.Seconds()
			}
		}
	}
	for i := range density {
		if density[i] > 1 {
			density[i] = 1
		}
	}

	JSON(w, http.StatusOK, map[string]any{
		"camera_id": camID,
		"from":      from,
		"to":        to,
		"buckets":   buckets,
		"density":   density,
		"recorded":  mergeRanges(segs),
		"events":    []any{}, // phase 3
	})
}

func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	camID := r.URL.Query().Get("camera")
	if camID == "" {
		Error(w, http.StatusBadRequest, "bad_request", "camera is required")
		return
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	from, err1 := parseTimeParam(r, "from")
	to, err2 := parseTimeParam(r, "to")
	if err1 != nil || err2 != nil {
		Error(w, http.StatusBadRequest, "bad_request", "from and to (RFC 3339) are required")
		return
	}
	segs, err := s.segs.SegmentsInRange(r.Context(), camID, from, to)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	gaps, err := s.segs.GapsInRange(r.Context(), camID, from, to)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{
		"recorded": mergeRanges(segs),
		"gaps":     gaps,
	})
}

// --- HLS playlists ----------------------------------------------------------

// buildPlaylist renders a media playlist for segments (docs/05). With
// transcode=true the init + segment URLs point at lazily transcoded H.264
// variants (H.265 cameras, unsupported browsers).
func buildPlaylist(camID string, segs []*record.Segment, live bool, transcode bool) string {
	var b []byte
	p := func(format string, args ...any) {
		b = append(b, fmt.Sprintf(format, args...)...)
	}
	p("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
	if transcode {
		p("#EXT-X-MAP:URI=\"/api/v1/playback/%s/init.mp4?transcode=1\"\n", camID)
	} else {
		p("#EXT-X-MAP:URI=\"/api/v1/playback/%s/init.mp4\"\n", camID)
	}
	if live {
		p("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	}
	for _, s := range segs {
		dur := s.End.Sub(s.Start).Seconds()
		if dur <= 0 {
			dur = 6
		}
		p("#EXT-X-PROGRAM-DATE-TIME:%s\n", s.Start.UTC().Format("2006-01-02T15:04:05.000Z"))
		p("#EXTINF:%.3f,\n", dur)
		if transcode {
			p("/api/v1/segments/%s?transcode=h264\n", s.ID)
		} else {
			p("/api/v1/segments/%s\n", s.ID)
		}
	}
	if !live {
		p("#EXT-X-ENDLIST\n")
	}
	return string(b)
}

// wantsTranscode reports whether the client asked for transcoded H.264.
func wantsTranscode(r *http.Request) bool {
	v := r.URL.Query().Get("transcode")
	return v == "1" || v == "true" || v == "h264"
}

func (s *Server) playbackPlaylist(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraForParam(w, r, "cameraId")
	if cam == nil {
		return
	}
	start, err := parseTimeParam(r, "start")
	if err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	end, err := parseTimeParam(r, "end")
	if err != nil {
		end = time.Now()
	}
	segs, err := s.segs.SegmentsInRange(r.Context(), cam.ID, start, end)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(segs) == 0 {
		Error(w, http.StatusNotFound, "no_recordings", "no recordings in range")
		return
	}
	servePlaylist(w, buildPlaylist(cam.ID, segs, false, wantsTranscode(r)))
}

func (s *Server) livePlaylist(w http.ResponseWriter, r *http.Request) {
	camID := chi.URLParam(r, "cameraId")
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	segs, err := s.segs.RecentSegments(r.Context(), camID, 4)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(segs) == 0 {
		Error(w, http.StatusNotFound, "no_recordings", "no recent segments")
		return
	}
	servePlaylist(w, buildPlaylist(camID, segs, true, wantsTranscode(r)))
}

func servePlaylist(w http.ResponseWriter, pl string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(pl))
}

// initMP4 serves the camera's fMP4 init segment (from status_detail.init).
// With ?transcode=1 it serves the cached transcoded H.264 init, generating
// it from the newest segment on a cache miss.
func (s *Server) initMP4(w http.ResponseWriter, r *http.Request) {
	camID := chi.URLParam(r, "cameraId")
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	cam, err := s.cams.Get(r.Context(), camID)
	if err != nil {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	if wantsTranscode(r) {
		s.serveTranscodeInit(w, r, cam)
		return
	}
	initBytes, err := camInitBytes(cam)
	if err != nil {
		Error(w, http.StatusNotFound, "no_init",
			"codec init unavailable — camera has not streamed yet")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(initBytes)
}

// camInitBytes decodes the base64 fMP4 init stored in status_detail.
func camInitBytes(cam *camera.Camera) ([]byte, error) {
	initB64, _ := cam.StatusDetail["init"].(string)
	if initB64 == "" {
		return nil, errors.New("no init")
	}
	return base64.StdEncoding.DecodeString(initB64)
}

// serveTranscodeInit serves (or builds) the transcoded H.264 init segment.
func (s *Server) serveTranscodeInit(w http.ResponseWriter, r *http.Request, cam *camera.Camera) {
	if cam.PlaybackTranscode == "never" {
		Error(w, http.StatusBadRequest, "transcode_disabled",
			"playback_transcode is 'never' for this camera")
		return
	}
	path := s.transcode.InitPath(cam.ID)
	if _, err := os.Stat(path); err != nil {
		// build from the newest segment (writes the tinit cache as a side effect)
		initBytes, err := camInitBytes(cam)
		if err != nil {
			Error(w, http.StatusNotFound, "no_init",
				"codec init unavailable — camera has not streamed yet")
			return
		}
		segs, err := s.segs.RecentSegments(r.Context(), cam.ID, 1)
		if err != nil || len(segs) == 0 {
			Error(w, http.StatusNotFound, "no_recordings", "no segments to transcode")
			return
		}
		// drop any stale fragment cache so the build rewrites tinit
		s.transcode.Evict(segs[0].ID)
		if _, err := s.transcode.Segment(r.Context(), segs[0], initBytes, s.resolve); err != nil {
			Error(w, http.StatusInternalServerError, "transcode_failed", err.Error())
			return
		}
	}
	f, err := os.Open(path)
	if err != nil {
		Error(w, http.StatusInternalServerError, "transcode_failed", err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	io.Copy(w, f)
}

// serveSegment streams a segment file with Range support. Local segments are
// sent with sendfile; S3 segments redirect to a presigned GET (docs/06:
// reads never proxy through Go). ?transcode=h264 serves a lazily transcoded
// fragment (H.265 cameras). Segments are immutable → year-long cache.
func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	seg, err := s.segs.GetSegment(r.Context(), id)
	if err != nil {
		Error(w, http.StatusNotFound, "segment_not_found", "segment not found")
		return
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, seg.CameraID) {
		Error(w, http.StatusNotFound, "segment_not_found", "segment not found")
		return
	}

	if wantsTranscode(r) {
		s.serveTranscodedSegment(w, r, seg)
		return
	}

	if seg.Storage == "s3" {
		b, err := s.s3m.Backend(r.Context())
		if err != nil || b == nil {
			Error(w, http.StatusServiceUnavailable, "s3_unavailable",
				"segment is in S3 but S3 is not configured")
			return
		}
		url, err := b.Presign(r.Context(), seg.Path, 15*time.Minute)
		if err != nil {
			Error(w, http.StatusInternalServerError, "presign_failed", err.Error())
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	rc, err := s.backend.Open(r.Context(), seg.Path)
	if err != nil {
		Error(w, http.StatusNotFound, "segment_missing", "segment file missing")
		return
	}
	defer rc.Close()
	f, ok := rc.(*os.File)
	if !ok {
		Error(w, http.StatusInternalServerError, "internal", "unexpected backend reader")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "immutable, max-age=31536000")
	http.ServeContent(w, r, "", seg.Start, f)
}

// serveTranscodedSegment serves (or builds) the H.264 transcoded fragment.
func (s *Server) serveTranscodedSegment(w http.ResponseWriter, r *http.Request, seg *record.Segment) {
	cam, err := s.cams.Get(r.Context(), seg.CameraID)
	if err != nil {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	if cam.PlaybackTranscode == "never" {
		Error(w, http.StatusBadRequest, "transcode_disabled",
			"playback_transcode is 'never' for this camera")
		return
	}
	// already H.264 — serve the original
	if codec, _ := cam.StatusDetail["codec"].(string); codec == "h264" {
		if seg.Storage != "local" {
			Error(w, http.StatusBadRequest, "transcode_unneeded",
				"segment is already H.264")
			return
		}
		rc, err := s.backend.Open(r.Context(), seg.Path)
		if err != nil {
			Error(w, http.StatusNotFound, "segment_missing", "segment file missing")
			return
		}
		defer rc.Close()
		if f, ok := rc.(*os.File); ok {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "immutable, max-age=31536000")
			http.ServeContent(w, r, "", seg.Start, f)
			return
		}
	}
	initBytes, err := camInitBytes(cam)
	if err != nil {
		Error(w, http.StatusNotFound, "no_init",
			"codec init unavailable — camera has not streamed yet")
		return
	}
	path, err := s.transcode.Segment(r.Context(), seg, initBytes, s.resolve)
	if errors.Is(err, transcode.ErrBusy) {
		Error(w, http.StatusServiceUnavailable, "transcode_busy", err.Error())
		return
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "transcode_failed", err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		Error(w, http.StatusInternalServerError, "transcode_failed", err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "immutable, max-age=31536000")
	http.ServeContent(w, r, "", seg.Start, f)
}

// --- MJPEG fallback ---------------------------------------------------------

// mjpeg streams multipart JPEGs at ~2 fps from the snapshot cache.
func (s *Server) mjpeg(w http.ResponseWriter, r *http.Request) {
	camID := chi.URLParam(r, "cameraId")
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary=tapetum")
	w.Header().Set("Cache-Control", "no-store")
	fl, ok := w.(http.Flusher)
	if !ok {
		Error(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		jpg, err := s.ingest.Snapshot(r.Context(), camID)
		if err == nil {
			fmt.Fprintf(w, "--tapetum\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jpg))
			w.Write(jpg)
			w.Write([]byte("\r\n"))
			fl.Flush()
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// --- protect ----------------------------------------------------------------

func (s *Server) protectSegments(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CameraID string `json:"camera_id"`
		Start    string `json:"start"`
		End      string `json:"end"`
	}
	if err := Decode(w, r, &b); err != nil || b.CameraID == "" {
		Error(w, http.StatusBadRequest, "bad_request", "camera_id, start, end required")
		return
	}
	start, err1 := time.Parse(time.RFC3339, b.Start)
	end, err2 := time.Parse(time.RFC3339, b.End)
	if err1 != nil || err2 != nil {
		Error(w, http.StatusBadRequest, "bad_request", "start/end must be RFC 3339")
		return
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, b.CameraID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	n, err := s.segs.ProtectRange(r.Context(), b.CameraID, start, end)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"protected": n})
}

func (s *Server) unprotectSegment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.segs.Unprotect(r.Context(), id); err != nil {
		Error(w, http.StatusNotFound, "segment_not_found", "segment not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
