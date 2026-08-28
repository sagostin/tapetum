package onvif

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	onvifgo "github.com/0x524a/onvif-go"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/events"
)

// PullSupervisor runs a WS-Notification pull-point client per camera with an
// ONVIF endpoint, translating camera-native motion alarms into bus
// motion.started/motion.ended signals (OR'ed with software detection by the
// events manager).
type PullSupervisor struct {
	cams *camera.Store
	bus  *events.Bus
	log  *slog.Logger

	mu      sync.Mutex
	clients map[string]context.CancelFunc
}

func NewPullSupervisor(cams *camera.Store, bus *events.Bus) *PullSupervisor {
	return &PullSupervisor{
		cams:    cams,
		bus:     bus,
		log:     slog.With("component", "onvif-events"),
		clients: map[string]context.CancelFunc{},
	}
}

// Start launches pull clients for all eligible cameras.
func (p *PullSupervisor) Start(ctx context.Context) error {
	cams, err := p.cams.ListEnabled(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, c := range cams {
		if p.eligible(c) {
			p.add(c)
			n++
		}
	}
	p.log.Info("ONVIF pull-point started", "cameras", n)
	return nil
}

// Sync reconciles the pull client for a camera after CRUD changes.
func (p *PullSupervisor) Sync(ctx context.Context, camID string) {
	p.mu.Lock()
	p.removeLocked(camID)
	p.mu.Unlock()
	cam, err := p.cams.Get(ctx, camID)
	if err != nil || !p.eligible(cam) {
		return
	}
	p.add(cam)
}

// Remove stops the pull client for a deleted camera.
func (p *PullSupervisor) Remove(camID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeLocked(camID)
}

// Close stops all pull clients (daemon shutdown).
func (p *PullSupervisor) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, cancel := range p.clients {
		cancel()
		delete(p.clients, id)
	}
}

// eligible: ONVIF endpoint configured and motion detection enabled (hardware
// events are an additional motion signal source — docs/07).
func (p *PullSupervisor) eligible(c *camera.Camera) bool {
	if c == nil || !c.Enabled || c.OnvifEndpoint == nil || *c.OnvifEndpoint == "" {
		return false
	}
	en, _ := c.MotionConfig["enabled"].(bool)
	return en
}

func (p *PullSupervisor) add(c *camera.Camera) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	p.clients[c.ID] = cancel
	go p.run(ctx, c.ID, *c.OnvifEndpoint, c.Username, c.PasswordEnc)
}

func (p *PullSupervisor) removeLocked(camID string) {
	if cancel, ok := p.clients[camID]; ok {
		cancel()
		delete(p.clients, camID)
	}
}

// run maintains a subscription + pull loop with reconnection backoff.
func (p *PullSupervisor) run(ctx context.Context, camID, endpoint, username string, passwordEnc []byte) {
	password, err := p.cams.DecryptPassword(passwordEnc)
	if err != nil {
		p.log.Error("decrypt password", "camera", camID, "err", err)
		return
	}
	log := p.log.With("camera", camID)
	backoff := 5 * time.Second
	active := false

	for ctx.Err() == nil {
		client, err := NewClient(endpoint, username, password)
		if err != nil {
			log.Warn("onvif client", "err", err)
		} else {
			// one hour; Renew is skipped — a dropped subscription is
			// simply re-created by the reconnect loop
			term := time.Hour
			sub, err := client.CreatePullPointSubscription(ctx,
				"tns1:RuleEngine/CellMotionDetector/Motion | tns1:VideoSource/MotionAlarm",
				&term, "")
			if err != nil {
				log.Debug("pull-point subscribe failed", "err", err)
			} else {
				backoff = 5 * time.Second
				active = p.pullLoop(ctx, client, sub.SubscriptionReference, camID, active, log)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// pullLoop pulls messages until error; returns the last motion state so a
// reconnect doesn't re-fire a stale start.
func (p *PullSupervisor) pullLoop(ctx context.Context, client *onvifgo.Client,
	ref, camID string, active bool, log *slog.Logger,
) bool {
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		msgs, err := client.PullMessages(pctx, ref, 10*time.Second, 20)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Debug("pull messages", "err", err)
			}
			return active
		}
		for _, m := range msgs {
			motion, ok := motionOf(m)
			if !ok || motion == active {
				continue
			}
			active = motion
			now := time.Now()
			if motion {
				p.bus.Publish(events.Message{Topic: "motion.started", CameraID: camID,
					Payload: map[string]any{"ts": now, "source": "onvif"}})
			} else {
				p.bus.Publish(events.Message{Topic: "motion.ended", CameraID: camID,
					Payload: map[string]any{"end": now, "source": "onvif"}})
			}
		}
	}
	return active
}

// motionOf extracts a motion boolean from a notification: CellMotionDetector
// reports Data IsMotion, MotionAlarm reports Data State.
func motionOf(m onvifgo.NotificationMessage) (bool, bool) {
	for _, item := range m.Message.Data {
		if item.Name == "IsMotion" || item.Name == "State" {
			v := strings.EqualFold(item.Value, "true") || item.Value == "1"
			return v, true
		}
	}
	return false, false
}
