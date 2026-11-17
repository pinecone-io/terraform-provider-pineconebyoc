package delegation

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	defaultQueryTimeout = 3 * time.Second
	defaultMinInterval  = time.Second
	defaultMaxInterval  = 15 * time.Second
	defaultUnreachable  = 45 * time.Second
	ednsSize            = 1232
)

// Checker asks the parent zone's authoritative servers directly: a recursive resolver caches an
// early NXDOMAIN for the zone's negative TTL, up to 15 minutes on Route 53.
type Checker struct {
	Resolver *net.Resolver
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)

	QueryTimeout time.Duration
	MinInterval  time.Duration
	MaxInterval  time.Duration
	// Unreachable ends the wait early when no server has answered at all, as behind a firewall that allows only a
	// local resolver: waiting longer can't help there.
	Unreachable time.Duration
}

type server struct {
	name      string
	addrs     []string
	udpFailed bool
	answered  bool
	served    bool
	answer    string
}

type query struct {
	id  uint16
	tcp []byte
}

type response struct {
	truncated bool
	rcode     dnsmessage.RCode
	ns        []string
}

var errMismatch = errors.New("reply to another query")

func (c *Checker) Wait(ctx context.Context, fqdn string, nameServers []string) error {
	fqdn = canonical(fqdn)
	want := make([]string, 0, len(nameServers))
	for _, ns := range nameServers {
		want = append(want, canonical(strings.TrimSpace(ns)))
	}
	slices.Sort(want)
	want = slices.Compact(want)

	var (
		zone    string
		servers []*server
		err     error
	)
	interval := cmp.Or(c.MinInterval, defaultMinInterval)
	start := time.Now()
	for {
		if servers == nil {
			z, s, e := c.parentServers(ctx, fqdn)
			if ctx.Err() == nil || err == nil {
				zone, servers, err = z, s, e
			}
		}
		if servers != nil && c.poll(ctx, fqdn, want, servers) {
			return nil
		}
		if servers != nil && !slices.ContainsFunc(servers, func(s *server) bool { return s.answered }) &&
			time.Since(start) >= cmp.Or(c.Unreachable, defaultUnreachable) {
			return unreachable(fqdn, want, zone, servers)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return notDelegated(ctx.Err(), fqdn, want, zone, servers, err)
		case <-t.C:
		}
		interval = min(2*interval, cmp.Or(c.MaxInterval, defaultMaxInterval))
	}
}

// parentServers starts above fqdn: fqdn has NS records of its own once an update moves it.
func (c *Checker) parentServers(ctx context.Context, fqdn string) (string, []*server, error) {
	r := c.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	labels := strings.Split(strings.TrimSuffix(fqdn, "."), ".")
	for i := 1; i < len(labels); i++ {
		zone := strings.Join(labels[i:], ".") + "."
		nss, err := r.LookupNS(ctx, zone)
		var de *net.DNSError
		if len(nss) == 0 && (err == nil || (errors.As(err, &de) && de.IsNotFound)) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("NS %s: %w", zone, err)
		}
		servers := make([]*server, 0, len(nss))
		for _, ns := range nss {
			ips, err := r.LookupNetIP(ctx, "ip", ns.Host)
			if err != nil {
				return "", nil, fmt.Errorf("address of %s: %w", ns.Host, err)
			}
			s := &server{name: canonical(ns.Host)}
			// IPv4 first: a runner whose IPv6 route is broken would spend a query timeout per poll.
			for _, v4 := range []bool{true, false} {
				for _, ip := range ips {
					if ip.Unmap().Is4() == v4 {
						s.addrs = append(s.addrs, net.JoinHostPort(ip.Unmap().String(), "53"))
					}
				}
			}
			servers = append(servers, s)
		}
		slices.SortFunc(servers, func(a, b *server) int { return strings.Compare(a.name, b.name) })
		return zone, servers, nil
	}
	return "", nil, fmt.Errorf("no zone above %s has NS records", fqdn)
}

func (c *Checker) poll(ctx context.Context, fqdn string, want []string, servers []*server) bool {
	var wg sync.WaitGroup
	for _, s := range servers {
		if !s.served {
			wg.Go(func() { c.check(ctx, fqdn, want, s) })
		}
	}
	wg.Wait()
	return !slices.ContainsFunc(servers, func(s *server) bool { return !s.served })
}

func (c *Checker) check(ctx context.Context, fqdn string, want []string, s *server) {
	var errs []string
	for _, addr := range s.addrs {
		r, err := c.query(ctx, fqdn, addr, s)
		if ctx.Err() != nil {
			// Keep the last full answer: one cut short by the deadline says nothing.
			return
		}
		if err == nil {
			s.answered = true
			s.served, s.answer = verdict(r, want)
			return
		}
		errs = append(errs, addr+": "+err.Error())
	}
	s.answer = "no answer: " + strings.Join(errs, "; ")
}

func (c *Checker) query(ctx context.Context, fqdn, addr string, s *server) (response, error) {
	q, err := newQuery(fqdn)
	if err != nil {
		return response{}, err
	}

	var udpErr error
	if !s.udpFailed {
		r, err := c.exchange(ctx, "udp", addr, q, fqdn)
		if err == nil && !r.truncated {
			return r, nil
		}
		if err == nil {
			err = errors.New("truncated")
		}
		udpErr = err
	}
	r, err := c.exchange(ctx, "tcp", addr, q, fqdn)
	if err != nil {
		if udpErr != nil {
			return response{}, fmt.Errorf("udp: %v, tcp: %v", udpErr, err)
		}
		return response{}, fmt.Errorf("tcp: %w", err)
	}
	if udpErr != nil {
		s.udpFailed = true
	}
	return r, nil
}

func newQuery(fqdn string) (query, error) {
	name, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return query{}, err
	}
	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(ednsSize, dnsmessage.RCodeSuccess, false); err != nil {
		return query{}, err
	}
	id := uint16(rand.Uint32())
	b := dnsmessage.NewBuilder(make([]byte, 2, 514), dnsmessage.Header{ID: id})
	if err := b.StartQuestions(); err != nil {
		return query{}, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeNS, Class: dnsmessage.ClassINET}); err != nil {
		return query{}, err
	}
	if err := b.StartAdditionals(); err != nil {
		return query{}, err
	}
	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return query{}, err
	}
	msg, err := b.Finish()
	if err != nil {
		return query{}, err
	}
	binary.BigEndian.PutUint16(msg, uint16(len(msg)-2))
	return query{id: id, tcp: msg}, nil
}

func (c *Checker) exchange(ctx context.Context, network, addr string, q query, fqdn string) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(c.QueryTimeout, defaultQueryTimeout))
	defer cancel()
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, network, addr)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return response{}, err
	}

	if network == "udp" {
		if _, err := conn.Write(q.tcp[2:]); err != nil {
			return response{}, err
		}
		buf := make([]byte, ednsSize)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return response{}, err
			}
			if r, err := parse(buf[:n], q.id, fqdn); !errors.Is(err, errMismatch) {
				return r, err
			}
		}
	}

	if _, err := conn.Write(q.tcp); err != nil {
		return response{}, err
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return response{}, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return response{}, err
	}
	return parse(buf, q.id, fqdn)
}

func parse(msg []byte, id uint16, fqdn string) (response, error) {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		return response{}, err
	}
	if !h.Response || h.ID != id {
		return response{}, errMismatch
	}
	r := response{truncated: h.Truncated, rcode: h.RCode}
	switch q, err := p.Question(); {
	case errors.Is(err, dnsmessage.ErrSectionDone):
		// REFUSED and FORMERR may come without the question.
		return r, nil
	case err != nil:
		return response{}, err
	case q.Type != dnsmessage.TypeNS || canonical(q.Name.String()) != fqdn:
		return response{}, errMismatch
	}
	if r.truncated {
		return r, nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return response{}, err
	}
	sections := []struct {
		header func() (dnsmessage.ResourceHeader, error)
		skip   func() error
	}{{p.AnswerHeader, p.SkipAnswer}, {p.AuthorityHeader, p.SkipAuthority}}
	for _, s := range sections {
		for {
			rh, err := s.header()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			if err != nil {
				return response{}, err
			}
			if rh.Type != dnsmessage.TypeNS || canonical(rh.Name.String()) != fqdn {
				if err := s.skip(); err != nil {
					return response{}, err
				}
				continue
			}
			ns, err := p.NSResource()
			if err != nil {
				return response{}, err
			}
			r.ns = append(r.ns, canonical(ns.NS.String()))
		}
	}
	return r, nil
}

func verdict(r response, want []string) (bool, string) {
	if r.rcode != dnsmessage.RCodeSuccess {
		return false, rcodeName(r.rcode)
	}
	got := slices.Clone(r.ns)
	slices.Sort(got)
	got = slices.Compact(got)
	switch {
	case len(got) == 0:
		return false, "NOERROR with no NS records"
	case !slices.Equal(got, want):
		return false, "NS " + names(got)
	}
	return true, ""
}

func rcodeName(r dnsmessage.RCode) string {
	switch r {
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	}
	return fmt.Sprintf("RCODE%d", r)
}

func notDelegated(cause error, fqdn string, want []string, zone string, servers []*server, err error) error {
	head := fmt.Sprintf("%s is not delegated to %s", strings.TrimSuffix(fqdn, "."), names(want))
	if servers == nil {
		return fmt.Errorf("%s: %w: cannot find its parent zone's name servers: %v", head, cause, err)
	}
	lines := []string{fmt.Sprintf("These servers of %s don't serve it yet:", strings.TrimSuffix(zone, "."))}
	for _, s := range servers {
		if !s.served {
			lines = append(lines, fmt.Sprintf("  %s: %s", strings.TrimSuffix(s.name, "."), cmp.Or(s.answer, "no answer before the deadline")))
		}
	}
	return fmt.Errorf("%s: %w. %s", head, cause, strings.Join(lines, "\n"))
}

func unreachable(fqdn string, want []string, zone string, servers []*server) error {
	lines := []string{fmt.Sprintf(
		"%s is not delegated to %s: no name server of %s answered on port 53. The machine that runs Terraform must reach them over UDP or TCP port 53:",
		strings.TrimSuffix(fqdn, "."), names(want), strings.TrimSuffix(zone, "."),
	)}
	for _, s := range servers {
		lines = append(lines, fmt.Sprintf("  %s: %s", strings.TrimSuffix(s.name, "."), cmp.Or(s.answer, "no answer")))
	}
	return errors.New(strings.Join(lines, "\n"))
}

func names(ns []string) string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = strings.TrimSuffix(n, ".")
	}
	return strings.Join(out, ", ")
}

func canonical(name string) string {
	name = strings.ToLower(name)
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}
