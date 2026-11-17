package delegation_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/delegation"
)

const (
	fqdn = "env-1.byoc.example.test"
	zone = "example.test."
)

var want = []string{"NS-B.example.net", "ns-a.example.net."}

type answer int

const (
	serve answer = iota
	nxdomain
	nodata
	stale
	truncate
	dropUDP
)

type auth struct {
	script    []answer
	queries   atomic.Int32
	udp       atomic.Int32
	recursive atomic.Bool
}

func (a *auth) serve(q dnsmessage.Message, overUDP bool) *dnsmessage.Message {
	n := int(a.queries.Add(1)) - 1
	if overUDP {
		a.udp.Add(1)
	}
	if q.RecursionDesired {
		a.recursive.Store(true)
	}
	m := reply(q)
	referral := func(ns ...string) {
		for _, n := range ns {
			m.Authorities = append(m.Authorities, rr(q.Questions[0].Name, 300, &dnsmessage.NSResource{NS: dnsmessage.MustNewName(n)}))
		}
	}
	switch a.script[min(n, len(a.script)-1)] {
	case serve:
		referral("ns-a.example.net.", "ns-b.example.net.")
	case nxdomain:
		m.Authoritative, m.RCode = true, dnsmessage.RCodeNameError
		m.Authorities = []dnsmessage.Resource{soa()}
	case nodata:
		m.Authoritative = true
		m.Authorities = []dnsmessage.Resource{soa()}
	case stale:
		referral("ns-old.example.net.")
	case truncate:
		if overUDP {
			m.Truncated = true
		} else {
			referral("ns-a.example.net.", "ns-b.example.net.")
		}
	case dropUDP:
		if overUDP {
			return nil
		}
		referral("ns-a.example.net.", "ns-b.example.net.")
	}
	return &m
}

func reply(q dnsmessage.Message) dnsmessage.Message {
	return dnsmessage.Message{
		Header:    dnsmessage.Header{ID: q.ID, Response: true, OpCode: q.OpCode, RecursionDesired: q.RecursionDesired},
		Questions: q.Questions[:1],
	}
}

func rr(name dnsmessage.Name, ttl uint32, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: ttl}, Body: body}
}

func soa() dnsmessage.Resource {
	return rr(dnsmessage.MustNewName(zone), 900, &dnsmessage.SOAResource{
		NS:     dnsmessage.MustNewName("ns1." + zone),
		MBox:   dnsmessage.MustNewName("hostmaster." + zone),
		MinTTL: 900,
	})
}

// byoc.example.test is not a zone, so Wait has to walk up a label to find example.test.
func resolver(q dnsmessage.Message, _ bool) *dnsmessage.Message {
	m := reply(q)
	m.RecursionAvailable = true
	name, qtype := q.Questions[0].Name, q.Questions[0].Type
	var i int
	_, nsHost := fmt.Sscanf(name.String(), "ns%d."+zone, &i)
	switch {
	case name.String() == zone && qtype == dnsmessage.TypeNS:
		for i := 3; i >= 1; i-- {
			m.Answers = append(m.Answers, rr(name, 60, &dnsmessage.NSResource{NS: dnsmessage.MustNewName(fmt.Sprintf("ns%d.%s", i, zone))}))
		}
	case nsHost == nil && qtype == dnsmessage.TypeA:
		m.Answers = []dnsmessage.Resource{rr(name, 60, &dnsmessage.AResource{A: [4]byte{192, 0, 2, byte(i)}})}
	case nsHost == nil, name.String() == zone, name.String() == "byoc."+zone:
		m.Authorities = []dnsmessage.Resource{soa()}
	default:
		m.RCode = dnsmessage.RCodeNameError
	}
	return &m
}

type handler func(q dnsmessage.Message, overUDP bool) *dnsmessage.Message

func respond(t *testing.T, h handler, req []byte, overUDP bool) []byte {
	var q dnsmessage.Message
	if err := q.Unpack(req); err != nil || len(q.Questions) == 0 {
		t.Errorf("bad query: %v", err)
		return nil
	}
	m := h(q, overUDP)
	if m == nil {
		return nil
	}
	b, err := m.Pack()
	if err != nil {
		t.Errorf("pack reply: %v", err)
	}
	return b
}

func listen(t *testing.T, h handler) map[string]string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = pc.Close()
		_ = l.Close()
		wg.Wait()
	})
	wg.Go(func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := respond(t, h, buf[:n], true); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}
	})
	wg.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = conn.Close() }()
				for {
					var length [2]byte
					if _, err := io.ReadFull(conn, length[:]); err != nil {
						return
					}
					req := make([]byte, binary.BigEndian.Uint16(length[:]))
					if _, err := io.ReadFull(conn, req); err != nil {
						return
					}
					r := respond(t, h, req, false)
					if r == nil {
						return
					}
					if _, err := conn.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(r))), r...)); err != nil {
						return
					}
				}
			})
		}
	})
	return map[string]string{"udp": pc.LocalAddr().String(), "tcp": l.Addr().String()}
}

// setup refuses any dial but to ns1 to ns3 in process, so no test sends a packet off the machine.
func setup(t *testing.T, scripts [3][]answer, blocked ...string) (*delegation.Checker, []*auth) {
	t.Helper()
	res := listen(t, resolver)
	servers := make([]*auth, 3)
	addrs := map[string]map[string]string{}
	for i, s := range scripts {
		servers[i] = &auth{script: s}
		addrs[fmt.Sprintf("192.0.2.%d:53", i+1)] = listen(t, servers[i].serve)
	}
	d := &net.Dialer{}
	return &delegation.Checker{
		Resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, res[network])
		}},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			to, ok := addrs[address]
			if !ok {
				return nil, fmt.Errorf("test refused %s", address)
			}
			for _, b := range blocked {
				if b == network {
					return nil, errors.New("blocked")
				}
			}
			return d.DialContext(ctx, network, to[network])
		},
		QueryTimeout: 100 * time.Millisecond,
		MinInterval:  5 * time.Millisecond,
		MaxInterval:  20 * time.Millisecond,
	}, servers
}

func TestWaitReturnsOnceEveryServerServesTheDelegation(t *testing.T) {
	cases := map[string]struct {
		scripts [3][]answer
		blocked []string
		queries [3]int32
	}{
		"every server serves it":               {scripts: [3][]answer{{serve}, {serve}, {serve}}, queries: [3]int32{1, 1, 1}},
		"a lagging server catches up":          {scripts: [3][]answer{{serve}, {nxdomain, nxdomain, serve}, {serve}}, queries: [3]int32{1, 3, 1}},
		"NXDOMAIN until the change is in sync": {scripts: [3][]answer{{nxdomain, serve}, {nxdomain, serve}, {nxdomain, serve}}, queries: [3]int32{2, 2, 2}},
		"UDP blocked, TCP answers":             {scripts: [3][]answer{{serve}, {serve}, {serve}}, blocked: []string{"udp"}, queries: [3]int32{1, 1, 1}},
		"UDP truncated, TCP answers":           {scripts: [3][]answer{{truncate}, {truncate}, {truncate}}, queries: [3]int32{2, 2, 2}},
		"UDP dropped, TCP answers":             {scripts: [3][]answer{{dropUDP}, {dropUDP}, {dropUDP}}, queries: [3]int32{2, 2, 2}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, servers := setup(t, tc.scripts, tc.blocked...)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if err := c.Wait(ctx, fqdn, want); err != nil {
				t.Fatal(err)
			}
			for i, s := range servers {
				if n := s.queries.Load(); n != tc.queries[i] {
					t.Errorf("ns%d got %d queries, want %d", i+1, n, tc.queries[i])
				}
				if s.recursive.Load() {
					t.Errorf("ns%d got a recursive query", i+1)
				}
			}
		})
	}
}

func TestWaitStaysOnTCPOnceUDPGoesUnanswered(t *testing.T) {
	c, servers := setup(t, [3][]answer{{serve}, {dropUDP, nxdomain, nxdomain, serve}, {serve}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.Wait(ctx, fqdn, want); err != nil {
		t.Fatal(err)
	}
	if n, udp := servers[1].queries.Load(), servers[1].udp.Load(); n != 4 || udp != 1 {
		t.Errorf("ns2 got %d queries, %d over UDP, want 4 and 1", n, udp)
	}
}

func TestWaitTimeoutNamesWhatEachLaggingServerAnswered(t *testing.T) {
	cases := map[string]struct {
		scripts [3][]answer
		blocked []string
		lines   []string
	}{
		"one server never serves it": {
			scripts: [3][]answer{{serve}, {serve}, {nodata}},
			lines:   []string{"  ns3.example.test: NOERROR with no NS records"},
		},
		"the parent answers NXDOMAIN": {
			scripts: [3][]answer{{nxdomain}, {nxdomain}, {nxdomain}},
			lines:   []string{"  ns1.example.test: NXDOMAIN", "  ns2.example.test: NXDOMAIN", "  ns3.example.test: NXDOMAIN"},
		},
		"a mismatched NS set": {
			scripts: [3][]answer{{stale}, {serve}, {stale}},
			lines:   []string{"  ns1.example.test: NS ns-old.example.net", "  ns3.example.test: NS ns-old.example.net"},
		},
		"UDP and TCP blocked": {
			scripts: [3][]answer{{serve}, {serve}, {serve}},
			blocked: []string{"udp", "tcp"},
			lines: []string{
				"  ns1.example.test: no answer: 192.0.2.1:53: udp: blocked, tcp: blocked",
				"  ns2.example.test: no answer: 192.0.2.2:53: udp: blocked, tcp: blocked",
				"  ns3.example.test: no answer: 192.0.2.3:53: udp: blocked, tcp: blocked",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := setup(t, tc.scripts, tc.blocked...)
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			err := c.Wait(ctx, fqdn, want)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("got %v, want a deadline", err)
			}
			wantErr := "env-1.byoc.example.test is not delegated to ns-a.example.net, ns-b.example.net: context deadline exceeded. " +
				"These servers of example.test don't serve it yet:\n" + strings.Join(tc.lines, "\n")
			if err.Error() != wantErr {
				t.Errorf("got\n%s\nwant\n%s", err, wantErr)
			}
		})
	}
}

func TestWaitTimeoutWithoutAParentZone(t *testing.T) {
	c, _ := setup(t, [3][]answer{{serve}, {serve}, {serve}})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := c.Wait(ctx, "env-1.byoc.unknown.test", want)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline", err)
	}
	re := regexp.MustCompile(`^env-1\.byoc\.unknown\.test is not delegated to .*: cannot find its parent zone's name servers: no zone above env-1\.byoc\.unknown\.test\. has NS records$`)
	if !re.MatchString(err.Error()) {
		t.Errorf("got %q", err)
	}
}

func TestWaitGivesUpEarlyWhenNoServerAnswers(t *testing.T) {
	c, _ := setup(t, [3][]answer{{serve}, {serve}, {serve}}, "udp", "tcp")
	c.Unreachable = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := c.Wait(ctx, fqdn, want)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want an early unreachable error", err)
	}
	wantErr := "env-1.byoc.example.test is not delegated to ns-a.example.net, ns-b.example.net: no name server of example.test answered on port 53. " +
		"The machine that runs Terraform must reach them over UDP or TCP port 53:\n" +
		"  ns1.example.test: no answer: 192.0.2.1:53: udp: blocked, tcp: blocked\n" +
		"  ns2.example.test: no answer: 192.0.2.2:53: udp: blocked, tcp: blocked\n" +
		"  ns3.example.test: no answer: 192.0.2.3:53: udp: blocked, tcp: blocked"
	if err.Error() != wantErr {
		t.Errorf("got\n%s\nwant\n%s", err, wantErr)
	}
}

func TestWaitKeepsWaitingWhileAServerAnswers(t *testing.T) {
	c, _ := setup(t, [3][]answer{{nxdomain}, {nxdomain}, {nxdomain}})
	c.Unreachable = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx, fqdn, want); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the deadline: answering servers mean the delegation may still propagate", err)
	}
}
