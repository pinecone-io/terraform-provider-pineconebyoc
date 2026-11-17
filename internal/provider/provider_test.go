package provider_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/mock"
	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/provider"
)

const (
	envAddr = "pineconebyoc_environment.this"
	dnsAddr = "pineconebyoc_dns_delegation.this"
	valAddr = "pineconebyoc_dns_delegation_validation.this"
	ampAddr = "pineconebyoc_amp_access.this"
)

// hostGuard refuses, rather than forwards, a request bound anywhere but the mock.
type hostGuard struct {
	host    string
	foreign atomic.Int32
}

func (g *hostGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != g.host {
		g.foreign.Add(1)
		return nil, errors.New("test refused a request outside the mock")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func setup(t *testing.T) (*mock.Server, map[string]func() (tfprotov6.ProviderServer, error)) {
	t.Helper()
	m := mock.New()
	guard := &hostGuard{host: m.Listener.Addr().String()}
	t.Cleanup(func() {
		m.Close()
		if n := guard.foreign.Load(); n > 0 {
			t.Errorf("%d requests ignored api_url or auth0_domain", n)
		}
	})
	opts := provider.Options{HTTP: &http.Client{Transport: guard}, RetryBaseDelay: time.Millisecond, WaitDelegated: m.WaitDelegated}
	return m, map[string]func() (tfprotov6.ProviderServer, error){
		"pineconebyoc": providerserver.NewProtocol6WithError(provider.New("test", opts)()),
	}
}

func providerBlock(m *mock.Server, key string) string {
	return fmt.Sprintf("provider \"pineconebyoc\" {\n  api_url      = %q\n  auth0_domain = %q\n  %s\n}\n", m.URL, m.URL, key)
}

func withKey(m *mock.Server) string {
	return providerBlock(m, fmt.Sprintf("api_key = %q", mock.CustomerAPIKey))
}

const environment = `
resource "pineconebyoc_environment" "this" {
  cloud  = "aws"
  region = "us-east-1"
}

resource "pineconebyoc_dns_delegation" "this" {
  cpgw_api_key = pineconebyoc_environment.this.cpgw_api_key
  subdomain    = pineconebyoc_environment.this.subdomain
  name_servers = ["ns-1.example", "ns-2.example"]
}

resource "pineconebyoc_dns_delegation_validation" "this" {
  fqdn         = pineconebyoc_dns_delegation.this.fqdn
  name_servers = pineconebyoc_dns_delegation.this.name_servers
}

resource "pineconebyoc_amp_access" "this" {
  cpgw_api_key      = pineconebyoc_environment.this.cpgw_api_key
  workload_role_arn = "arn:aws:iam::111122223333:role/pc-amp-ingest-0001"
}
`

const envOnly = `
resource "pineconebyoc_environment" "this" {
  cloud  = "aws"
  region = "us-east-1"
}
`

func noneLive(m *mock.Server) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for kind, n := range m.Live() {
			if n != 0 {
				return fmt.Errorf("%d %s left behind", n, kind)
			}
		}
		return nil
	}
}

func live(m *mock.Server, want map[string]int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for kind, n := range m.Live() {
			if n != want[kind] {
				return fmt.Errorf("want %d %s, have %d", want[kind], kind, n)
			}
		}
		return nil
	}
}

func allOne(m *mock.Server) resource.TestCheckFunc {
	return live(m, map[string]int{"environments": 1, "cpgw_api_keys": 1, "service_accounts": 1, "projects": 1, "datadog_keys": 1, "dns_delegations": 1, "amp_access": 1})
}

func deletes(m *mock.Server) []string {
	return slices.DeleteFunc(m.Routes(), func(r string) bool { return !strings.HasSuffix(r, ".delete") })
}

type noCalls struct{ m *mock.Server }

func (c noCalls) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, call := range c.m.Calls() {
		resp.Error = errors.Join(resp.Error, fmt.Errorf("plan called %s %s", call.Method, call.Path))
	}
}

func TestAccProviderAPIKeyFromTheEnvironment(t *testing.T) {
	m, factories := setup(t)
	t.Setenv("PINECONE_API_KEY", mock.CustomerAPIKey)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps:                    []resource.TestStep{{Config: providerBlock(m, "") + envOnly}},
	})
}

func TestAccProviderAPIKeyFromAnEphemeralVariable(t *testing.T) {
	m, factories := setup(t)
	t.Setenv("TF_VAR_pinecone_api_key", mock.CustomerAPIKey)
	config := `
variable "pinecone_api_key" {
  type      = string
  ephemeral = true
  sensitive = true
}
` + providerBlock(m, "api_key = var.pinecone_api_key") + envOnly
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{{
			Config: config,
			Check: func(s *terraform.State) error {
				for addr, rs := range s.RootModule().Resources {
					for k, v := range rs.Primary.Attributes {
						if v == mock.CustomerAPIKey {
							return fmt.Errorf("%s.%s holds the customer's API key", addr, k)
						}
					}
				}
				return nil
			},
		}},
	})
}

func TestAccProviderMissingAPIKeyIsRefused(t *testing.T) {
	m, factories := setup(t)
	t.Setenv("PINECONE_API_KEY", "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(m, "") + envOnly,
			ExpectError: regexp.MustCompile(`Missing Pinecone API key`),
		}},
	})
	if n := len(m.Calls()); n != 0 {
		t.Errorf("%d calls reached the mock", n)
	}
}

func TestAccProviderDeleteCountsAbsentAsDeleted(t *testing.T) {
	m, factories := setup(t)
	config := withKey(m) + environment
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					for _, r := range []string{mock.AMPDelete, mock.DNSDelete, mock.DatadogDelete, mock.ProjectDelete, mock.SADelete, mock.CpgwDelete, mock.EnvDelete} {
						m.Fail(r, http.StatusNotFound)
					}
				},
				Config:  config,
				Destroy: true,
			},
		},
	})
}

func TestAccProviderRefusesCleartextEndpoints(t *testing.T) {
	cases := map[string]struct {
		api, auth0, env, want string
	}{
		"api_url":                {api: "http://api.example.invalid", want: `Insecure api_url`},
		"auth0_domain":           {auth0: "http://login.example.invalid", want: `Insecure auth0_domain`},
		"PINECONE_API_URL":       {env: "http://api.example.invalid", want: `Insecure api_url`},
		"a URL without a scheme": {api: "api.example.invalid", want: `isn't an absolute URL`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, factories := setup(t)
			t.Setenv("PINECONE_API_URL", c.env)
			block := fmt.Sprintf("provider \"pineconebyoc\" {\n  auth0_domain = %q\n  api_key = %q\n", cmp.Or(c.auth0, m.URL), mock.CustomerAPIKey)
			if c.env == "" {
				block += fmt.Sprintf("  api_url = %q\n", cmp.Or(c.api, m.URL))
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config:      block + "}\n" + envOnly,
					ExpectError: regexp.MustCompile(c.want),
				}},
			})
			if n := len(m.Calls()); n != 0 {
				t.Errorf("%d calls reached the mock", n)
			}
		})
	}
}
