package scan

import (
	"context"
	"errors"
	"io"
	"io/ioutil"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newScanRange(opts ...scanRangeOption) *Range {
	sr := &Range{
		SrcIP:  net.IPv4(192, 168, 0, 3),
		SrcMAC: net.HardwareAddr{0x1, 0x2, 0x3, 0x4, 0x5, 0x6},
		DstSubnet: &net.IPNet{
			IP:   net.IPv4(192, 168, 0, 0),
			Mask: net.CIDRMask(24, 32),
		},
		Ports: []*PortRange{
			{
				StartPort: 22,
				EndPort:   888,
			},
		},
	}
	for _, o := range opts {
		o(sr)
	}
	return sr
}

type scanRangeOption func(sr *Range)

func withPorts(ports []*PortRange) scanRangeOption {
	return func(sr *Range) {
		sr.Ports = ports
	}
}

func withSubnet(subnet *net.IPNet) scanRangeOption {
	return func(sr *Range) {
		sr.DstSubnet = subnet
	}
}

func newScanRequest(opts ...scanRequestOption) *Request {
	r := &Request{
		SrcIP:  net.IPv4(192, 168, 0, 3),
		SrcMAC: net.HardwareAddr{0x1, 0x2, 0x3, 0x4, 0x5, 0x6},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

type scanRequestOption func(sr *Request)

func withDstIP(dstIP net.IP) scanRequestOption {
	return func(sr *Request) {
		sr.DstIP = dstIP
	}
}

func withDstPort(dstPort uint16) scanRequestOption {
	return func(sr *Request) {
		sr.DstPort = dstPort
	}
}

func chanPortToGeneric(in <-chan uint16) <-chan interface{} {
	out := make(chan interface{}, cap(in))
	go func() {
		defer close(out)
		for i := range in {
			out <- i
		}
	}()
	return out
}

func TestPortGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scanRange *Range
		expected  []interface{}
		err       bool
	}{
		{
			name:      "NilPorts",
			scanRange: newScanRange(withPorts(nil)),
			err:       true,
		},
		{
			name: "InvalidPortRange",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 5000,
					EndPort:   2000,
				},
			})),
			err: true,
		},
		{
			name: "InvalidPortRangeAfterValid",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 1000,
					EndPort:   1000,
				},
				{
					StartPort: 7000,
					EndPort:   5000,
				},
			})),
			err: true,
		},
		{
			name: "OnePort",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 22,
					EndPort:   22,
				},
			})),
			expected: []interface{}{uint16(22)},
		},
		{
			name: "TwoPorts",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 22,
					EndPort:   23,
				},
			})),
			expected: []interface{}{uint16(22), uint16(23)},
		},
		{
			name: "ThreePorts",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 25,
					EndPort:   27,
				},
			})),
			expected: []interface{}{uint16(25), uint16(26), uint16(27)},
		},
		{
			name: "OnePortOverflow",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 65535,
					EndPort:   65535,
				},
			})),
			expected: []interface{}{uint16(65535)},
		},
		{
			name: "TwoRangesOnePort",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 25,
					EndPort:   25,
				},
				{
					StartPort: 27,
					EndPort:   27,
				},
			})),
			expected: []interface{}{uint16(25), uint16(27)},
		},
		{
			name: "TwoRangesTwoPorts",
			scanRange: newScanRange(withPorts([]*PortRange{
				{
					StartPort: 21,
					EndPort:   22,
				},
				{
					StartPort: 26,
					EndPort:   27,
				},
			})),
			expected: []interface{}{uint16(21), uint16(22), uint16(26), uint16(27)},
		},
	}

	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)
				portgen := NewPortGenerator()
				ports, err := portgen.Ports(context.Background(), tt.scanRange)
				if tt.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				result := chanToSlice(t, chanPortToGeneric(ports), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func chanIPToGeneric(in <-chan IPGetter) <-chan interface{} {
	out := make(chan interface{}, cap(in))
	go func() {
		defer close(out)
		for i := range in {
			out <- i
		}
	}()
	return out
}

func TestIPGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scanRange *Range
		expected  []interface{}
		err       bool
	}{
		{
			name:      "NilSubnet",
			scanRange: newScanRange(withSubnet(nil)),
			err:       true,
		},
		{
			name: "OneIP",
			scanRange: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(32, 32)}),
			),
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1).To4()),
			},
		},
		{
			name: "TwoIPs",
			scanRange: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(1, 0, 0, 1), Mask: net.CIDRMask(31, 32)}),
			),
			expected: []interface{}{
				wrapIP(net.IPv4(1, 0, 0, 0).To4()),
				wrapIP(net.IPv4(1, 0, 0, 1).To4()),
			},
		},
		{
			name: "FourIPs",
			scanRange: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(10, 0, 0, 1), Mask: net.CIDRMask(30, 32)}),
			),
			expected: []interface{}{
				wrapIP(net.IPv4(10, 0, 0, 0).To4()),
				wrapIP(net.IPv4(10, 0, 0, 1).To4()),
				wrapIP(net.IPv4(10, 0, 0, 2).To4()),
				wrapIP(net.IPv4(10, 0, 0, 3).To4()),
			},
		},
	}

	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)
				ipgen := NewIPGenerator()
				ips, err := ipgen.IPs(context.Background(), tt.scanRange)
				if tt.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				result := chanToSlice(t, chanIPToGeneric(ips), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func chanPairToGeneric(in <-chan *Request) <-chan interface{} {
	out := make(chan interface{}, cap(in))
	go func() {
		defer close(out)
		for i := range in {
			out <- i
		}
	}()
	return out
}

func TestIPPortGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Range
		expected []interface{}
		err      bool
	}{
		{
			name: "InvalidPortRange",
			input: newScanRange(
				withPorts([]*PortRange{
					{
						StartPort: 5000,
						EndPort:   2000,
					},
				}),
			),
			err: true,
		},
		{
			name:  "NilSubnet",
			input: newScanRange(withSubnet(nil)),
			err:   true,
		},
		{
			name: "OneIpOnePort",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(32, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 888,
						EndPort:   888,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(888)),
			},
		},
		{
			name: "OneIpTwoPorts",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(32, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 888,
						EndPort:   889,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(889)),
			},
		},
		{
			name: "TwoIpsOnePort",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(31, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 888,
						EndPort:   888,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(888)),
			},
		},
		{
			name: "FourIpsOnePort",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(30, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 888,
						EndPort:   888,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 2).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 3).To4()), withDstPort(888)),
			},
		},
		{
			name: "TwoIpsTwoPorts",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(31, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 888,
						EndPort:   889,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(888)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4()), withDstPort(889)),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(889)),
			},
		},
		{
			name: "OneIpPortOverflow",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(32, 32)}),
				withPorts([]*PortRange{
					{
						StartPort: 65535,
						EndPort:   65535,
					},
				}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4()), withDstPort(65535)),
			},
		},
	}

	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)

				reqgen := NewIPPortGenerator(NewIPGenerator(), NewPortGenerator())
				pairs, err := reqgen.GenerateRequests(context.Background(), tt.input)
				if tt.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				result := chanToSlice(t, chanPairToGeneric(pairs), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func TestIPRequestGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Range
		expected []interface{}
		err      bool
	}{
		{
			name:  "NilSubnet",
			input: newScanRange(withSubnet(nil)),
			err:   true,
		},
		{
			name: "OneIP",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(32, 32)}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4())),
			},
		},
		{
			name: "TwoIPs",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(31, 32)}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4())),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4())),
			},
		},
		{
			name: "FourIPs",
			input: newScanRange(
				withSubnet(&net.IPNet{IP: net.IPv4(192, 168, 0, 1), Mask: net.CIDRMask(30, 32)}),
			),
			expected: []interface{}{
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 0).To4())),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 1).To4())),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 2).To4())),
				newScanRequest(withDstIP(net.IPv4(192, 168, 0, 3).To4())),
			},
		},
	}

	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)

				reqgen := NewIPRequestGenerator(NewIPGenerator())
				pairs, err := reqgen.GenerateRequests(context.Background(), tt.input)
				if tt.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				result := chanToSlice(t, chanPairToGeneric(pairs), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func TestFileIPPortGeneratorWithInvalidFile(t *testing.T) {
	t.Parallel()

	reqgen := NewFileIPPortGenerator(func() (io.ReadCloser, error) {
		return nil, errors.New("open file error")
	})
	_, err := reqgen.GenerateRequests(context.Background(), &Range{})
	require.Error(t, err)
}

func TestFileIPPortGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []interface{}
	}{
		{
			name:  "OneIPPort",
			input: `{"ip":"192.168.0.1","port":888}`,
			expected: []interface{}{
				&Request{DstIP: net.IPv4(192, 168, 0, 1), DstPort: 888},
			},
		},
		{
			name:  "OneIPPortWithUnknownField",
			input: `{"ip":"192.168.0.1","port":888,"abc":"field"}`,
			expected: []interface{}{
				&Request{DstIP: net.IPv4(192, 168, 0, 1), DstPort: 888},
			},
		},
		{
			name: "TwoIPPorts",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1","port":888}`,
				`{"ip":"192.168.0.2","port":222}`,
			}, "\n"),
			expected: []interface{}{
				&Request{DstIP: net.IPv4(192, 168, 0, 1), DstPort: 888},
				&Request{DstIP: net.IPv4(192, 168, 0, 2), DstPort: 222},
			},
		},
		{
			name:  "InvalidJSON",
			input: `{"ip":"192`,
			expected: []interface{}{
				&Request{Err: ErrJSON},
			},
		},
		{
			name: "InvalidJSONAfterValid",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1","port":888}`,
				`{"ip":"192`,
			}, "\n"),
			expected: []interface{}{
				&Request{DstIP: net.IPv4(192, 168, 0, 1), DstPort: 888},
				&Request{Err: ErrJSON},
			},
		},
		{
			name: "ValidJSONAfterInvalid",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1","port":888}`,
				`{"ip":"192`,
				`{"ip":"192.168.0.3","port":888}`,
			}, "\n"),
			expected: []interface{}{
				&Request{DstIP: net.IPv4(192, 168, 0, 1), DstPort: 888},
				&Request{Err: ErrJSON},
			},
		},
		{
			name:  "InvalidIP",
			input: `{"ip":"192.168.0.1111","port":888}`,
			expected: []interface{}{
				&Request{Err: ErrIP},
			},
		},
		{
			name:  "InvalidPort",
			input: `{"ip":"192.168.0.1","port":88888}`,
			expected: []interface{}{
				&Request{Err: ErrPort},
			},
		},
	}
	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)

				reqgen := NewFileIPPortGenerator(func() (io.ReadCloser, error) {
					return ioutil.NopCloser(strings.NewReader(tt.input)), nil
				})
				pairs, err := reqgen.GenerateRequests(context.Background(), &Range{})
				require.NoError(t, err)
				result := chanToSlice(t, chanPairToGeneric(pairs), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func TestFileIPGeneratorWithInvalidFile(t *testing.T) {
	t.Parallel()

	ipgen := NewFileIPGenerator(func() (io.ReadCloser, error) {
		return nil, errors.New("open file error")
	})
	_, err := ipgen.IPs(context.Background(), &Range{})
	require.Error(t, err)
}

func TestFileIPGenerator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []interface{}
	}{
		{
			name:  "OneIP",
			input: `{"ip":"192.168.0.1"}`,
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1)),
			},
		},
		{
			name:  "OneIPWithUnknownField",
			input: `{"ip":"192.168.0.1","abc":"field"}`,
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1)),
			},
		},
		{
			name: "TwoIPs",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1"}`,
				`{"ip":"192.168.0.2"}`,
			}, "\n"),
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1)),
				wrapIP(net.IPv4(192, 168, 0, 2)),
			},
		},
		{
			name:  "InvalidJSON",
			input: `{"ip":"192`,
			expected: []interface{}{
				&ipError{error: ErrJSON},
			},
		},
		{
			name: "InvalidJSONAfterValid",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1","port":888}`,
				`{"ip":"192`,
			}, "\n"),
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1)),
				&ipError{error: ErrJSON},
			},
		},
		{
			name: "ValidJSONAfterInvalid",
			input: strings.Join([]string{
				`{"ip":"192.168.0.1","port":888}`,
				`{"ip":"192`,
				`{"ip":"192.168.0.3","port":888}`,
			}, "\n"),
			expected: []interface{}{
				wrapIP(net.IPv4(192, 168, 0, 1)),
				&ipError{error: ErrJSON},
			},
		},
		{
			name:  "InvalidIP",
			input: `{"ip":"192.168.0.1111"}`,
			expected: []interface{}{
				&ipError{error: ErrIP},
			},
		},
	}
	for _, vtt := range tests {
		tt := vtt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan interface{})
			go func() {
				defer close(done)

				ipgen := NewFileIPGenerator(func() (io.ReadCloser, error) {
					return ioutil.NopCloser(strings.NewReader(tt.input)), nil
				})
				ips, err := ipgen.IPs(context.Background(), &Range{})
				require.NoError(t, err)
				result := chanToSlice(t, chanIPToGeneric(ips), len(tt.expected))
				require.Equal(t, tt.expected, result)
			}()
			waitDone(t, done)
		})
	}
}

func TestLiveRequestGeneratorContextExit(t *testing.T) {
	t.Parallel()

	reqgen := NewIPPortGenerator(NewIPGenerator(), NewPortGenerator())
	rg := NewLiveRequestGenerator(reqgen, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requests, err := rg.GenerateRequests(ctx, newScanRange())
	require.NoError(t, err)
	// consume all requests
loop:
	for {
		select {
		case _, ok := <-requests:
			if !ok {
				break loop
			}
		case <-time.After(waitTimeout):
			require.Fail(t, "test timeout")
		}
	}
}
