package delegation_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/delegation"
)

func TestLivePublicDelegation(t *testing.T) {
	name := strings.TrimSuffix(os.Getenv("PINECONEBYOC_LIVE_DNS"), ".")
	if name == "" {
		t.Skip("set PINECONEBYOC_LIVE_DNS to a delegated public name to query its parent zone's servers")
	}
	nss, err := net.DefaultResolver.LookupNS(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	hosts := make([]string, len(nss))
	for i, ns := range nss {
		hosts[i] = ns.Host
	}

	tcpOnly := func(ctx context.Context, network, address string) (net.Conn, error) {
		if network == "udp" {
			return nil, errors.New("UDP refused")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	for _, tc := range []struct {
		transport string
		checker   delegation.Checker
	}{{"UDP", delegation.Checker{}}, {"TCP", delegation.Checker{Dial: tcpOnly}}} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		start := time.Now()
		err := tc.checker.Wait(ctx, name, hosts)
		cancel()
		if err != nil {
			t.Fatalf("over %s: %v", tc.transport, err)
		}
		t.Logf("%s over %s: every parent server refers it to %s, in %s", name, tc.transport, strings.Join(hosts, ", "), time.Since(start).Round(time.Millisecond))
	}

	_, parent, _ := strings.Cut(name, ".")
	madeUp := fmt.Sprintf("no-such-delegation-%d.%s", time.Now().UnixNano(), parent)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = (&delegation.Checker{}).Wait(ctx, madeUp, hosts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s: got %v, want a deadline", madeUp, err)
	}
	t.Logf("%s: %v", madeUp, err)
}
