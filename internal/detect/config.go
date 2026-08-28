// Package detect runs software motion detection on camera sub-streams:
// ffmpeg decodes to small luma frames, a rolling-background frame differ
// scores change against zones/sensitivity/schedules, and a state machine
// turns tick sequences into motion events (docs/07-detection-notifications.md).
package detect

import (
	"encoding/json"
	"fmt"
	"time"
)

// MotionConfig mirrors cameras.motion_config (docs/07).
type MotionConfig struct {
	Enabled     bool                   `json:"enabled"`
	Sensitivity float64                `json:"sensitivity"`  // 0..1, default 0.6
	MinAreaPct  float64                `json:"min_area_pct"` // % of effective area, default 0.5
	Zones       []Zone                 `json:"zones"`
	Schedule    map[string][][2]string `json:"schedule"` // {"mon": [["08:00","18:00"]]}
	PreRollS    int                    `json:"pre_roll_s"`
	PostRollS   int                    `json:"post_roll_s"`
	CooldownS   int                    `json:"cooldown_s"`
}

// Zone is a normalized-coordinate polygon; mode is "include" or "exclude".
type Zone struct {
	Name    string       `json:"name"`
	Polygon [][2]float64 `json:"polygon"`
	Mode    string       `json:"mode"`
}

// ParseConfig decodes the jsonb motion_config with defaults applied.
func ParseConfig(raw json.RawMessage) MotionConfig {
	cfg := MotionConfig{
		Sensitivity: 0.6,
		MinAreaPct:  0.5,
		PostRollS:   10,
		CooldownS:   30,
		PreRollS:    5,
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg) // unknown/missing fields keep defaults
	}
	if cfg.Sensitivity <= 0 || cfg.Sensitivity > 1 {
		cfg.Sensitivity = 0.6
	}
	return cfg
}

var dayKeys = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Active reports whether detection should run at time t per the schedule.
// An empty schedule means always active. Windows may wrap midnight
// ("21:00"–"07:00"). The special "everyday" key applies to all days.
func (c MotionConfig) Active(t time.Time) bool {
	if len(c.Schedule) == 0 {
		return true
	}
	windows, ok := c.Schedule["everyday"]
	if !ok {
		windows, ok = c.Schedule[dayKeys[int(t.Weekday())]]
	}
	if !ok || len(windows) == 0 {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	for _, w := range windows {
		start, err1 := parseHM(w[0])
		end, err2 := parseHM(w[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if start <= end {
			if now >= start && now < end {
				return true
			}
		} else if now >= start || now < end { // wraps midnight
			return true
		}
	}
	return false
}

func parseHM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	return h*60 + m, nil
}

// pixelThreshold maps sensitivity (0..1) to a per-pixel luma difference
// threshold: higher sensitivity → lower threshold.
func (c MotionConfig) pixelThreshold() float64 {
	return 5 + (1-c.Sensitivity)*45
}
