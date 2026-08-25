// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import (
	"context"
	"sync"
	"time"
)

// grabber is the backend half of a capture: the part that knows how to get
// pixels out of a display server. Everything above it — the buffer rotation,
// the freshness bookkeeping, the statistics, the close protocol — is in this
// file, is platform-independent, and is what the test suite exercises against
// a scripted grabber on every platform.
type grabber interface {
	// Buffers is how many distinct frame buffers the grabber cycles through.
	// It is at least [MinQueueDepth], so the loop can always find a buffer
	// that is neither the published frame nor the one lent to the consumer.
	Buffers() int
	// Grab captures one frame into buffer i and returns a borrowed view of
	// it. The returned Frame's Seq and At are filled in by the caller.
	Grab(i int) (Frame, error)
	// Source names what is being captured, for logs and [Stream.Source].
	Source() string
	// Converts reports whether each frame needs a pixel-format conversion
	// pass, which is information a consumer chasing milliseconds wants.
	Converts() bool
	// Transport names how the pixels get here, e.g. "X11/MIT-SHM".
	Transport() string
	// Interrupt aborts a Grab that is in flight, so [Stream.Close] cannot
	// block behind a display server that has stopped answering. It must be
	// safe to call concurrently with Grab, and it must NOT free anything Grab
	// is still using — releasing the buffers is Close's job, and Close only
	// runs once the capture loop has stopped.
	Interrupt()
	// Close releases the backend's resources. It is called exactly once.
	Close() error
}

// Stream is a live capture. Create one with [CaptureDisplay] or
// [CaptureWindow]; stop it with [Stream.Close], which is idempotent.
//
// A Stream runs one goroutine that grabs a frame every tick and publishes it.
// The consumer never blocks behind the capture: [Stream.Frame] takes whatever
// the most recent published frame is and says whether it is new.
type Stream struct {
	opt    Options
	source string
	src    grabber

	mu       sync.Mutex
	cur      Frame  // most recently published frame
	curBuf   int    // buffer index cur lives in
	heldBuf  int    // buffer index lent to the consumer, -1 when none
	handed   uint64 // Seq of the frame most recently handed to the consumer
	seq      uint64
	stats    Stats
	err      error
	closed   bool
	closing  bool
	converts bool
	xport    string

	fresh chan struct{} // capacity 1; a publish pokes it
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

// newStream wires a grabber to the portable stream machinery and starts the
// capture loop. opt must already be resolved.
func newStream(src grabber, opt Options) *Stream {
	s := &Stream{
		opt:      opt,
		source:   src.Source(),
		src:      src,
		heldBuf:  -1,
		curBuf:   -1,
		converts: src.Converts(),
		xport:    src.Transport(),
		fresh:    make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.run(tickInterval(opt.FPS))
	return s
}

// Options returns the stream's resolved options: every zero field replaced by
// the default that was actually used.
func (s *Stream) Options() Options { return s.opt }

// Source names what is being captured, e.g. "display HDMI-1" or
// "window 0x1400007".
func (s *Stream) Source() string { return s.source }

// Transport names how a frame's pixels reach this process, for logs and for a
// consumer that wants to know whether it got the fast path. On the X11 backend
// it is "X11/MIT-SHM" when the server writes straight into a shared segment
// and "X11/GetImage" when every pixel comes back through the socket.
func (s *Stream) Transport() string { return s.xport }

// Converts reports whether each frame goes through a pixel-format conversion
// pass. It is false on the common case — a little-endian server with a
// depth-24 TrueColor visual already hands back BGRA — and true when the
// server's visual forced a rewrite.
func (s *Stream) Converts() bool { return s.converts }

// run is the capture loop. It grabs one frame immediately, so a consumer that
// calls WaitFrame straight away does not sit through a whole tick, then keeps
// grabbing on the tick.
func (s *Stream) run(interval time.Duration) {
	defer close(s.done)
	if !s.grabOnce() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if !s.grabOnce() {
				return
			}
		}
	}
}

// grabOnce captures into a free buffer and publishes it. It reports whether
// the loop should continue.
func (s *Stream) grabOnce() bool {
	i, ok := s.nextBuffer()
	if !ok {
		// Every buffer is spoken for. This cannot happen with a queue depth
		// of three or more, but counting it is cheaper than asserting it.
		s.mu.Lock()
		s.stats.Idle++
		s.mu.Unlock()
		return true
	}
	f, err := s.src.Grab(i)
	if err != nil {
		s.fail(err)
		return false
	}
	s.publish(i, f)
	return true
}

// nextBuffer picks a buffer index that is neither the published frame's nor
// the one lent to the consumer.
func (s *Stream) nextBuffer() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.src.Buffers()
	for i := 0; i < n; i++ {
		if i != s.curBuf && i != s.heldBuf {
			return i, true
		}
	}
	return 0, false
}

// publish makes f the current frame.
func (s *Stream) publish(i int, f Frame) {
	now := time.Now()
	s.mu.Lock()
	// Once Close has started, a frame that was already in flight is dropped
	// rather than published: the consumer has said it is done.
	if s.closed || s.closing {
		s.mu.Unlock()
		return
	}
	if s.cur.Seq > s.handed {
		// The frame we are replacing was never asked for.
		s.stats.Superseded++
	}
	s.seq++
	f.Seq = s.seq
	f.At = now
	s.cur = f
	s.curBuf = i
	s.stats.Frames++
	if !s.stats.Last.IsZero() {
		s.stats.Interval = now.Sub(s.stats.Last)
	}
	s.stats.Last = now
	s.mu.Unlock()

	select {
	case s.fresh <- struct{}{}:
	default:
	}
}

// fail records the first capture error and wakes any waiter.
func (s *Stream) fail(err error) {
	s.mu.Lock()
	// A grab aborted by Close is not a malfunction; it is the Close.
	if s.err == nil && !s.closing {
		s.err = err
	}
	s.mu.Unlock()
	select {
	case s.fresh <- struct{}{}:
	default:
	}
}

// Frame returns the most recent captured frame and whether it is newer than
// the one the previous call returned.
//
// The returned Frame BORROWS the capture buffer. It stays valid until the
// capture loop cycles back round to that buffer, which with the default
// [Options.QueueDepth] is several frames away; the stream will not reuse the
// buffer handed out by the most recent Frame call before the call after it.
//
// It allocates nothing.
func (s *Stream) Frame() (Frame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cur.Seq == 0 {
		return Frame{}, false
	}
	fresh := s.cur.Seq > s.handed
	s.handed = s.cur.Seq
	s.heldBuf = s.curBuf
	return s.cur, fresh
}

// WaitFrame blocks until a frame newer than the one the previous [Stream.Frame]
// or WaitFrame call returned is available, then returns it. It reports
// [ErrNoFrame] when ctx expires first, [ErrClosed] after [Stream.Close], and
// the capture error when the loop has failed.
func (s *Stream) WaitFrame(ctx context.Context) (Frame, error) {
	for {
		s.mu.Lock()
		switch {
		case s.closed:
			s.mu.Unlock()
			return Frame{}, ErrClosed
		case s.err != nil:
			err := s.err
			s.mu.Unlock()
			return Frame{}, err
		case s.cur.Seq > s.handed:
			s.handed = s.cur.Seq
			s.heldBuf = s.curBuf
			f := s.cur
			s.mu.Unlock()
			return f, nil
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return Frame{}, ErrNoFrame
		case <-s.done:
			// The capture loop has stopped, so no frame is ever coming. If
			// it stopped for a reason — an error, or a Close — one more turn
			// round the top of the loop reports THAT, which is more use than
			// a bare "no frame".
			s.mu.Lock()
			silent := s.err == nil && !s.closed
			s.mu.Unlock()
			if silent {
				return Frame{}, ErrNoFrame
			}
		case <-s.fresh:
		}
	}
}

// Stats reports what the stream has seen since it started.
func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Err reports the error that stopped the capture loop, or nil while it is
// running. A closed stream reports the error it stopped on, not [ErrClosed].
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close stops the capture and releases the backend's resources.
//
// It interrupts a capture that is in flight before waiting for the loop, so a
// display server that has stopped answering cannot make Close hang, and it
// releases the buffers only once the loop can no longer be writing into them.
//
// It is idempotent and safe to call from any goroutine; every subsequent
// [Stream.Frame] reports no frame and every [Stream.WaitFrame] reports
// [ErrClosed].
func (s *Stream) Close() error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.stop)
		// Abort whatever the loop is waiting on FIRST, so a server that has
		// stopped answering cannot make Close hang. Only then wait for the
		// loop to leave, and only then release the buffers it was using.
		s.src.Interrupt()
		<-s.done
		s.mu.Lock()
		s.closed = true
		s.cur = Frame{}
		s.mu.Unlock()
		err = s.src.Close()
		select {
		case s.fresh <- struct{}{}:
		default:
		}
	})
	return err
}
