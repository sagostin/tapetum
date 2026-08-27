// Package onvif wraps onvif-go for discovery, probing/profile sync, PTZ and
// imaging control. See docs/03-api.md (Cameras, PTZ & Imaging).
package onvif

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	onvifgo "github.com/0x524a/onvif-go"
	"github.com/0x524a/onvif-go/discovery"
)

// DiscoveredDevice is one WS-Discovery probe result.
type DiscoveredDevice struct {
	Endpoint string   `json:"endpoint"` // device service URL
	Name     string   `json:"name"`
	Hardware string   `json:"hardware"`
	Location string   `json:"location"`
	XAddrs   []string `json:"xaddrs"`
}

// Discover scans the LAN for ONVIF devices via WS-Discovery multicast.
func Discover(ctx context.Context, timeout time.Duration) ([]*DiscoveredDevice, error) {
	devs, err := discovery.Discover(ctx, timeout)
	if err != nil {
		return nil, err
	}
	out := []*DiscoveredDevice{}
	for _, d := range devs {
		dd := &DiscoveredDevice{
			Endpoint: d.GetDeviceEndpoint(),
			Name:     d.GetName(),
			Location: d.GetLocation(),
			XAddrs:   d.XAddrs,
		}
		for _, s := range d.Scopes {
			if v, ok := strings.CutPrefix(s, "onvif://www.onvif.org/hardware/"); ok {
				dd.Hardware = v
			}
		}
		out = append(out, dd)
	}
	return out, nil
}

// NewClient builds an authenticated ONVIF client for an endpoint.
func NewClient(endpoint, username, password string) (*onvifgo.Client, error) {
	opts := []onvifgo.ClientOption{onvifgo.WithTimeout(10 * time.Second)}
	if username != "" {
		opts = append(opts, onvifgo.WithCredentials(username, password))
	}
	return onvifgo.NewClient(endpoint, opts...)
}

// Profile is one media profile with its resolved stream URI.
type Profile struct {
	Token     string `json:"token"`
	Name      string `json:"name"`
	Codec     string `json:"codec"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	StreamURI string `json:"stream_uri"`
	HasPTZ    bool   `json:"has_ptz"`
}

// ProbeResult is the response of an ONVIF probe / connection test.
type ProbeResult struct {
	Manufacturer    string     `json:"manufacturer"`
	Model           string     `json:"model"`
	FirmwareVersion string     `json:"firmware_version"`
	HasPTZ          bool       `json:"has_ptz"`
	HasImaging      bool       `json:"has_imaging"`
	Profiles        []*Profile `json:"profiles"`
}

// Probe pulls device info, capabilities and profiles (with stream URIs) from
// an ONVIF endpoint.
func Probe(ctx context.Context, endpoint, username, password string) (*ProbeResult, error) {
	c, err := NewClient(endpoint, username, password)
	if err != nil {
		return nil, err
	}
	res := &ProbeResult{Profiles: []*Profile{}}

	if info, err := c.GetDeviceInformation(ctx); err == nil {
		res.Manufacturer = info.Manufacturer
		res.Model = info.Model
		res.FirmwareVersion = info.FirmwareVersion
	}
	if caps, err := c.GetCapabilities(ctx); err == nil {
		res.HasPTZ = caps.PTZ != nil
		res.HasImaging = caps.Imaging != nil
	}

	profiles, err := c.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("onvif: get profiles: %w", err)
	}
	for _, p := range profiles {
		pr := &Profile{Token: p.Token, Name: p.Name, HasPTZ: p.PTZConfiguration != nil}
		if vec := p.VideoEncoderConfiguration; vec != nil {
			pr.Codec = strings.ToLower(vec.Encoding)
			if vec.Resolution != nil {
				pr.Width = vec.Resolution.Width
				pr.Height = vec.Resolution.Height
			}
		}
		if uri, err := c.GetStreamURI(ctx, p.Token); err == nil {
			pr.StreamURI = uri.URI
		}
		res.Profiles = append(res.Profiles, pr)
	}
	// highest resolution first — profile[0] is the natural main stream
	sort.Slice(res.Profiles, func(i, j int) bool {
		return res.Profiles[i].Width*res.Profiles[i].Height >
			res.Profiles[j].Width*res.Profiles[j].Height
	})
	if res.HasPTZ {
		for _, p := range res.Profiles {
			if p.HasPTZ {
				goto done
			}
		}
		// device advertises PTZ but no profile has a PTZ configuration
		res.HasPTZ = false
	}
done:
	return res, nil
}

// --- PTZ -------------------------------------------------------------------

var (
	ErrNoOnvif = errors.New("camera has no ONVIF endpoint configured")
	ErrNoPTZ   = errors.New("camera does not advertise PTZ")
)

// ContinuousMove starts a continuous move; speeds are -1..1 per axis.
func ContinuousMove(ctx context.Context, endpoint, user, pass, profileToken string,
	pan, tilt, zoom float64, timeoutMs int,
) error {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return err
	}
	clamp := func(f float64) float64 {
		return min(1, max(-1, f))
	}
	speed := &onvifgo.PTZSpeed{
		PanTilt: &onvifgo.Vector2D{X: clamp(pan), Y: clamp(tilt)},
		Zoom:    &onvifgo.Vector1D{X: clamp(zoom)},
	}
	var timeout *string
	if timeoutMs > 0 {
		t := fmt.Sprintf("PT%dS", max(1, timeoutMs/1000))
		timeout = &t
	}
	return c.ContinuousMove(ctx, profileToken, speed, timeout)
}

// Stop halts all PTZ axes.
func Stop(ctx context.Context, endpoint, user, pass, profileToken string) error {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return err
	}
	return c.Stop(ctx, profileToken, true, true)
}

// Preset is a saved PTZ position.
type Preset struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// GetPresets lists saved presets.
func GetPresets(ctx context.Context, endpoint, user, pass, profileToken string) ([]*Preset, error) {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return nil, err
	}
	ps, err := c.GetPresets(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	out := []*Preset{}
	for _, p := range ps {
		out = append(out, &Preset{Token: p.Token, Name: p.Name})
	}
	return out, nil
}

// SetPreset saves the current position under name; returns the preset token.
func SetPreset(ctx context.Context, endpoint, user, pass, profileToken, name string) (string, error) {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return "", err
	}
	return c.SetPreset(ctx, profileToken, name, "")
}

// GotoPreset moves to a preset.
func GotoPreset(ctx context.Context, endpoint, user, pass, profileToken, presetToken string) error {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return err
	}
	return c.GotoPreset(ctx, profileToken, presetToken, nil)
}

// RemovePreset deletes a preset.
func RemovePreset(ctx context.Context, endpoint, user, pass, profileToken, presetToken string) error {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return err
	}
	return c.RemovePreset(ctx, profileToken, presetToken)
}

// --- Imaging ---------------------------------------------------------------

// Imaging is the flat set of imaging settings exposed over the API.
type Imaging struct {
	Brightness      *float64 `json:"brightness,omitempty"`
	Contrast        *float64 `json:"contrast,omitempty"`
	ColorSaturation *float64 `json:"color_saturation,omitempty"`
	Sharpness       *float64 `json:"sharpness,omitempty"`
	IrCutFilter     *string  `json:"ir_cut_filter,omitempty"`
	WDREnabled      *bool    `json:"wdr_enabled,omitempty"`
	WDRLevel        *float64 `json:"wdr_level,omitempty"`
}

// GetImaging reads imaging settings for a video source token.
func GetImaging(ctx context.Context, endpoint, user, pass, videoSourceToken string) (*Imaging, error) {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return nil, err
	}
	is, err := c.GetImagingSettings(ctx, videoSourceToken)
	if err != nil {
		return nil, err
	}
	out := &Imaging{
		Brightness:      is.Brightness,
		Contrast:        is.Contrast,
		ColorSaturation: is.ColorSaturation,
		Sharpness:       is.Sharpness,
		IrCutFilter:     is.IrCutFilter,
	}
	if is.WideDynamicRange != nil {
		on := strings.EqualFold(is.WideDynamicRange.Mode, "ON")
		out.WDREnabled = &on
		lvl := is.WideDynamicRange.Level
		out.WDRLevel = &lvl
	}
	return out, nil
}

// SetImaging writes the non-nil fields of im.
func SetImaging(ctx context.Context, endpoint, user, pass, videoSourceToken string, im *Imaging) error {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return err
	}
	// read-modify-write: cameras reject documents missing required elements
	cur, err := c.GetImagingSettings(ctx, videoSourceToken)
	if err != nil {
		return err
	}
	if im.Brightness != nil {
		cur.Brightness = im.Brightness
	}
	if im.Contrast != nil {
		cur.Contrast = im.Contrast
	}
	if im.ColorSaturation != nil {
		cur.ColorSaturation = im.ColorSaturation
	}
	if im.Sharpness != nil {
		cur.Sharpness = im.Sharpness
	}
	if im.IrCutFilter != nil {
		cur.IrCutFilter = im.IrCutFilter
	}
	if im.WDREnabled != nil {
		mode := "OFF"
		if *im.WDREnabled {
			mode = "ON"
		}
		lvl := float64(50)
		if im.WDRLevel != nil {
			lvl = *im.WDRLevel
		} else if cur.WideDynamicRange != nil {
			lvl = cur.WideDynamicRange.Level
		}
		cur.WideDynamicRange = &onvifgo.WideDynamicRange{Mode: mode, Level: lvl}
	}
	return c.SetImagingSettings(ctx, videoSourceToken, cur, true)
}

// VideoSourceToken resolves the video source token for a profile (needed by
// the imaging service).
func VideoSourceToken(ctx context.Context, endpoint, user, pass, profileToken string) (string, error) {
	c, err := NewClient(endpoint, user, pass)
	if err != nil {
		return "", err
	}
	profiles, err := c.GetProfiles(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range profiles {
		if p.Token == profileToken && p.VideoSourceConfiguration != nil {
			return p.VideoSourceConfiguration.SourceToken, nil
		}
	}
	if len(profiles) > 0 && profiles[0].VideoSourceConfiguration != nil {
		return profiles[0].VideoSourceConfiguration.SourceToken, nil
	}
	return "", errors.New("onvif: no video source found")
}
