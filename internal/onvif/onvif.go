// Package onvif wraps onvif-go for discovery, probing/profile sync, PTZ and
// imaging control. See docs/03-api.md (Cameras, PTZ & Imaging).
package onvif

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
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

// Default ONVIF ports and paths to probe when a device is reachable on a
// known host (or a network range) but did not respond to WS-Discovery
// multicast. Manufacturers vary: Hikvision commonly uses 80, Dahua 80, Reolink
// 80/8000, some legacy devices 8080/8899.
var (
	defaultONVIFPorts   = []int{80, 8000, 8080, 8899}
	defaultONVIFPaths   = []string{"/onvif/device_service"}
	maxScanHostsPerCIDR = 1024 // refuse /8 scans; keep scan time bounded
)

// CIDRHosts expands an IPv4 CIDR (e.g. "192.168.1.0/24") into the list of host
// IPs in the range, skipping the network and broadcast addresses. Returns an
// error for IPv6 or malformed input. Larger ranges than maxScanHostsPerCIDR are
// rejected so the scan cannot block for arbitrarily long.
func CIDRHosts(cidr string) ([]string, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("onvif: invalid CIDR %q: %w", cidr, err)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("onvif: only IPv4 CIDRs are supported")
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("onvif: only IPv4 CIDRs are supported")
	}
	if 32-ones < 1 {
		return []string{ip.String()}, nil // /32 is a single host
	}
	// walk every address in the CIDR; we'll skip network and broadcast below.
	cur := ip.Mask(ipNet.Mask)
	end := make(net.IP, len(cur))
	copy(end, cur)
	// set host bits to all 1s
	for i := range end {
		end[i] |= ^ipNet.Mask[i]
	}
	hosts := []string{}
	for {
		hosts = append(hosts, cur.String())
		if cur.Equal(end) {
			break
		}
		incIP(cur)
	}
	if 32-ones >= 2 {
		// strip network and broadcast for prefixes wider than /31
		hosts = hosts[1 : len(hosts)-1]
	}
	if len(hosts) > maxScanHostsPerCIDR {
		return nil, fmt.Errorf("onvif: CIDR too large (%d hosts); narrow to <= %d hosts",
			len(hosts), maxScanHostsPerCIDR)
	}
	return hosts, nil
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

// ResolveHost expands a single hostname or IP literal into one or more IP
// literals to probe. Hostnames are resolved via DNS so the scan hits every
// address a multi-homed host exposes.
func ResolveHost(host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, fmt.Errorf("onvif: only IPv4 hosts are supported")
		}
		return []string{ip.String()}, nil
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("onvif: resolve %q: %w", host, err)
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || ip.To4() == nil {
			continue
		}
		out = append(out, ip.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("onvif: no IPv4 addresses for %q", host)
	}
	return out, nil
}

// ScanOptions tunes a directed-scan discovery.
type ScanOptions struct {
	// PerProbeTimeout caps the time spent on a single (host, port, path) probe.
	// Defaults to 2s if zero.
	PerProbeTimeout time.Duration
	// Parallelism is the max in-flight probes. Defaults to 32 if zero.
	Parallelism int
	// Ports overrides the TCP port set (default: common ONVIF ports).
	Ports []int
	// Paths overrides the URL path set (default: /onvif/device_service).
	Paths []string
}

// ScanHosts probes the given IP list against common ONVIF device-service
// ports/paths and returns the discovered devices. Each (host, port, path)
// triple is tried in parallel; the first successful response on a host marks
// that host as a device and we stop probing additional ports/paths for it.
//
// Credentials, when non-empty, are sent with every probe — use this when the
// fleet shares a username/password (most common case for ONVIF). Empty
// credentials attempt unauthenticated access (which some devices allow).
func ScanHosts(ctx context.Context, hosts []string, username, password string, opts ScanOptions) ([]*DiscoveredDevice, error) {
	if opts.PerProbeTimeout <= 0 {
		opts.PerProbeTimeout = 2 * time.Second
	}
	if opts.Parallelism <= 0 {
		opts.Parallelism = 32
	}
	if len(opts.Ports) == 0 {
		opts.Ports = defaultONVIFPorts
	}
	if len(opts.Paths) == 0 {
		opts.Paths = defaultONVIFPaths
	}

	// Build every (host, port, path) candidate. The probe fan-out is bounded
	// by opts.Parallelism; once a host yields a device we still drain the
	// candidates that mention the same host so we don't leak goroutines.
	type candidate struct {
		host, path string
		port       int
	}
	cands := make([]candidate, 0, len(hosts)*len(opts.Ports)*len(opts.Paths))
	for _, h := range hosts {
		for _, p := range opts.Ports {
			for _, pa := range opts.Paths {
				cands = append(cands, candidate{host: h, port: p, path: pa})
			}
		}
	}

	// Per-host "found" channel so once one candidate succeeds for a host, the
	// remaining candidates for that host stop being processed (they'd just be
	// duplicates anyway).
	knownHosts := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		knownHosts[h] = false
	}

	var (
		mu      sync.Mutex
		devices []*DiscoveredDevice
		wg      sync.WaitGroup
		sem     = make(chan struct{}, opts.Parallelism)
	)
	for _, c := range cands {
		select {
		case <-ctx.Done():
			wg.Wait()
			if devices == nil {
				devices = []*DiscoveredDevice{}
			}
			return devices, ctx.Err()
		default:
		}
		mu.Lock()
		if knownHosts[c.host] {
			mu.Unlock()
			continue
		}
		mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func(c candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, opts.PerProbeTimeout)
			defer cancel()
			dev, err := ProbeAt(probeCtx, c.host, c.port, c.path, username, password)
			if err != nil || dev == nil {
				return
			}
			mu.Lock()
			if !knownHosts[c.host] {
				knownHosts[c.host] = true
				devices = append(devices, dev)
			}
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if devices == nil {
		devices = []*DiscoveredDevice{}
	}
	return devices, nil
}

// ProbeAt attempts to reach an ONVIF device service at the given
// (host, port, path) candidate with the supplied credentials. Returns a
// DiscoveredDevice when GetDeviceInformation succeeds, or an error otherwise.
// The endpoint URL is normalised to http://host:port/path and exposed in
// DiscoveredDevice.Endpoint for adoption.
func ProbeAt(ctx context.Context, host string, port int, path, username, password string) (*DiscoveredDevice, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := fmt.Sprintf("http://%s:%d%s", host, port, path)
	c, err := NewClient(endpoint, username, password)
	if err != nil {
		return nil, err
	}
	info, err := c.GetDeviceInformation(ctx)
	if err != nil {
		return nil, err
	}
	dev := &DiscoveredDevice{
		Endpoint: endpoint,
		Name:     info.Manufacturer + " " + info.Model,
		Hardware: info.Model,
		Location: info.SerialNumber,
	}
	if info.FirmwareVersion != "" {
		dev.Location = strings.TrimSpace(dev.Location + " fw=" + info.FirmwareVersion)
	}
	// Prefer the device service XAddr advertised by GetCapabilities if present.
	if caps, err := c.GetCapabilities(ctx); err == nil {
		if caps.Device != nil && caps.Device.XAddr != "" {
			dev.Endpoint = caps.Device.XAddr
			dev.XAddrs = append(dev.XAddrs, caps.Device.XAddr)
		}
		if caps.PTZ != nil && caps.PTZ.XAddr != "" {
			dev.XAddrs = append(dev.XAddrs, caps.PTZ.XAddr)
		}
		if caps.Media != nil && caps.Media.XAddr != "" {
			dev.XAddrs = append(dev.XAddrs, caps.Media.XAddr)
		}
		if caps.Imaging != nil && caps.Imaging.XAddr != "" {
			dev.XAddrs = append(dev.XAddrs, caps.Imaging.XAddr)
		}
	}
	return dev, nil
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
