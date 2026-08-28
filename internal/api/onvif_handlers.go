package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/onvif"
)

// onvifCreds resolves the ONVIF endpoint + credentials for a camera.
func (s *Server) onvifCreds(r *http.Request, cam *camera.Camera) (endpoint, user, pass, profile string, err error) {
	if cam.OnvifEndpoint == nil || *cam.OnvifEndpoint == "" {
		return "", "", "", "", onvif.ErrNoOnvif
	}
	pass, err = s.cams.DecryptPassword(cam.PasswordEnc)
	if err != nil {
		return "", "", "", "", err
	}
	profile = ""
	if cam.OnvifProfile != nil {
		profile = *cam.OnvifProfile
	}
	return *cam.OnvifEndpoint, cam.Username, pass, profile, nil
}

// onvifProfileOrFirst resolves the PTZ/imaging profile token, falling back
// to the device's first profile when the camera row has none stored.
func (s *Server) onvifProfileOrFirst(r *http.Request, cam *camera.Camera,
	endpoint, user, pass, profile string,
) (string, error) {
	if profile != "" {
		return profile, nil
	}
	res, err := onvif.Probe(r.Context(), endpoint, user, pass)
	if err != nil {
		return "", err
	}
	if len(res.Profiles) == 0 {
		return "", errors.New("onvif: device has no media profiles")
	}
	return res.Profiles[0].Token, nil
}

// discover runs an ONVIF WS-Discovery scan of the LAN (rate-limited, docs/03).
func (s *Server) discoverCameras(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if !s.probeLimiter.allow(u.ID) {
		Error(w, http.StatusTooManyRequests, "rate_limited", "discover limit: 10/min")
		return
	}
	var b struct {
		TimeoutS int `json:"timeout_s"`
	}
	_ = Decode(w, r, &b) // body optional
	timeout := 5 * time.Second
	if b.TimeoutS > 0 && b.TimeoutS <= 30 {
		timeout = time.Duration(b.TimeoutS) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+5*time.Second)
	defer cancel()
	devs, err := onvif.Discover(ctx, timeout)
	if err != nil {
		Error(w, http.StatusBadGateway, "discovery_failed", err.Error())
		return
	}
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.discover", IP: clientIP(r),
		Detail: map[string]any{"found": len(devs)},
	})
	JSON(w, http.StatusOK, map[string]any{"devices": devs})
}

// onvifSync pulls profiles/PTZ/imaging caps from the device and updates the
// camera's stream URLs + onvif_profile + has_ptz (docs/03-api.md).
func (s *Server) onvifSync(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	endpoint, user, pass, _, err := s.onvifCreds(r, cam)
	if errors.Is(err, onvif.ErrNoOnvif) {
		Error(w, http.StatusBadRequest, "no_onvif", err.Error())
		return
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := onvif.Probe(ctx, endpoint, user, pass)
	if err != nil {
		Error(w, http.StatusBadGateway, "onvif_unreachable", err.Error())
		return
	}
	params := camera.UpdateParams{HasPTZ: &res.HasPTZ}
	if len(res.Profiles) > 0 {
		main := res.Profiles[0]
		params.OnvifProfile = &main.Token
		if main.StreamURI != "" {
			params.MainURL = &main.StreamURI
		}
		// next distinct, lower-resolution URI becomes the sub stream
		for _, p := range res.Profiles[1:] {
			if p.StreamURI != "" && p.StreamURI != main.StreamURI {
				params.SubURL = &p.StreamURI
				break
			}
		}
	}
	updated, err := s.cams.Update(r.Context(), cam.ID, params)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.onvif_sync", Target: cam.ID, IP: clientIP(r),
	})
	s.syncCamera(r.Context(), cam.ID, false)
	JSON(w, http.StatusOK, map[string]any{"camera": updated, "probe": res})
}

// --- PTZ -------------------------------------------------------------------

// ptzTarget resolves camera + creds + profile for PTZ/imaging routes.
func (s *Server) ptzTarget(w http.ResponseWriter, r *http.Request) (
	cam *camera.Camera, endpoint, user, pass, profile string, ok bool,
) {
	cam = s.cameraFor(w, r)
	if cam == nil {
		return nil, "", "", "", "", false
	}
	endpoint, user, pass, profile, err := s.onvifCreds(r, cam)
	if errors.Is(err, onvif.ErrNoOnvif) {
		Error(w, http.StatusBadRequest, "no_onvif", err.Error())
		return nil, "", "", "", "", false
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return nil, "", "", "", "", false
	}
	profile, err = s.onvifProfileOrFirst(r, cam, endpoint, user, pass, profile)
	if err != nil {
		Error(w, http.StatusBadGateway, "onvif_unreachable", err.Error())
		return nil, "", "", "", "", false
	}
	return cam, endpoint, user, pass, profile, true
}

func (s *Server) ptzMove(w http.ResponseWriter, r *http.Request) {
	cam, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	if !cam.HasPTZ {
		Error(w, http.StatusBadRequest, "no_ptz", "camera does not advertise PTZ")
		return
	}
	var b struct {
		Pan       float64 `json:"pan"`
		Tilt      float64 `json:"tilt"`
		Zoom      float64 `json:"zoom"`
		TimeoutMs int     `json:"timeout_ms"`
	}
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if b.TimeoutMs <= 0 {
		b.TimeoutMs = 1000
	}
	if err := onvif.ContinuousMove(r.Context(), endpoint, user, pass, profile,
		b.Pan, b.Tilt, b.Zoom, b.TimeoutMs); err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ptzStop(w http.ResponseWriter, r *http.Request) {
	_, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	if err := onvif.Stop(r.Context(), endpoint, user, pass, profile); err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ptzPresets(w http.ResponseWriter, r *http.Request) {
	_, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	presets, err := onvif.GetPresets(r.Context(), endpoint, user, pass, profile)
	if err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"presets": presets})
}

func (s *Server) ptzSavePreset(w http.ResponseWriter, r *http.Request) {
	_, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if err := Decode(w, r, &b); err != nil || b.Name == "" {
		Error(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	token, err := onvif.SetPreset(r.Context(), endpoint, user, pass, profile, b.Name)
	if err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	JSON(w, http.StatusCreated, map[string]any{"token": token, "name": b.Name})
}

func (s *Server) ptzGotoPreset(w http.ResponseWriter, r *http.Request) {
	_, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	token := chi.URLParam(r, "token")
	if err := onvif.GotoPreset(r.Context(), endpoint, user, pass, profile, token); err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ptzDeletePreset(w http.ResponseWriter, r *http.Request) {
	_, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	token := chi.URLParam(r, "token")
	if err := onvif.RemovePreset(r.Context(), endpoint, user, pass, profile, token); err != nil {
		Error(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Imaging ---------------------------------------------------------------

// imagingSourceToken resolves (and caches on the camera row) the ONVIF video
// source token used by the imaging service.
func (s *Server) imagingSourceToken(r *http.Request, cam *camera.Camera,
	endpoint, user, pass, profile string,
) (string, error) {
	if tok, _ := cam.ImagingConfig["video_source_token"].(string); tok != "" {
		return tok, nil
	}
	tok, err := onvif.VideoSourceToken(r.Context(), endpoint, user, pass, profile)
	if err != nil {
		return "", err
	}
	cfg := map[string]any{}
	for k, v := range cam.ImagingConfig {
		cfg[k] = v
	}
	cfg["video_source_token"] = tok
	_, _ = s.cams.Update(r.Context(), cam.ID, camera.UpdateParams{ImagingConfig: cfg})
	return tok, nil
}

func (s *Server) imagingGet(w http.ResponseWriter, r *http.Request) {
	cam, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	tok, err := s.imagingSourceToken(r, cam, endpoint, user, pass, profile)
	if err != nil {
		Error(w, http.StatusBadGateway, "onvif_unreachable", err.Error())
		return
	}
	im, err := onvif.GetImaging(r.Context(), endpoint, user, pass, tok)
	if err != nil {
		Error(w, http.StatusBadGateway, "imaging_failed", err.Error())
		return
	}
	JSON(w, http.StatusOK, im)
}

func (s *Server) imagingPut(w http.ResponseWriter, r *http.Request) {
	cam, endpoint, user, pass, profile, ok := s.ptzTarget(w, r)
	if !ok {
		return
	}
	var im onvif.Imaging
	if err := Decode(w, r, &im); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	tok, err := s.imagingSourceToken(r, cam, endpoint, user, pass, profile)
	if err != nil {
		Error(w, http.StatusBadGateway, "onvif_unreachable", err.Error())
		return
	}
	if err := onvif.SetImaging(r.Context(), endpoint, user, pass, tok, &im); err != nil {
		Error(w, http.StatusBadGateway, "imaging_failed", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.imaging_set", Target: cam.ID, IP: clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
