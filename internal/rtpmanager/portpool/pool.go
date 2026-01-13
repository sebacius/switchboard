package portpool

import (
	"fmt"
	"sync"
)

// PortPool manages a pool of RTP ports for media sessions.
// Ports are allocated in pairs (even for RTP, odd for RTCP).
type PortPool struct {
	mu        sync.Mutex
	minPort   int
	maxPort   int
	available map[int]bool // port -> available
	allocated map[int]bool // port -> allocated
}

// NewPortPool creates a new port pool with the given range.
// minPort should be even, maxPort should be odd.
func NewPortPool(minPort, maxPort int) *PortPool {
	// Ensure minPort is even
	if minPort%2 != 0 {
		minPort++
	}

	available := make(map[int]bool)
	// Add even ports (RTP ports) to available pool
	for port := minPort; port < maxPort; port += 2 {
		available[port] = true
	}

	return &PortPool{
		minPort:   minPort,
		maxPort:   maxPort,
		available: available,
		allocated: make(map[int]bool),
	}
}

// Allocate returns a pair of ports (RTP, RTCP) or an error if none available.
func (p *PortPool) Allocate() (rtpPort, rtcpPort int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find first available port
	for port := range p.available {
		delete(p.available, port)
		p.allocated[port] = true

		rtpPort = port
		rtcpPort = port + 1
		return rtpPort, rtcpPort, nil
	}

	return 0, 0, fmt.Errorf("no ports available in pool (range %d-%d)", p.minPort, p.maxPort)
}

// Release returns a port pair to the pool.
func (p *PortPool) Release(rtpPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.allocated[rtpPort]; ok {
		delete(p.allocated, rtpPort)
		p.available[rtpPort] = true
	}
}

// Available returns the number of available port pairs.
func (p *PortPool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.available)
}

// Allocated returns the number of allocated port pairs.
func (p *PortPool) Allocated() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocated)
}
