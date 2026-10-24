package v2rayxhttp

import (
	"io"
	"sync"
)

// bufferedPipe is an io.Pipe with a buffer: Write returns as soon as the data is queued instead
// of waiting for the HTTP transport to pick it up, so the producer and the HTTP framing run
// concurrently. Write blocks only while the buffer is full.
//
// The buffer starts small and grows on demand up to its limit, since most streams (DNS, UDP
// flows, idle connections) never queue more than a few KB, and it is released as soon as the
// pipe is finished.
type bufferedPipe struct {
	access       sync.Mutex
	cond         *sync.Cond
	data         []byte
	limit        int
	start, count int
	writeClosed  bool
	writeErr     error // returned by Read once the buffer is drained
	readErr      error // returned by Write, set when the reader goes away
}

const pipeInitialSize = 4 << 10

// grow doubles the buffer, up to the limit, keeping the queued bytes in order.
func (p *bufferedPipe) grow() {
	next := make([]byte, min(len(p.data)*2, p.limit))
	first := min(p.count, len(p.data)-p.start)
	copy(next, p.data[p.start:p.start+first])
	copy(next[first:], p.data[:p.count-first])
	p.data, p.start = next, 0
}

func newBufferedPipe(size int) (*pipeReader, *pipeWriter) {
	pipe := &bufferedPipe{data: make([]byte, min(size, pipeInitialSize)), limit: size}
	pipe.cond = sync.NewCond(&pipe.access)
	return &pipeReader{pipe}, &pipeWriter{pipe}
}

func (p *bufferedPipe) write(b []byte) (int, error) {
	p.access.Lock()
	defer p.access.Unlock()
	written := 0
	for len(b) > 0 {
		for p.count == len(p.data) && p.readErr == nil && !p.writeClosed {
			if len(p.data) < p.limit {
				p.grow()
				break
			}
			p.cond.Wait()
		}
		if p.readErr != nil {
			return written, p.readErr
		}
		if p.writeClosed {
			return written, io.ErrClosedPipe
		}
		end := (p.start + p.count) % len(p.data)
		n := min(len(b), len(p.data)-p.count, len(p.data)-end)
		copy(p.data[end:], b[:n])
		p.count += n
		b = b[n:]
		written += n
		p.cond.Broadcast()
	}
	return written, nil
}

func (p *bufferedPipe) read(b []byte) (int, error) {
	p.access.Lock()
	defer p.access.Unlock()
	for p.count == 0 && !p.writeClosed && p.readErr == nil {
		p.cond.Wait()
	}
	if p.readErr != nil {
		return 0, io.ErrClosedPipe
	}
	if p.count == 0 {
		p.data = nil
		if p.writeErr != nil {
			return 0, p.writeErr
		}
		return 0, io.EOF
	}
	n := min(len(b), p.count, len(p.data)-p.start)
	copy(b, p.data[p.start:p.start+n])
	p.start = (p.start + n) % len(p.data)
	p.count -= n
	p.cond.Broadcast()
	return n, nil
}

type pipeReader struct{ pipe *bufferedPipe }

func (r *pipeReader) Read(b []byte) (int, error) { return r.pipe.read(b) }

func (r *pipeReader) Close() error { return r.CloseWithError(nil) }

// CloseWithError makes pending and later writes fail with err (io.ErrClosedPipe when nil).
func (r *pipeReader) CloseWithError(err error) error {
	if err == nil {
		err = io.ErrClosedPipe
	}
	r.pipe.access.Lock()
	if r.pipe.readErr == nil {
		r.pipe.readErr = err
		r.pipe.data = nil
	}
	r.pipe.cond.Broadcast()
	r.pipe.access.Unlock()
	return nil
}

type pipeWriter struct{ pipe *bufferedPipe }

func (w *pipeWriter) Write(b []byte) (int, error) { return w.pipe.write(b) }

// Close lets the reader drain the buffered data and then see io.EOF.
func (w *pipeWriter) Close() error {
	w.pipe.access.Lock()
	w.pipe.writeClosed = true
	w.pipe.cond.Broadcast()
	w.pipe.access.Unlock()
	return nil
}
