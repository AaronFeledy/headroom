package tlsidentity

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

type sniffListener struct {
	net.Listener
	config    *tls.Config
	ready     chan net.Conn
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	pending   map[net.Conn]struct{}
	wg        sync.WaitGroup
	acceptErr error
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }

func Listen(listener net.Listener, certificate tls.Certificate) net.Listener {
	l := &sniffListener{Listener: listener, config: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}, ready: make(chan net.Conn), done: make(chan struct{}), pending: map[net.Conn]struct{}{}}
	l.wg.Add(1)
	go l.run()
	return l
}

func (l *sniffListener) run() {
	defer l.wg.Done()
	var delay time.Duration
	for {
		conn, err := l.Listener.Accept()
		if err != nil && isTransient(err) {
			// Like net/http, survive transient failures such as descriptor exhaustion.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			select {
			case <-l.done:
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		if err != nil {
			l.mu.Lock()
			l.acceptErr = err
			l.mu.Unlock()
			l.stop()
			return
		}
		l.mu.Lock()
		select {
		case <-l.done:
			l.mu.Unlock()
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				l.setError(err)
			}
			return
		default:
		}
		l.pending[conn] = struct{}{}
		l.wg.Add(1)
		l.mu.Unlock()
		go l.detect(conn)
	}
}

func (l *sniffListener) detect(conn net.Conn) {
	defer l.wg.Done()
	defer func() { l.mu.Lock(); delete(l.pending, conn); l.mu.Unlock() }()
	reader := bufio.NewReader(conn)
	err := conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first []byte
	if err == nil {
		first, err = reader.Peek(1)
	}
	if err == nil {
		err = conn.SetReadDeadline(time.Time{})
	}
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			l.setError(closeErr)
		}
		return
	}
	var accepted net.Conn = &bufferedConn{Conn: conn, reader: reader}
	if first[0] == 0x16 {
		accepted = tls.Server(accepted, l.config)
	}
	select {
	case l.ready <- accepted:
	case <-l.done:
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			l.setError(err)
		}
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ready:
		return conn, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.acceptErr != nil {
			return nil, l.acceptErr
		}
		return nil, net.ErrClosed
	}
}

func isTransient(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var temporary interface{ Temporary() bool }
	return errors.As(err, &temporary) && temporary.Temporary()
}

func (l *sniffListener) setError(err error) {
	l.mu.Lock()
	l.acceptErr = errors.Join(l.acceptErr, err)
	l.mu.Unlock()
}
func (l *sniffListener) stop() { l.once.Do(func() { close(l.done) }) }
func (l *sniffListener) Close() error {
	l.stop()
	err := l.Listener.Close()
	l.mu.Lock()
	for conn := range l.pending {
		if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}
	l.mu.Unlock()
	l.wg.Wait()
	return err
}
