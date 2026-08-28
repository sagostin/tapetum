package live

import (
	"sync"
	"testing"
	"time"
)

func TestSubscribeRebasesPTS(t *testing.T) {
	h := NewHub()

	// Begin a stream and offer frames with absolute PTS.
	camID := "cam1"
	h.Begin(camID, false, "h264", []byte{0x67}, []byte{0x68}, nil)

	// Offer a keyframe at absolute PTS = 1,000,000 (about 11 seconds at 90kHz).
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 1_000_000)
	// P-frames after it.
	for i := 1; i < 10; i++ {
		h.Offer(camID, false, [][]byte{{0x41, 0x9a, 0x24, byte(i)}}, 1_000_000+int64(i)*3000)
	}

	// Subscribe — frames should be rebased so the first one is at PTS=0.
	_, _, _, _, ch, cancel, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe returned ok=false")
	}
	defer cancel()

	// Pull all the ring frames.
	got := []int64{}
	for i := 0; i < 10; i++ {
		select {
		case f := <-ch:
			got = append(got, f.PTS)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for frame %d", i)
		}
	}

	// First frame MUST be at PTS=0 (rebased), not the absolute PTS.
	if got[0] != 0 {
		t.Errorf("first frame PTS = %d, want 0 (rebased)", got[0])
	}

	// Deltas between frames MUST match the original Offer deltas.
	wantDeltas := []int64{3000, 3000, 3000, 3000, 3000, 3000, 3000, 3000, 3000}
	for i := 1; i < len(got); i++ {
		gotDelta := got[i] - got[i-1]
		if gotDelta != wantDeltas[i-1] {
			t.Errorf("frame %d delta = %d, want %d", i, gotDelta, wantDeltas[i-1])
		}
	}
}

func TestSubscribeLiveFramesAreRebased(t *testing.T) {
	h := NewHub()
	camID := "cam2"

	h.Begin(camID, false, "h264", nil, nil, nil)
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 5_000_000)

	_, _, _, _, ch, cancel, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe returned ok=false")
	}
	defer cancel()

	// Drain the ring buffer first.
	<-ch

	// Now offer a new live frame. Absolute PTS is 5_000_000 + 3000; rebased
	// version should be 3000.
	h.Offer(camID, false, [][]byte{{0x41, 0x9a, 0x24, 0x01}}, 5_000_000+3000)

	select {
	case f := <-ch:
		if f.PTS != 3000 {
			t.Errorf("live frame PTS = %d, want 3000 (rebased from %d)", f.PTS, 5_000_000+3000)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for live frame")
	}
}

func TestConcurrentSubscribersEachRebaseFromTheirOwnAnchor(t *testing.T) {
	h := NewHub()
	camID := "cam3"

	h.Begin(camID, false, "h264", nil, nil, nil)
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 10_000_000)

	_, _, _, _, ch1, cancel1, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe #1 failed")
	}
	<-ch1 // drain ring

	// Move the timeline forward before subscriber #2 joins.
	h.Offer(camID, false, [][]byte{{0x41, 0x9a, 0x24, 0x02}}, 10_000_000+30_000)

	_, _, _, _, ch2, cancel2, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe #2 failed")
	}
	defer cancel1()
	defer cancel2()

	// Subscriber #1 already saw its first frame at PTS=0; the ring replay for
	// subscriber #2 should also start at PTS=0 (its own anchor), even though
	// the absolute PTS is 10,030,000.
	select {
	case f := <-ch2:
		if f.PTS != 0 {
			t.Errorf("sub #2 first frame PTS = %d, want 0", f.PTS)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestCancelClosesRawSafely(t *testing.T) {
	h := NewHub()
	camID := "cam4"

	h.Begin(camID, false, "h264", nil, nil, nil)
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 100)

	_, _, _, _, ch, cancel, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe failed")
	}
	<-ch

	// Calling cancel twice must not panic — sync.Once inside subscriber.close
	// guarantees the underlying channel is only closed once.
	cancel()
	cancel()

	// The output channel must eventually close so the consumer's `for range`
	// can terminate.
	select {
	case _, open := <-ch:
		if open {
			// got a frame — drain it and re-check
			<-ch
		}
	case <-time.After(time.Second):
		t.Fatal("output channel did not close within 1s after cancel")
	}
}

func TestSlowConsumerCloseIsIdempotent(t *testing.T) {
	h := NewHub()
	camID := "cam5"

	h.Begin(camID, false, "h264", nil, nil, nil)
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 200)

	_, _, _, _, ch, cancel, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe failed")
	}

	// Don't drain. The first Offer's slow-consumer path will close newSub.raw;
	// then the cancel call below will also try to close it. Both must be
	// safe.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Hammer Offer until the slow-consumer path drops us.
		for i := 0; i < subBuf+50; i++ {
			h.Offer(camID, false, [][]byte{{0x41, byte(i)}}, 1000+int64(i))
		}
		cancel()
	}()
	wg.Wait()

	// Drain whatever's left.
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("output channel never closed")
		}
	}
}
