package netlimit

import (
	"net"
	"testing"
	"time"
)

func TestListenerCapsOpenConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Listener(ln, 2)
	defer l.Close()
	accepted := make(chan net.Conn, 10)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	first, second := <-accepted, <-accepted
	select {
	case <-accepted:
		t.Fatal("third connection accepted while two are open")
	case <-time.After(100 * time.Millisecond):
	}
	first.Close()
	first.Close() // closing twice frees one slot only
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(time.Second):
		t.Fatal("third connection not accepted after one closed")
	}
	second.Close()
}
