// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeGrabber is a scripted backend. Everything the stream does — the buffer
// rotation, the freshness flag, the statistics, the close protocol — is
// exercised against it, on every platform, with no display anywhere.
type fakeGrabber struct {
	mu      sync.Mutex
	bufs    [][]byte
	w, h    int
	stride  int
	calls   int
	lastIdx int
	err     error // returned once the call count reaches errAt
	errAt   int
	closed  int
	block   chan struct{} // when non-nil, Grab waits on it
	seen    []int         // the buffer indices Grab was given, in order
}

func newFakeGrabber(n, w, h int) *fakeGrabber {
	g := &fakeGrabber{w: w, h: h, stride: w * 4, lastIdx: -1}
	for i := 0; i < n; i++ {
		g.bufs = append(g.bufs, make([]byte, g.stride*h))
	}
	return g
}

func (g *fakeGrabber) Buffers() int { return len(g.bufs) }

// Interrupt releases a Grab that is parked on the block channel, which is what
// a real backend's connection close does to a request waiting for its reply.
func (g *fakeGrabber) Interrupt() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.block != nil {
		select {
		case <-g.block: // already released
		default:
			close(g.block)
		}
		g.block = nil
	}
}

func (g *fakeGrabber) Source() string    { return "fake" }
func (g *fakeGrabber) Converts() bool    { return false }
func (g *fakeGrabber) Transport() string { return "fake/none" }

func (g *fakeGrabber) Grab(i int) (Frame, error) {
	g.mu.Lock()
	block := g.block
	g.calls++
	n := g.calls
	g.lastIdx = i
	g.seen = append(g.seen, i)
	err := g.err
	errAt := g.errAt
	g.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil && n >= errAt {
		return Frame{}, err
	}
	// Stamp the frame so a test can tell one from another and prove the
	// stream never hands back a buffer it is about to overwrite.
	buf := g.bufs[i]
	for j := range buf {
		buf[j] = byte(n)
	}
	return Frame{Pix: buf, Width: g.w, Height: g.h, Stride: g.stride}, nil
}

func (g *fakeGrabber) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed++
	return nil
}

func (g *fakeGrabber) indices() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]int, len(g.seen))
	copy(out, g.seen)
	return out
}

func (g *fakeGrabber) closeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// resolved builds the options a stream would have been created with.
func resolved(t *testing.T, o Options, w, h int) Options {
	t.Helper()
	r, err := o.resolve(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStreamDeliversFrames(t *testing.T) {
	g := newFakeGrabber(4, 8, 4)
	s := newStream(g, resolved(t, Options{FPS: 200, QueueDepth: 4}, 8, 4))
	defer func() { _ = s.Close() }()

	if s.Source() != "fake" || s.Transport() != "fake/none" || s.Converts() {
		t.Errorf("Source/Transport/Converts = %q, %q, %v", s.Source(), s.Transport(), s.Converts())
	}
	if s.Options().FPS != 200 || s.Options().QueueDepth != 4 {
		t.Errorf("Options = %+v", s.Options())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, err := s.WaitFrame(ctx)
	if err != nil {
		t.Fatalf("WaitFrame: %v", err)
	}
	if f.Width != 8 || f.Height != 4 || f.Stride != 32 || f.Seq == 0 || f.At.IsZero() {
		t.Fatalf("Frame = %+v", f)
	}
	// The frame just handed out is not fresh a second time.
	if _, fresh := s.Frame(); fresh {
		t.Error("Frame reported the already-handed frame as fresh")
	}
	// A later frame is.
	f2, err := s.WaitFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f2.Seq <= f.Seq {
		t.Errorf("the second frame's Seq is %d, not past %d", f2.Seq, f.Seq)
	}
}

func TestStreamFrameBeforeAnythingArrives(t *testing.T) {
	g := newFakeGrabber(3, 4, 4)
	g.block = make(chan struct{})
	s := newStream(g, resolved(t, Options{FPS: 200}, 4, 4))
	f, fresh := s.Frame()
	if fresh || f.Valid() {
		t.Errorf("Frame before the first capture = %+v, %v", f, fresh)
	}
	_ = s.Close()
}

func TestStreamFrameDoesNotAllocate(t *testing.T) {
	g := newFakeGrabber(4, 16, 16)
	s := newStream(g, resolved(t, Options{FPS: 500}, 16, 16))
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.WaitFrame(ctx); err != nil {
		t.Fatal(err)
	}
	// This is the contract the whole design exists for: the consumer's
	// per-frame call must not allocate.
	if got := testing.AllocsPerRun(500, func() { s.Frame() }); got != 0 {
		t.Fatalf("Frame allocated %v times per run", got)
	}
}

func TestStreamNeverReusesTheLentBuffer(t *testing.T) {
	// The borrow contract: the buffer handed to the consumer must not be the
	// one the capture loop writes next.
	g := newFakeGrabber(3, 4, 4)
	s := newStream(g, resolved(t, Options{FPS: 1000, QueueDepth: 3}, 4, 4))
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.WaitFrame(ctx); err != nil {
		t.Fatal(err)
	}
	held, _ := s.Frame()
	heldIdx := s.heldIndex()
	// Let the loop run for a while with the buffer lent out.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.Stats().Frames > 20 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for _, i := range g.indices()[1:] {
		if i == heldIdx {
			t.Fatalf("the capture loop wrote into buffer %d while it was lent out", i)
		}
	}
	// And the lent bytes are still the ones that were handed over.
	first := held.Pix[0]
	for _, b := range held.Pix {
		if b != first {
			t.Fatal("the lent frame was overwritten mid-flight")
		}
	}
}

// heldIndex exposes the lent buffer index for the borrow test.
func (s *Stream) heldIndex() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldBuf
}

func TestStreamStats(t *testing.T) {
	g := newFakeGrabber(4, 4, 4)
	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))
	defer func() { _ = s.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Stats().Frames < 5 {
		time.Sleep(time.Millisecond)
	}
	st := s.Stats()
	if st.Frames < 5 {
		t.Fatalf("only %d frames after 3s", st.Frames)
	}
	if st.Last.IsZero() || st.Interval <= 0 {
		t.Errorf("Stats = %+v", st)
	}
	if st.FPS() <= 0 {
		t.Errorf("Stats.FPS() = %v", st.FPS())
	}
	// Nobody asked for those frames, so they were all superseded.
	if st.Superseded == 0 {
		t.Error("Superseded stayed at zero although no frame was ever collected")
	}
}

func TestStreamCaptureFailureStopsTheLoop(t *testing.T) {
	g := newFakeGrabber(3, 4, 4)
	want := errors.New("the drawable went away")
	g.mu.Lock()
	g.err, g.errAt = want, 2
	g.mu.Unlock()

	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The first frame arrives; the second fails and the error surfaces.
	if _, err := s.WaitFrame(ctx); err != nil {
		t.Fatalf("first WaitFrame: %v", err)
	}
	if _, err := s.WaitFrame(ctx); !errors.Is(err, want) {
		t.Fatalf("second WaitFrame reported %v, want %v", err, want)
	}
	if !errors.Is(s.Err(), want) {
		t.Errorf("Err() = %v", s.Err())
	}
	// Every later wait reports the same error rather than hanging.
	if _, err := s.WaitFrame(ctx); !errors.Is(err, want) {
		t.Errorf("a later WaitFrame reported %v", err)
	}
}

func TestStreamFirstGrabFailure(t *testing.T) {
	g := newFakeGrabber(3, 4, 4)
	want := errors.New("nothing to capture")
	g.mu.Lock()
	g.err, g.errAt = want, 1
	g.mu.Unlock()
	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.WaitFrame(ctx); !errors.Is(err, want) {
		t.Fatalf("WaitFrame reported %v, want %v", err, want)
	}
}

func TestStreamWaitFrameHonoursItsContext(t *testing.T) {
	g := newFakeGrabber(3, 4, 4)
	g.block = make(chan struct{})
	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.WaitFrame(ctx); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("WaitFrame reported %v, want ErrNoFrame", err)
	}
	_ = s.Close()
}

func TestStreamCloseIsIdempotent(t *testing.T) {
	g := newFakeGrabber(3, 4, 4)
	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := g.closeCount(); got != 1 {
		t.Errorf("the backend was closed %d times", got)
	}
	if f, fresh := s.Frame(); fresh || f.Valid() {
		t.Errorf("Frame after Close = %+v, %v", f, fresh)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.WaitFrame(ctx); !errors.Is(err, ErrClosed) {
		t.Errorf("WaitFrame after Close reported %v, want ErrClosed", err)
	}
	if s.Err() != nil {
		t.Errorf("Err() after a clean Close = %v, want nil", s.Err())
	}
}

func TestStreamCloseDoesNotHangBehindAStuckGrab(t *testing.T) {
	// The failure this guards against: a display server that stops answering
	// leaves Grab waiting for a reply that never comes, and a Close that
	// waited for the loop before interrupting it would hang for ever.
	g := newFakeGrabber(3, 4, 4)
	g.block = make(chan struct{})
	s := newStream(g, resolved(t, Options{FPS: 500}, 4, 4))

	waited := make(chan error, 1)
	go func() {
		_, err := s.WaitFrame(context.Background())
		waited <- err
	}()
	time.Sleep(20 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung behind a grab that never returned")
	}
	select {
	case err := <-waited:
		if !errors.Is(err, ErrClosed) && !errors.Is(err, ErrNoFrame) {
			t.Fatalf("the blocked WaitFrame reported %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitFrame did not wake when the stream closed")
	}
	// An interrupted grab is a Close, not a malfunction.
	if s.Err() != nil {
		t.Errorf("Err() after an interrupted Close = %v, want nil", s.Err())
	}
}

func TestStreamPublishAfterCloseIsDropped(t *testing.T) {
	// A frame that finishes capturing during Close must not resurrect the
	// stream's current frame.
	g := newFakeGrabber(3, 4, 4)
	s := newStream(g, resolved(t, Options{FPS: 1000}, 4, 4))
	_ = s.Close()
	s.publish(0, Frame{Pix: make([]byte, 64), Width: 4, Height: 4, Stride: 16})
	if f, fresh := s.Frame(); fresh || f.Valid() {
		t.Errorf("a post-Close publish was accepted: %+v, %v", f, fresh)
	}
}

// exhaustedGrabber has fewer buffers than the stream needs, which is the one
// case where the loop can find nothing to write into.
type exhaustedGrabber struct{ fakeGrabber }

func (g *exhaustedGrabber) Buffers() int { return 0 }

func TestStreamCountsATickItCannotServe(t *testing.T) {
	g := &exhaustedGrabber{*newFakeGrabber(1, 4, 4)}
	s := newStream(g, resolved(t, Options{FPS: 1000}, 4, 4))
	defer func() { _ = s.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Stats().Idle == 0 {
		time.Sleep(time.Millisecond)
	}
	if s.Stats().Idle == 0 {
		t.Fatal("a stream with no usable buffer never counted an idle tick")
	}
	if s.Stats().Frames != 0 {
		t.Errorf("a stream with no buffer produced %d frames", s.Stats().Frames)
	}
}

func TestStreamConcurrentReaders(t *testing.T) {
	// A compositor reads from its render goroutine while the capture loop
	// writes; -race is what makes this test worth having.
	g := newFakeGrabber(6, 32, 32)
	s := newStream(g, resolved(t, Options{FPS: 1000, QueueDepth: 6}, 32, 32))
	defer func() { _ = s.Close() }()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				f, _ := s.Frame()
				_ = f.Valid()
				_ = s.Stats()
				_ = s.Err()
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestWaitFrameOnALoopThatHasAlreadyStopped(t *testing.T) {
	// The loop is gone, with neither an error nor a Close to explain it: the
	// only honest answer is that no frame is coming.
	s := &Stream{heldBuf: -1, curBuf: -1,
		fresh: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	close(s.done)
	if _, err := s.WaitFrame(context.Background()); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("WaitFrame reported %v, want ErrNoFrame", err)
	}
	// With an error to report, that is what comes back instead.
	want := errors.New("the server hung up")
	s2 := &Stream{heldBuf: -1, curBuf: -1, err: want,
		fresh: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	close(s2.done)
	if _, err := s2.WaitFrame(context.Background()); !errors.Is(err, want) {
		t.Fatalf("WaitFrame reported %v, want %v", err, want)
	}
}

func TestFailWithAFreshnessPokeAlreadyPending(t *testing.T) {
	// Nobody is collecting frames, so the freshness channel is already full
	// when the capture fails. The failure must still be recorded, and must
	// not block the loop.
	g := newFakeGrabber(3, 4, 4)
	g.mu.Lock()
	g.err, g.errAt = errors.New("gone"), 2
	g.mu.Unlock()
	s := newStream(g, resolved(t, Options{FPS: 1000}, 4, 4))
	defer func() { _ = s.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if s.Err() == nil {
		t.Fatal("the capture failure was never recorded")
	}
}
