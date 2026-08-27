package ingest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

// ProbeStream describes one tested stream.
type ProbeStream struct {
	Type     string `json:"type"`  // "video" | "audio"
	Codec    string `json:"codec"` // "h264", "h265", "aac", …
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Rate     int    `json:"rate,omitempty"`
}

// ProbeResult is the response of POST /cameras/probe.
type ProbeResult struct {
	OK      bool          `json:"ok"`
	Streams []ProbeStream `json:"streams"`
	Error   string        `json:"error,omitempty"`
}

// Probe connects to an RTSP URL, DESCRIBEs it, and reports the streams.
// Used by the camera add/edit UI before saving.
func Probe(ctx context.Context, rawURL, username, password, transport string) ProbeResult {
	if !strings.Contains(rawURL, "://") {
		rawURL = "rtsp://" + rawURL
	}
	pu, err := url.Parse(rawURL)
	if err != nil {
		return ProbeResult{Error: "invalid URL: " + err.Error()}
	}
	if pu.User == nil && username != "" {
		pu.User = url.UserPassword(username, password)
	}
	u, err := base.ParseURL(pu.String())
	if err != nil {
		return ProbeResult{Error: "invalid RTSP URL: " + err.Error()}
	}

	c := &gortsplib.Client{
		Scheme:      u.Scheme,
		Host:        u.Host,
		ReadTimeout: 8 * time.Second,
		Protocol:    protocolOf(transport),
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
	}
	if err := c.Start(); err != nil {
		return ProbeResult{Error: fmt.Sprintf("connect: %v", err)}
	}
	defer c.Close()

	type result struct {
		desc *description.Session
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		desc, _, err := c.Describe(u)
		ch <- result{desc, err}
	}()
	select {
	case <-ctx.Done():
		return ProbeResult{Error: "probe cancelled"}
	case <-time.After(10 * time.Second):
		return ProbeResult{Error: "probe timed out"}
	case r := <-ch:
		if r.err != nil {
			return ProbeResult{Error: fmt.Sprintf("describe: %v", r.err)}
		}
		return ProbeResult{OK: true, Streams: describeStreams(r.desc)}
	}
}

func describeStreams(desc *description.Session) []ProbeStream {
	out := []ProbeStream{}
	var h264f *format.H264
	if desc.FindFormat(&h264f) != nil {
		s := ProbeStream{Type: "video", Codec: "h264"}
		if len(h264f.SPS) > 0 {
			if w, h, err := h264Dimensions(h264f.SPS); err == nil {
				s.Width, s.Height = w, h
			}
		}
		out = append(out, s)
	}
	var h265f *format.H265
	if desc.FindFormat(&h265f) != nil {
		s := ProbeStream{Type: "video", Codec: "h265"}
		if len(h265f.SPS) > 0 {
			if w, h, err := h265Dimensions(h265f.SPS); err == nil {
				s.Width, s.Height = w, h
			}
		}
		out = append(out, s)
	}
	var aac *format.MPEG4Audio
	if desc.FindFormat(&aac) != nil {
		s := ProbeStream{Type: "audio", Codec: "aac"}
		if aac.Config != nil {
			s.Rate = aac.Config.SampleRate
			s.Channels = aac.Config.ChannelCount
		}
		out = append(out, s)
	}
	for _, m := range desc.Medias {
		for _, f := range m.Formats {
			switch f.(type) {
			case *format.H264, *format.H265, *format.MPEG4Audio:
			default:
				out = append(out, ProbeStream{
					Type:  string(m.Type),
					Codec: f.Codec(),
				})
			}
		}
	}
	return out
}

// h264Dimensions parses width/height from an SPS NAL.
func h264Dimensions(sps []byte) (int, int, error) {
	var s h264.SPS
	if err := s.Unmarshal(sps); err != nil {
		return 0, 0, err
	}
	return s.Width(), s.Height(), nil
}

func h265Dimensions(sps []byte) (int, int, error) {
	var s h265.SPS
	if err := s.Unmarshal(sps); err != nil {
		return 0, 0, err
	}
	return s.Width(), s.Height(), nil
}
