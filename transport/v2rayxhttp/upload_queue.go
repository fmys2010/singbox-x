package v2rayxhttp

import (
	"container/heap"
	"io"
	"sync"
	"sync/atomic"

	E "github.com/sagernet/sing/common/exceptions"
)

// uploadQueue reorders the packet-up POST payloads by their sequence number
// into a single ordered byte stream. For stream-up the first (and only)
// pushed request body is read directly instead.
type uploadQueue struct {
	reader     atomic.Pointer[streamReader]
	pushed     chan packet
	heap       packetHeap
	nextSeq    uint64
	maxPackets int
	closed     chan struct{}
	closeOnce  sync.Once
}

type streamReader struct {
	io.Reader
	io.Closer
}

type packet struct {
	reader  *streamReader
	payload []byte
	seq     uint64
}

func newUploadQueue(maxPackets int) *uploadQueue {
	return &uploadQueue{
		pushed:     make(chan packet, maxPackets),
		closed:     make(chan struct{}),
		maxPackets: maxPackets,
	}
}

func (q *uploadQueue) isClosed() bool {
	select {
	case <-q.closed:
		return true
	default:
		return false
	}
}

func (q *uploadQueue) push(p packet) error {
	if q.reader.Load() != nil || (p.reader != nil && !q.reader.CompareAndSwap(nil, p.reader)) {
		return E.New("stream reader already exists")
	}
	select {
	case q.pushed <- p:
		if q.isClosed() {
			return E.New("packet queue closed")
		}
		return nil
	case <-q.closed:
		return E.New("packet queue closed")
	}
}

func (q *uploadQueue) Close() error {
	q.closeOnce.Do(func() { close(q.closed) })
	if reader := q.reader.Load(); reader != nil {
		return reader.Close()
	}
	return nil
}

func (q *uploadQueue) Read(b []byte) (int, error) {
	if reader := q.reader.Load(); reader != nil {
		return reader.Read(b)
	}
	if q.isClosed() {
		return 0, io.EOF
	}
	if len(q.heap) == 0 {
		select {
		case p := <-q.pushed:
			if p.reader != nil {
				return p.reader.Read(b)
			}
			heap.Push(&q.heap, p)
		case <-q.closed:
			return 0, io.EOF
		}
	}
	for len(q.heap) > 0 {
		p := heap.Pop(&q.heap).(packet)
		if p.seq == q.nextSeq {
			n := copy(b, p.payload)
			if n < len(p.payload) {
				p.payload = p.payload[n:]
				heap.Push(&q.heap, p)
			} else {
				q.nextSeq = p.seq + 1
			}
			if n == 0 && len(b) > 0 {
				if len(q.heap) == 0 {
					return q.Read(b)
				}
				continue
			}
			return n, nil
		}
		if p.seq > q.nextSeq {
			if len(q.heap) > q.maxPackets {
				// the reassembly buffer is too large; tear the connection down
				// to bound memory usage
				return 0, E.New("packet queue is too large")
			}
			heap.Push(&q.heap, p)
			select {
			case next := <-q.pushed:
				heap.Push(&q.heap, next)
			case <-q.closed:
				return 0, io.EOF
			}
		}
		// seq < nextSeq: duplicate, drop
	}
	return 0, nil
}

type packetHeap []packet

func (h packetHeap) Len() int           { return len(h) }
func (h packetHeap) Less(i, j int) bool { return h[i].seq < h[j].seq }
func (h packetHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *packetHeap) Push(x any) {
	*h = append(*h, x.(packet))
}

func (h *packetHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
