package v2rayxhttp

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// splitConn glues an independent reader and writer (the download response
// and the upload requests of an XHTTP session) into a net.Conn.
type splitConn struct {
	writer     io.WriteCloser
	reader     io.ReadCloser
	remoteAddr net.Addr
	localAddr  net.Addr
	onClose    func()
	closeOnce  sync.Once
	closed     atomic.Bool
}

func (c *splitConn) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	if err != nil && c.closed.Load() {
		// the body was torn down by our own Close, that is a plain EOF
		err = io.EOF
	}
	return n, err
}

func (c *splitConn) Write(b []byte) (int, error) {
	return c.writer.Write(b)
}

func (c *splitConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		err = E.Errors(c.writer.Close(), c.reader.Close())
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

// NeedAdditionalReadDeadline makes sing-box wrap the conn with a goroutine based read
// deadline, the HTTP bodies underneath cannot be interrupted by a deadline.
func (c *splitConn) NeedAdditionalReadDeadline() bool { return true }

func (c *splitConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *splitConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *splitConn) SetDeadline(t time.Time) error      { return nil }
func (c *splitConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *splitConn) SetWriteDeadline(t time.Time) error { return nil }

var placeholderAddr net.Addr = &net.TCPAddr{IP: net.IPv4zero}

// waitReadCloser lets a caller start reading before the HTTP response arrived.
type waitReadCloser struct {
	ready  chan struct{}
	access sync.Mutex
	reader io.ReadCloser
	err    error
	closed bool
}

func newWaitReadCloser() *waitReadCloser {
	return &waitReadCloser{ready: make(chan struct{})}
}

func (w *waitReadCloser) set(reader io.ReadCloser, err error) {
	w.access.Lock()
	defer w.access.Unlock()
	select {
	case <-w.ready:
		if reader != nil {
			reader.Close()
		}
		return
	default:
	}
	if w.closed && reader != nil {
		reader.Close()
		reader = nil
		err = io.ErrClosedPipe
	}
	w.reader = reader
	w.err = err
	close(w.ready)
}

func (w *waitReadCloser) Read(b []byte) (int, error) {
	<-w.ready
	w.access.Lock()
	reader, err := w.reader, w.err
	w.access.Unlock()
	if reader == nil {
		if err == nil {
			err = io.ErrClosedPipe
		}
		return 0, err
	}
	return reader.Read(b)
}

func (w *waitReadCloser) Close() error {
	w.access.Lock()
	w.closed = true
	reader := w.reader
	select {
	case <-w.ready:
	default:
		w.err = io.ErrClosedPipe
		close(w.ready)
	}
	w.access.Unlock()
	if reader != nil {
		return reader.Close()
	}
	return nil
}

// uploadBuffer batches consecutive Writes so that they are sent as large POST
// bodies. Write blocks while the buffer is full.
type uploadBuffer struct {
	access sync.Mutex
	cond   *sync.Cond
	data   []byte
	limit  int
	closed bool
	err    error
}

func newUploadBuffer(limit int) *uploadBuffer {
	b := &uploadBuffer{limit: max(limit, 1)}
	b.cond = sync.NewCond(&b.access)
	return b
}

func (b *uploadBuffer) Write(p []byte) (int, error) {
	b.access.Lock()
	defer b.access.Unlock()
	written := 0
	for len(p) > 0 {
		for len(b.data) >= b.limit && !b.closed {
			b.cond.Wait()
		}
		if b.closed {
			if b.err != nil {
				return written, b.err
			}
			return written, io.ErrClosedPipe
		}
		n := min(len(p), b.limit-len(b.data))
		b.data = append(b.data, p[:n]...)
		p = p[n:]
		written += n
		b.cond.Broadcast()
	}
	return written, nil
}

// take blocks until data is available and returns at most size bytes.
// ok is false once the buffer is closed and drained.
func (b *uploadBuffer) take(size int) (chunk []byte, ok bool) {
	b.access.Lock()
	defer b.access.Unlock()
	for len(b.data) == 0 && !b.closed {
		b.cond.Wait()
	}
	if len(b.data) == 0 {
		return nil, false
	}
	n := min(size, len(b.data))
	chunk = make([]byte, n)
	copy(chunk, b.data[:n])
	b.data = b.data[n:]
	if len(b.data) == 0 {
		b.data = nil
	}
	b.cond.Broadcast()
	return chunk, true
}

// Close lets the consumer drain the remaining data.
func (b *uploadBuffer) Close() error {
	b.access.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.access.Unlock()
	return nil
}

// abort drops the pending data and fails further writes with err.
func (b *uploadBuffer) abort(err error) {
	b.access.Lock()
	b.closed = true
	b.err = err
	b.data = nil
	b.cond.Broadcast()
	b.access.Unlock()
}
