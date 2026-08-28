package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/notify"
)

// --- notification channels --------------------------------------------------

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.notifyStore.ListChannels(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"channels": chs})
}

type channelBody struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Config  map[string]any `json:"config"`
	Enabled *bool          `json:"enabled"`
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var b channelBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if b.Name == "" || !notify.ValidChannelType(b.Type) {
		Error(w, http.StatusBadRequest, "bad_request", "name and a valid type are required")
		return
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	ch := &notify.Channel{Name: b.Name, Type: b.Type, Config: b.Config, Enabled: enabled}
	if ch.Config == nil {
		ch.Config = map[string]any{}
	}
	if err := s.notifyStore.CreateChannel(r.Context(), ch); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.channel_create", Target: ch.ID, IP: clientIP(r),
		Detail: map[string]any{"name": ch.Name, "type": ch.Type},
	})
	JSON(w, http.StatusCreated, ch)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.notifyStore.GetChannel(r.Context(), id)
	if err != nil || existing == nil {
		Error(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	var b channelBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if b.Name != "" {
		existing.Name = b.Name
	}
	if b.Type != "" {
		if !notify.ValidChannelType(b.Type) {
			Error(w, http.StatusBadRequest, "bad_request", "invalid type")
			return
		}
		existing.Type = b.Type
	}
	if b.Config != nil {
		if existing.Config == nil {
			existing.Config = map[string]any{}
		}
		for k, v := range b.Config {
			existing.Config[k] = v
		}
	}
	if b.Enabled != nil {
		existing.Enabled = *b.Enabled
	}
	if err := s.notifyStore.UpdateChannel(r.Context(), existing); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.channel_update", Target: id, IP: clientIP(r),
	})
	JSON(w, http.StatusOK, existing)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.notifyStore.DeleteChannel(r.Context(), id); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.channel_delete", Target: id, IP: clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.notifyWorker.TestChannel(r.Context(), id); err != nil {
		Error(w, http.StatusBadGateway, "send_failed", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"status": "sent"})
}

// --- notification rules -----------------------------------------------------

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.notifyStore.ListRules(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"rules": rules})
}

type ruleBody struct {
	Name       string         `json:"name"`
	Enabled    *bool          `json:"enabled"`
	CameraIDs  []string       `json:"camera_ids"`
	EventTypes []string       `json:"event_types"`
	Labels     []string       `json:"labels"`
	Schedule   map[string]any `json:"schedule"`
	CooldownS  *int           `json:"cooldown_s"`
	ChannelIDs []string       `json:"channel_ids"`
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var b ruleBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if b.Name == "" || len(b.ChannelIDs) == 0 {
		Error(w, http.StatusBadRequest, "bad_request", "name and channel_ids are required")
		return
	}
	rule := &notify.Rule{
		Name:       b.Name,
		Enabled:    true,
		CameraIDs:  orEmpty(b.CameraIDs),
		EventTypes: orEmpty(b.EventTypes),
		Labels:     orEmpty(b.Labels),
		Schedule:   b.Schedule,
		CooldownS:  300,
		ChannelIDs: b.ChannelIDs,
	}
	if rule.EventTypes == nil {
		rule.EventTypes = []string{"motion"}
	}
	if b.Enabled != nil {
		rule.Enabled = *b.Enabled
	}
	if b.CooldownS != nil {
		rule.CooldownS = *b.CooldownS
	}
	if rule.Schedule == nil {
		rule.Schedule = map[string]any{}
	}
	if err := s.notifyStore.CreateRule(r.Context(), rule); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.rule_create", Target: rule.ID, IP: clientIP(r),
		Detail: map[string]any{"name": rule.Name},
	})
	JSON(w, http.StatusCreated, rule)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rules, err := s.notifyStore.ListRules(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var rule *notify.Rule
	for _, rl := range rules {
		if rl.ID == id {
			rule = rl
			break
		}
	}
	if rule == nil {
		Error(w, http.StatusNotFound, "not_found", "rule not found")
		return
	}
	var b ruleBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if b.Name != "" {
		rule.Name = b.Name
	}
	if b.Enabled != nil {
		rule.Enabled = *b.Enabled
	}
	if b.CameraIDs != nil {
		rule.CameraIDs = b.CameraIDs
	}
	if b.EventTypes != nil {
		rule.EventTypes = b.EventTypes
	}
	if b.Labels != nil {
		rule.Labels = b.Labels
	}
	if b.Schedule != nil {
		rule.Schedule = b.Schedule
	}
	if b.CooldownS != nil {
		rule.CooldownS = *b.CooldownS
	}
	if b.ChannelIDs != nil {
		rule.ChannelIDs = b.ChannelIDs
	}
	if err := s.notifyStore.UpdateRule(r.Context(), rule); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.rule_update", Target: id, IP: clientIP(r),
	})
	JSON(w, http.StatusOK, rule)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.notifyStore.DeleteRule(r.Context(), id); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "notify.rule_delete", Target: id, IP: clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notifyLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	entries, err := s.notifyStore.ListLog(r.Context(), q.Get("rule"), q.Get("status"), limit)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"log": entries})
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
