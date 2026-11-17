package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/mock"
)

func newClient(m *mock.Server) *client.Client {
	return &client.Client{
		APIURL:         m.URL,
		Auth0Domain:    m.URL,
		APIKey:         mock.CustomerAPIKey,
		UserAgent:      "terraform-provider-pineconebyoc/test",
		Retries:        3,
		RetryBaseDelay: time.Millisecond,
	}
}

func count(m *mock.Server, route string) int {
	n := 0
	for _, c := range m.Calls() {
		if c.Route == route {
			n++
		}
	}
	return n
}

func TestServerErrorsAreRetried(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	m.FailTimes(mock.EnvCreate, http.StatusBadGateway, 3)
	if _, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1", GlobalEnv: "prod"}); err != nil {
		t.Fatalf("three 5xx then success should pass after three retries: %v", err)
	}
	if n := count(m, mock.EnvCreate); n != 4 {
		t.Errorf("want 4 calls, have %d", n)
	}
}

func TestRetriesStopAtThree(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	m.Fail(mock.EnvCreate, http.StatusInternalServerError)
	_, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"})
	if !client.IsServerError(err) {
		t.Fatalf("want a server error, have %v", err)
	}
	if n := count(m, mock.EnvCreate); n != 4 {
		t.Errorf("want 1 call and 3 retries, have %d calls", n)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	m.Fail(mock.EnvCreate, http.StatusForbidden)
	if _, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"}); err == nil {
		t.Fatal("want an error")
	}
	if n := count(m, mock.EnvCreate); n != 1 {
		t.Errorf("a 4xx is final, have %d calls", n)
	}
}

func TestTokenIsNeverRetriedOrEchoed(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	m.Fail(mock.Token, http.StatusServiceUnavailable)
	_, err := c.Token(t.Context(), "client-id", "fake-secret-0001")
	if err == nil || strings.Contains(err.Error(), "fake-secret-0001") || strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("token error must name only the route and status, have %v", err)
	}
	if n := count(m, mock.Token); n != 1 {
		t.Errorf("the token call is not retried, have %d calls", n)
	}
}

func TestErrorsKeepOnlyTheServerMessage(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	m.Fail(mock.EnvCreate, http.StatusBadRequest)
	_, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"})
	if err == nil || !strings.Contains(err.Error(), "injected fault") || strings.Contains(err.Error(), mock.CustomerAPIKey) || strings.Contains(err.Error(), "request") {
		t.Fatalf("want the message field only, have %v", err)
	}
}

func TestEveryCallSendsTheUserAgent(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	if _, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range m.Calls() {
		if call.UserAgent != "terraform-provider-pineconebyoc/test" {
			t.Errorf("%s sent User-Agent %q", call.Route, call.UserAgent)
		}
	}
}

func TestDomainTheControlPlaneDroppedIsRefused(t *testing.T) {
	m := mock.New()
	defer m.Close()
	m.DropDomain = true
	c := newClient(m)
	d := "example.com"
	_, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1", Domain: &d})
	if err == nil || !strings.Contains(err.Error(), "recorded domain") {
		t.Fatalf("want a refusal, have %v", err)
	}
}

func TestDelegationFQDNHasNoTrailingDot(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	env, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := c.CreateCpgwAPIKey(t.Context(), env.Name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.CreateDNSDelegation(t.Context(), k.Key, strings.TrimSuffix(env.Name, ".byoc"), []string{"ns-1.example"})
	if err != nil {
		t.Fatal(err)
	}
	if d.FQDN != env.Name+".pinecone.io" {
		t.Errorf("delegated %q, want %q", d.FQDN, env.Name+".pinecone.io")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer other.Close()
	elsewhere := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	c := &client.Client{APIURL: api.URL, APIKey: mock.CustomerAPIKey, Retries: 3, RetryBaseDelay: time.Millisecond}
	_, err := c.CreateEnvironment(t.Context(), client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect {
		t.Fatalf("want the 307 as an error, have %v", err)
	}
	if n := reached.Load(); n > 0 {
		t.Errorf("the redirect target got %d requests", n)
	}
}

func TestInterruptedRetryReportsTheInterruption(t *testing.T) {
	m := mock.New()
	defer m.Close()
	c := newClient(m)
	c.RetryBaseDelay = time.Hour
	m.Fail(mock.EnvCreate, http.StatusInternalServerError)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := c.CreateEnvironment(ctx, client.CreateEnvironmentRequest{Cloud: "aws", Region: "us-east-1"})
	if !errors.Is(err, context.DeadlineExceeded) || client.IsServerError(err) || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("want the deadline first and the last 5xx as context, have %v", err)
	}
}
