//go:generate easyjson -output_filename result_easyjson.go socks5.go

package socks5

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/v-byte-cpu/sx/pkg/scan"
)

const (
	ScanType     = "socks"
	SOCKSVersion = 5

	defaultDialTimeout = 2 * time.Second
	defaultDataTimeout = 2 * time.Second
)

//easyjson:json
type ScanResult struct {
	ScanType string `json:"scan"`
	Version  int    `json:"version"`
	IP       string `json:"ip"`
	Port     uint16 `json:"port"`
	Auth     bool   `json:"auth,omitempty"`
}

func (r *ScanResult) String() string {
	return fmt.Sprintf("%-20s %-5d", r.IP, r.Port)
}

func (r *ScanResult) ID() string {
	return fmt.Sprintf("%s:%d", r.IP, r.Port)
}

type Scanner struct {
	dataTimeout time.Duration
	dialer      *net.Dialer
}

// Assert that socks5.Scanner conforms to the scan.Scanner interface
var _ scan.Scanner = (*Scanner)(nil)

type SocksOption func(*Scanner)

func WithDialTimeout(timeout time.Duration) SocksOption {
	return func(s *Scanner) {
		s.dialer.Timeout = timeout
	}
}

func WithDataTimeout(timeout time.Duration) SocksOption {
	return func(s *Scanner) {
		s.dataTimeout = timeout
	}
}

func NewScanner(opts ...SocksOption) *Scanner {
	s := &Scanner{
		dialer: &net.Dialer{
			Timeout: defaultDialTimeout,
		},
		dataTimeout: defaultDataTimeout,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Scanner) Scan(ctx context.Context, r *scan.Request) (result scan.Result, err error) {
	var conn net.Conn
	if conn, err = s.dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", r.DstIP, r.DstPort)); err != nil {
		return
	}
	defer conn.Close()
	// tell the operating system to discard any unsent or unacknowledged data on Close()
	// it will release all socket resources and send RST packet, fine for the scan
	if err = conn.(*net.TCPConn).SetLinger(0); err != nil {
		return
	}

	done := make(chan interface{})
	defer close(done)
	go func() {
		select {
		// return on ctx.Done without waiting read/write timeout
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	sconn := &socksConn{conn: conn, timeout: s.dataTimeout}

	req := NewMethodRequest(SOCKSVersion, MethodNoAuth)
	if _, err = req.WriteTo(sconn); err != nil {
		return
	}

	reply := &MethodReply{}
	if _, err = reply.ReadFrom(sconn); err != nil {
		return
	}

	// TODO also detect auth
	if reply.Ver == SOCKSVersion && reply.Method == MethodNoAuth {
		result = &ScanResult{
			ScanType: ScanType,
			Version:  SOCKSVersion,
			IP:       r.DstIP.String(),
			Port:     r.DstPort,
		}
	}
	return
}

type socksConn struct {
	conn    net.Conn
	timeout time.Duration
}

func (c *socksConn) Read(p []byte) (n int, err error) {
	if err = c.conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return
	}
	return c.conn.Read(p)
}

func (c *socksConn) Write(p []byte) (n int, err error) {
	if err = c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return
	}
	return c.conn.Write(p)
}
