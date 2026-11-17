package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/mock"
)

func TestAccEnvironmentLifecycle(t *testing.T) {
	m, factories := setup(t)
	config := withKey(m) + environment
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(s *terraform.State) error {
			want := []string{mock.AMPDelete, mock.DNSDelete, mock.DatadogDelete, mock.ProjectDelete, mock.SADelete, mock.CpgwDelete, mock.EnvDelete}
			got := deletes(m)
			// Terraform deletes the two dependents in either order, then the environment in reverse.
			if len(got) != len(want) || !slices.Equal(got[2:], want[2:]) || !slices.Contains(got[:2], mock.AMPDelete) || !slices.Contains(got[:2], mock.DNSDelete) {
				return fmt.Errorf("delete order %v, want %v", got, want)
			}
			return noneLive(m)(s)
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue(envAddr, tfjsonpath.New("api_url"), knownvalue.StringExact(m.URL)),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(envAddr, "api_url", m.URL),
					resource.TestCheckResourceAttr(envAddr, "environment_name", "aws-us-east-1-0001.byoc"),
					resource.TestCheckResourceAttr(envAddr, "subdomain", "aws-us-east-1-0001"),
					resource.TestCheckResourceAttr(envAddr, "environment_fqdn", "aws-us-east-1-0001.byoc.pinecone.io"),
					resource.TestCheckResourceAttr(envAddr, "org_name", "Acme Corp"),
					resource.TestCheckNoResourceAttr(envAddr, "domain"),
					resource.TestCheckResourceAttrSet(envAddr, "cpgw_api_key"),
					resource.TestCheckResourceAttrSet(envAddr, "sli_api_key"),
					resource.TestCheckResourceAttrSet(envAddr, "sli_project_id"),
					resource.TestCheckResourceAttrSet(envAddr, "datadog_api_key"),
					resource.TestCheckResourceAttr(dnsAddr, "fqdn", "aws-us-east-1-0001.byoc.pinecone.io"),
					resource.TestCheckResourceAttr(ampAddr, "pinecone_role_arn", "arn:aws:iam::111122223333:role/amp-writer"),
					resource.TestCheckResourceAttr(ampAddr, "amp_region", "us-east-1"),
					allOne(m),
					func(*terraform.State) error {
						want := []string{mock.EnvCreate, mock.CpgwCreate, mock.SACreate, mock.Token, mock.ProjectCreate, mock.APIKeyCreate, mock.DatadogCreate}
						if got := m.Routes(); !slices.Equal(got[:len(want)], want) {
							return fmt.Errorf("create order %v, want %v first", got, want)
						}
						if got := m.Delegation("aws-us-east-1-0001"); !slices.Equal(got, []string{"ns-1.example", "ns-2.example"}) {
							return fmt.Errorf("delegated to %v", got)
						}
						if role := m.AMPRole("aws-us-east-1-0001.byoc"); !strings.HasSuffix(role, "pc-amp-ingest-0001") {
							return fmt.Errorf("AMP trusts %q", role)
						}
						return nil
					},
				),
			},
			{
				PreConfig: m.ResetCalls,
				Config:    config,
				PlanOnly:  true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
					noCalls{m},
				}},
			},
		},
	})
}

func TestAccEnvironmentReplacementReplacesWhatTakesItsKey(t *testing.T) {
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{Config: withKey(m) + environment},
			{
				Config: withKey(m) + strings.Replace(environment, `region = "us-east-1"`, `region = "us-west-2"`, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(envAddr, plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction(ampAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: allOne(m),
			},
		},
	})
}

func TestAccEnvironmentCustomerDomain(t *testing.T) {
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{{
			Config: withKey(m) + strings.Replace(envOnly, `region = "us-east-1"`, "region = \"us-east-1\"\n  domain = \"example.com\"", 1),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(envAddr, "environment_fqdn", "aws-us-east-1-0001.byoc.example.com"),
				func(*terraform.State) error {
					if body := m.Calls()[0].Body; body["domain"] != "example.com" {
						return fmt.Errorf("environment create sent %v", body)
					}
					return nil
				},
			),
		}},
	})
}

func TestAccEnvironmentPartialCreateIsCleanedUpOnTheNextApply(t *testing.T) {
	m, factories := setup(t)
	config := withKey(m) + envOnly
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { m.Fail(mock.DatadogCreate, http.StatusInternalServerError) },
				Config:      config,
				ExpectError: regexp.MustCompile(`create datadog key`),
			},
			{
				PreConfig: func() {
					if l := m.Live(); l["projects"] != 1 || l["datadog_keys"] != 0 {
						t.Errorf("unexpected progress before the retry: %v", l)
					}
					m.Clear()
					m.ResetCalls()
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					live(m, map[string]int{"environments": 1, "cpgw_api_keys": 1, "service_accounts": 1, "projects": 1, "datadog_keys": 1}),
					func(*terraform.State) error {
						want := []string{mock.ProjectDelete, mock.SADelete, mock.CpgwDelete, mock.EnvDelete}
						if got := deletes(m); !slices.Equal(got, want) {
							return fmt.Errorf("cleanup %v, want %v", got, want)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccEnvironmentFailedDeleteResumesWhereItStopped(t *testing.T) {
	m, factories := setup(t)
	config := withKey(m) + envOnly
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:   func() { m.Fail(mock.CpgwDelete, http.StatusInternalServerError) },
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`cpgw-api-keys`),
			},
			{
				PreConfig: func() { m.Clear(); m.ResetCalls() },
				Config:    config,
				Destroy:   true,
				Check: func(*terraform.State) error {
					if got, want := m.Routes(), []string{mock.CpgwDelete, mock.EnvDelete}; !slices.Equal(got, want) {
						return fmt.Errorf("the retry called %v, want %v", got, want)
					}
					return nil
				},
			},
		},
	})
}

func TestAccEnvironmentRegistersInProdByDefault(t *testing.T) {
	clearControlPlaneEnv(t)
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{{
			Config: withKey(m) + envOnly,
			Check: resource.ComposeAggregateTestCheckFunc(
				sentGlobalEnv(m, "prod"),
				resource.TestCheckResourceAttr(envAddr, "gcp_project", "production-pinecone"),
			),
		}},
	})
}

func TestAccEnvironmentControlPlaneFromTheEnvironment(t *testing.T) {
	m, factories := setup(t)
	pointAtMock := func() {
		t.Setenv("PINECONE_API_URL", m.URL)
		t.Setenv("PINECONE_AUTH0_DOMAIN", m.URL)
	}
	config := fmt.Sprintf("provider \"pineconebyoc\" {\n  api_key = %q\n}\n", mock.CustomerAPIKey) + envOnly
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					pointAtMock()
					t.Setenv("PINECONE_GLOBAL_ENV", "ci")
					t.Setenv("PINECONE_GCP_PROJECT", "example-gcp-project")
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					sentGlobalEnv(m, "ci"),
					resource.TestCheckResourceAttr(envAddr, "api_url", m.URL),
					resource.TestCheckResourceAttr(envAddr, "gcp_project", "example-gcp-project"),
				),
			},
			{
				PreConfig: func() { clearControlPlaneEnv(t); m.ResetCalls() },
				Config:    config,
				PlanOnly:  true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
					noCalls{m},
				}},
			},
			// The destroy after the last step has to reach the mock.
			{PreConfig: pointAtMock, Config: config, PlanOnly: true},
		},
	})
}

func TestAccProviderConfigBeatsTheEnvironment(t *testing.T) {
	t.Setenv("PINECONE_API_URL", "https://api.example.invalid")
	t.Setenv("PINECONE_AUTH0_DOMAIN", "https://login.example.invalid")
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{{
			Config: withKey(m) + envOnly,
			Check:  resource.TestCheckResourceAttr(envAddr, "api_url", m.URL),
		}},
	})
}

func clearControlPlaneEnv(t *testing.T) {
	for _, v := range []string{"PINECONE_GLOBAL_ENV", "PINECONE_API_URL", "PINECONE_AUTH0_DOMAIN", "PINECONE_GCP_PROJECT"} {
		t.Setenv(v, "")
	}
}

func sentGlobalEnv(m *mock.Server, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, c := range m.Calls() {
			if c.Route == mock.EnvCreate {
				if got := c.Body["global_env"]; got != want {
					return fmt.Errorf("environment create sent global_env %v, want %s", got, want)
				}
				return nil
			}
		}
		return fmt.Errorf("no environment create call")
	}
}
