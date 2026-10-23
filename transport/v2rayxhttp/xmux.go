package v2rayxhttp

import (
	"io"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// xmux keeps a small pool of HTTP connections (a "mux client" each) and hands
// them out per XHTTP session, mirroring xray-core's XmuxManager.

type xmuxConn interface {
	isClosed() bool
	// isReady reports whether the underlying connection is established
	isReady() bool
	// outstanding is the number of request body bytes currently being sent
	outstanding() int64
	// warmUp starts dialing in the background so that a later request finds the connection ready
	warmUp()
	io.Closer
}

type xmuxClient struct {
	conn         xmuxConn
	running      atomic.Int32
	leftUsage    int32
	leftRequests atomic.Int32
	unreusableAt time.Time
	notUsed      atomic.Bool
}

func (c *xmuxClient) addRunning() {
	c.running.Add(1)
}

func (c *xmuxClient) doneRunning() {
	c.running.Add(-1)
	c.maybeClose()
}

func (c *xmuxClient) maybeClose() {
	if c.notUsed.Load() && c.running.Load() <= 0 {
		c.conn.Close()
	}
}

type xmuxManager struct {
	access       sync.Mutex
	config       xmuxConfig
	concurrency  int32
	connections  int32
	newConn      func() xmuxConn
	clients      []*xmuxClient
	uploadCursor uint32
}

func newXmuxManager(config xmuxConfig, newConn func() xmuxConn) *xmuxManager {
	return &xmuxManager{
		config:      config,
		concurrency: config.maxConcurrency.rand(),
		connections: config.maxConnections.rand(),
		newConn:     newConn,
	}
}

func (m *xmuxManager) newClient() *xmuxClient {
	client := &xmuxClient{
		conn:      m.newConn(),
		leftUsage: -1,
	}
	if x := m.config.cMaxReuseTimes.rand(); x > 0 {
		client.leftUsage = x - 1
	}
	client.leftRequests.Store(math.MaxInt32)
	if x := m.config.hMaxRequestTimes.rand(); x > 0 {
		client.leftRequests.Store(x)
	}
	if x := m.config.hMaxReusableSecs.rand(); x > 0 {
		client.unreusableAt = time.Now().Add(time.Duration(x) * time.Second)
	}
	m.clients = append(m.clients, client)
	return client
}

func (m *xmuxManager) getClient() *xmuxClient {
	m.access.Lock()
	defer m.access.Unlock()
	for i := 0; i < len(m.clients); {
		client := m.clients[i]
		if client.conn.isClosed() ||
			client.leftUsage == 0 ||
			client.leftRequests.Load() <= 0 ||
			(!client.unreusableAt.IsZero() && time.Now().After(client.unreusableAt)) {
			client.notUsed.Store(true)
			client.maybeClose()
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
		} else {
			i++
		}
	}

	if len(m.clients) == 0 {
		return m.newClient()
	}
	if m.connections > 0 && len(m.clients) < int(m.connections) {
		if !m.hasReady() {
			return m.newClient()
		}
		// grow the pool in the background instead of making this request pay for a
		// new REALITY handshake, it is served by a connection that is already up
		m.newClient().conn.warmUp()
	}

	candidates := m.clients
	if m.concurrency > 0 {
		candidates = nil
		for _, client := range m.clients {
			if client.running.Load() < m.concurrency {
				candidates = append(candidates, client)
			}
		}
	}
	if ready := filterReady(candidates); len(ready) > 0 {
		candidates = ready
	}
	if len(candidates) == 0 {
		return m.newClient()
	}
	client := candidates[rand.IntN(len(candidates))]
	if client.leftUsage > 0 {
		client.leftUsage--
	}
	return client
}

func (m *xmuxManager) Close() error {
	m.access.Lock()
	defer m.access.Unlock()
	for _, client := range m.clients {
		client.conn.Close()
	}
	m.clients = nil
	return nil
}

// closeIdle drops connections without running requests; active streams are untouched.
func (m *xmuxManager) closeIdle() {
	m.access.Lock()
	defer m.access.Unlock()
	for _, client := range m.clients {
		client.conn.Close()
	}
}

// grow starts one more pooled connection in the background while the pool is below
// max_connections. A long packet-up upload calls it so that its POSTs can be spread.
func (m *xmuxManager) grow() {
	m.access.Lock()
	defer m.access.Unlock()
	if m.connections > 0 && len(m.clients) < int(m.connections) && m.hasReady() {
		m.newClient().conn.warmUp()
	}
}

func (m *xmuxManager) hasReady() bool {
	for _, client := range m.clients {
		if client.conn.isReady() {
			return true
		}
	}
	return false
}

func filterReady(clients []*xmuxClient) []*xmuxClient {
	var ready []*xmuxClient
	for _, client := range clients {
		if client.conn.isReady() {
			ready = append(ready, client)
		}
	}
	return ready
}

// uploadClient picks the connection for the next packet-up POST: the established one with the
// fewest bytes still in flight. Every connection has its own flow control window and congestion
// state, so spreading the POSTs lifts the per-connection ceiling (window / RTT) of uploads, and
// a connection that is slow (just opened, lossy) naturally receives less.
func (m *xmuxManager) uploadClient(current *xmuxClient) *xmuxClient {
	m.access.Lock()
	defer m.access.Unlock()
	now := time.Now()
	best := current
	bestLoad := current.conn.outstanding()
	for _, client := range m.clients {
		if client == current || !client.conn.isReady() || client.conn.isClosed() || client.leftRequests.Load() <= 0 ||
			(!client.unreusableAt.IsZero() && !now.Before(client.unreusableAt)) {
			continue
		}
		if load := client.conn.outstanding(); load < bestLoad {
			best, bestLoad = client, load
		}
	}
	return best
}
