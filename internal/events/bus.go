// Package events provides the in-process event bus and the Postgres-backed
// event store (docs/07-detection-notifications.md, Event-Driven Internals).
package events

import "sync"

// Bus is a single-process pub/sub. Topics are free-form ("motion.started",
// "event.created", …). Publishing is non-blocking: slow subscribers with a
// full buffer miss events rather than stalling producers.
type Bus struct {
	mu   sync.RWMutex
	subs map[string]map[chan Message]struct{}
}

// Message is one published event.
type Message struct {
	Topic    string
	CameraID string
	Payload  any
}

const subBuf = 64

func NewBus() *Bus { return &Bus{subs: map[string]map[chan Message]struct{}{}} }

// Publish fans out to all subscribers of the topic.
func (b *Bus) Publish(m Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[m.Topic] {
		select {
		case ch <- m:
		default:
		}
	}
}

// Subscribe returns a channel receiving the topic's messages and a cancel
// func. The caller must drain until cancel is called.
func (b *Bus) Subscribe(topic string) (<-chan Message, func()) {
	ch := make(chan Message, subBuf)
	b.mu.Lock()
	set, ok := b.subs[topic]
	if !ok {
		set = map[chan Message]struct{}{}
		b.subs[topic] = set
	}
	set[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(set, ch)
		b.mu.Unlock()
	}
}
