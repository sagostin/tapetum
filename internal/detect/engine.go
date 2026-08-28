package detect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"

	"github.com/sagostin/tapetum/internal/live"
)

// detWidth is the luma frame width the engine analyzes.
const detWidth = 320

// Callbacks receive engine transitions. They run on the engine goroutine
// and must be cheap; heavy work (DB, snapshot) belongs in a goroutine.
type Callbacks struct {
	// OnStart fires when the state machine opens a motion event.
	OnStart func(ts time.Time)
	// OnEnd fires when an event closes (post-roll elapsed without motion).
	OnEnd func(start, end time.Time, peakScore float64)
}

// Engine runs motion detection for one camera against its sub-stream.
type Engine struct {
	camID string
	cfg   MotionConfig
	live  *live.Hub
	cb    Callbacks
	log   *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	running bool
}

// NewEngine creates (but does not start) a detector for a camera.
func NewEngine(camID string, cfg MotionConfig, hub *live.Hub, cb Callbacks) *Engine {
	return &Engine{
		camID: camID, cfg: cfg, live: hub, cb: cb,
		log: slog.With("component", "detect", "camera", camID),
	}
}

// Start launches the detection loop; it resubscribes across ingest
// reconnects until Stop.
func (e *Engine) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return
	}
	e.running = true
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		e.loop(ctx)
	}()
}

// Stop terminates the engine and waits for the subprocess to exit.
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	cancel, done := e.cancel, e.done
	e.mu.Unlock()
	cancel()
	<-done
}

// loop resubscribes to the sub-stream with backoff until stopped.
func (e *Engine) loop(ctx context.Context) {
	for {
		err := e.runSession(ctx)
		if ctx.Err() != nil {
			return
		}
		e.log.Debug("detect session ended", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// runSession decodes + analyzes until the subscription or ffmpeg fails.
func (e *Engine) runSession(ctx context.Context) error {
	codec, sps, pps, vps, frames, cancel, ok := e.live.Subscribe(e.camID, true)
	if !ok {
		return errors.New("sub-stream not live")
	}
	defer cancel()

	demux := "h264"
	if codec == "h265" {
		demux = "hevc"
	}
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "error",
		"-f", demux, "-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:-2,fps=5", detWidth),
		"-pix_fmt", "gray", "-vcodec", "pgm", "-f", "image2pipe", "pipe:1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	// feed AUs (Annex-B, param sets prepended at keyframes) to ffmpeg
	feedDone := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			_ = stdin.Close() // EOF lets ffmpeg finish → process() returns
			feedDone <- err
		}()
		params := [][]byte{}
		if len(vps) > 0 {
			params = append(params, vps)
		}
		params = append(params, sps, pps)
		for {
			select {
			case <-ctx.Done():
				return
			case f, open := <-frames:
				if !open {
					err = errors.New("subscription closed")
					return
				}
				au := f.AU
				if f.Keyframe {
					full := make([][]byte, 0, len(au)+len(params))
					full = append(full, params...)
					full = append(full, au...)
					au = full
				}
				annexb, merr := h264.AnnexB(au).Marshal()
				if merr != nil {
					continue
				}
				if _, werr := stdin.Write(annexb); werr != nil {
					err = werr
					return
				}
			}
		}
	}()

	procErr := e.process(ctx, bufio.NewReaderSize(stdout, 1<<20))
	select {
	case ferr := <-feedDone:
		if procErr == nil {
			procErr = ferr
		}
	default:
	}
	return procErr
}

// process reads PGM frames and runs the diff + state machine.
func (e *Engine) process(ctx context.Context, r *bufio.Reader) error {
	var bg []float32 // rolling background (float luma)
	var include, exclude []bool
	maskW, maskH := 0, 0

	// state machine
	armed := true
	cooldownUntil := time.Time{}
	consec := 0
	active := false
	eventStart := time.Time{}
	peak := 0.0
	lastAbove := time.Time{}
	tick := 0

	// A session that dies mid-event (ingest restart, stream loss) must not
	// leak an open event — close it at the last observed motion.
	defer func() {
		if active {
			e.notifyEnd(eventStart, lastAbove, peak)
		}
	}()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		img, w, h, err := readPGM(r)
		if err != nil {
			return err
		}
		now := time.Now()

		if w != maskW || h != maskH {
			maskW, maskH = w, h
			bg = nil
			include, exclude = buildMasks(e.cfg.Zones, w, h)
		}

		blur := boxBlur3(img, w, h)

		if bg == nil {
			bg = make([]float32, len(blur))
			for i, v := range blur {
				bg[i] = float32(v)
			}
			continue
		}

		// schedule gate: close any open event when detection goes off-hours
		if !e.cfg.Active(now) {
			if active {
				active = false
				e.notifyEnd(eventStart, lastAbove, peak)
			}
			consec = 0
			e.accumulate(bg, blur)
			continue
		}

		if now.Before(cooldownUntil) {
			e.accumulate(bg, blur)
			continue
		}

		thresh := e.cfg.pixelThreshold()
		changed := diffMask(blur, bg, w, h, float32(thresh))
		changed = morphology(changed, w, h)

		var hit, area int
		for i := range changed {
			if exclude != nil && exclude[i] {
				continue
			}
			if include != nil && !include[i] {
				continue
			}
			area++
			if changed[i] {
				hit++
			}
		}
		score := 0.0
		if area > 0 {
			score = float64(hit) / float64(area) * 100
		}
		above := score >= e.cfg.MinAreaPct

		tick++
		if tick%25 == 0 {
			e.log.Debug("tick", "score", score, "hit", hit, "area", area, "active", active)
		}

		e.accumulate(bg, blur)

		switch {
		case !active:
			if !armed {
				if now.After(cooldownUntil) {
					armed = true
				}
				break
			}
			if above {
				consec++
				if consec >= 3 {
					active = true
					armed = false
					eventStart = now.Add(-2 * 200 * time.Millisecond) // tick span
					peak = score
					lastAbove = now
					if e.cb.OnStart != nil {
						e.cb.OnStart(eventStart)
					}
				}
			} else {
				consec = 0
			}
		default: // active
			if above {
				lastAbove = now
				if score > peak {
					peak = score
				}
			} else if now.Sub(lastAbove) >= time.Duration(e.cfg.PostRollS)*time.Second {
				active = false
				cooldownUntil = now.Add(time.Duration(e.cfg.CooldownS) * time.Second)
				e.notifyEnd(eventStart, lastAbove, peak)
				consec = 0
			}
		}
	}
}

func (e *Engine) notifyEnd(start, end time.Time, peak float64) {
	if e.cb.OnEnd != nil {
		e.cb.OnEnd(start, end, peak)
	}
}

// accumulate folds the current frame into the background model.
func (e *Engine) accumulate(bg []float32, frame []byte) {
	const alpha = 0.05
	for i, v := range frame {
		bg[i] += alpha * (float32(v) - bg[i])
	}
}

// diffMask returns a binary mask of pixels differing from the background.
func diffMask(frame []byte, bg []float32, w, h int, thresh float32) []bool {
	out := make([]bool, len(frame))
	for i, v := range frame {
		d := float32(v) - bg[i]
		if d < 0 {
			d = -d
		}
		out[i] = d > thresh
	}
	return out
}

// morphology applies a 3x3 open then close to suppress speckle noise.
func morphology(mask []bool, w, h int) []bool {
	erode := func(in []bool) []bool {
		out := make([]bool, len(in))
		for y := 1; y < h-1; y++ {
			for x := 1; x < w-1; x++ {
				all := true
				for dy := -1; dy <= 1 && all; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if !in[(y+dy)*w+x+dx] {
							all = false
							break
						}
					}
				}
				out[y*w+x] = all
			}
		}
		return out
	}
	dilate := func(in []bool) []bool {
		out := make([]bool, len(in))
		for y := 1; y < h-1; y++ {
			for x := 1; x < w-1; x++ {
				any_ := false
				for dy := -1; dy <= 1 && !any_; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if in[(y+dy)*w+x+dx] {
							any_ = true
							break
						}
					}
				}
				out[y*w+x] = any_
			}
		}
		return out
	}
	return dilate(erode(dilate(erode(mask))))
}

// boxBlur3 is a separable 3x3 box blur over the luma plane.
func boxBlur3(img []byte, w, h int) []byte {
	tmp := make([]uint16, len(img))
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			sum := uint16(img[row+x])
			if x > 0 {
				sum += uint16(img[row+x-1])
			}
			if x < w-1 {
				sum += uint16(img[row+x+1])
			}
			tmp[row+x] = sum / 3
		}
	}
	out := make([]byte, len(img))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := uint16(tmp[y*w+x])
			if y > 0 {
				sum += tmp[(y-1)*w+x]
			}
			if y < h-1 {
				sum += tmp[(y+1)*w+x]
			}
			out[y*w+x] = byte(sum / 3)
		}
	}
	return out
}

// buildMasks rasterizes include/exclude zone polygons at the detection
// resolution. Returns nil masks when no zones of that class exist.
func buildMasks(zones []Zone, w, h int) (include, exclude []bool) {
	for _, z := range zones {
		if len(z.Polygon) < 3 {
			continue
		}
		mask := make([]bool, w*h)
		rasterize(mask, z.Polygon, w, h)
		if z.Mode == "exclude" {
			if exclude == nil {
				exclude = make([]bool, w*h)
			}
			or(exclude, mask)
		} else {
			if include == nil {
				include = make([]bool, w*h)
			}
			or(include, mask)
		}
	}
	return include, exclude
}

func or(dst, src []bool) {
	for i := range dst {
		dst[i] = dst[i] || src[i]
	}
}

// rasterize fills a polygon (normalized coords) into mask via scanline
// even-odd rule.
func rasterize(mask []bool, poly [][2]float64, w, h int) {
	for y := 0; y < h; y++ {
		ny := (float64(y) + 0.5) / float64(h)
		var xs []float64
		for i := range poly {
			a := poly[i]
			b := poly[(i+1)%len(poly)]
			if (a[1] <= ny && b[1] > ny) || (b[1] <= ny && a[1] > ny) {
				t := (ny - a[1]) / (b[1] - a[1])
				xs = append(xs, a[0]+t*(b[0]-a[0]))
			}
		}
		sort.Float64s(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			x0, x1 := xs[i], xs[i+1]
			if x0 > x1 {
				x0, x1 = x1, x0
			}
			for x := int(x0 * float64(w)); x < int(x1*float64(w)) && x < w; x++ {
				if x >= 0 {
					mask[y*w+x] = true
				}
			}
		}
	}
}

// readPGM parses one binary PGM (P5) frame from the stream.
func readPGM(r *bufio.Reader) ([]byte, int, int, error) {
	token := func() (string, error) {
		for {
			c, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			if c == '#' { // comment
				for c != '\n' {
					if c, err = r.ReadByte(); err != nil {
						return "", err
					}
				}
				continue
			}
			if c > ' ' {
				buf := []byte{c}
				for {
					c, err = r.ReadByte()
					if err != nil {
						return "", err
					}
					if c <= ' ' {
						return string(buf), nil
					}
					buf = append(buf, c)
				}
			}
		}
	}
	magic, err := token()
	if err != nil {
		return nil, 0, 0, err
	}
	if magic != "P5" {
		return nil, 0, 0, fmt.Errorf("bad pgm magic %q", magic)
	}
	var w, h, maxv int
	for _, dst := range []*int{&w, &h, &maxv} {
		tok, err := token()
		if err != nil {
			return nil, 0, 0, err
		}
		if _, err := fmt.Sscanf(tok, "%d", dst); err != nil {
			return nil, 0, 0, err
		}
	}
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, 0, 0, fmt.Errorf("bad pgm dims %dx%d", w, h)
	}
	img := make([]byte, w*h)
	if _, err := io.ReadFull(r, img); err != nil {
		return nil, 0, 0, err
	}
	return img, w, h, nil
}
