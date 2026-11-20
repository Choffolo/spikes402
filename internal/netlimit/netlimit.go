// Package netlimit caps the number of open connections of a listener.
package netlimit

import (
	"net"
	"sync"
)

// Listener returns a listener that keeps at most n connections open at
// once. Accept waits while n are open, so extra clients queue in the
// kernel's backlog instead of each costing the server a goroutine.
func Listener(l net.Listener, n int) net.Listener {
	return &limited{Listener: l, sem: make(chan struct{}, n)}
}

type limited struct {
	net.Listener
	sem chan struct{}
}

func (l *limited) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &conn{Conn: c, release: func() { <-l.sem }}, nil
}

type conn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
