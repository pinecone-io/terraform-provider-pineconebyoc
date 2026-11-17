package provider_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/mock"
)

func dnsCalls(m *mock.Server) []mock.Call {
	return slices.DeleteFunc(m.Calls(), func(c mock.Call) bool { return !strings.HasPrefix(c.Route, "dns.") })
}

func waitedAfterPosting(m *mock.Server, nameServers ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		calls := dnsCalls(m)
		if len(calls) != 2 || calls[0].Route != mock.DNSCreate || calls[1].Route != mock.DNSWait || calls[1].Status != 200 {
			return fmt.Errorf("dns calls %v, want a create then a wait that saw it served", calls)
		}
		if got := fmt.Sprint(calls[1].Body["nameservers"]); got != fmt.Sprint(nameServers) {
			return fmt.Errorf("waited for %s, want %v", got, nameServers)
		}
		return nil
	}
}

func onlyWaited(m *mock.Server, nameServers ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		calls := dnsCalls(m)
		if len(calls) != 1 || calls[0].Route != mock.DNSWait || calls[0].Status != 200 {
			return fmt.Errorf("dns calls %v, want only a wait that saw it served", calls)
		}
		if got := m.Delegation("aws-us-east-1-0001"); !slices.Equal(got, nameServers) {
			return fmt.Errorf("delegated to %v, want %v", got, nameServers)
		}
		return nil
	}
}

func validationTimeout(config string) string {
	return strings.Replace(config, `name_servers = pineconebyoc_dns_delegation.this.name_servers`, `name_servers = pineconebyoc_dns_delegation.this.name_servers

  timeouts {
    create = "2s"
  }`, 1)
}

func TestAccDNSDelegationNameServersChangeInPlace(t *testing.T) {
	m, factories := setup(t)
	changed := strings.Replace(environment, `["ns-1.example", "ns-2.example"]`, `["ns-3.example"]`, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(s *terraform.State) error {
			if got := dnsCalls(m); len(got) != 3 || got[2].Route != mock.DNSDelete {
				return fmt.Errorf("dns calls since the update %v, want its create and wait, then a delete", got)
			}
			return noneLive(m)(s)
		},
		Steps: []resource.TestStep{
			{
				Config: withKey(m) + environment,
				Check:  waitedAfterPosting(m, "ns-1.example", "ns-2.example"),
			},
			{
				PreConfig: m.ResetCalls,
				Config:    withKey(m) + changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(envAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(valAddr, plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction(ampAddr, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					allOne(m),
					waitedAfterPosting(m, "ns-3.example"),
					func(*terraform.State) error {
						if got := m.Delegation("aws-us-east-1-0001"); !slices.Equal(got, []string{"ns-3.example"}) {
							return fmt.Errorf("delegated to %v", got)
						}
						return nil
					},
				),
			},
		},
	})
}

// The certificate stand-in forces create_before_destroy onto the delegation, which would then delete what it reposted.
func TestAccDNSDelegationFailedWaitKeepsTheDelegation(t *testing.T) {
	m, factories := setup(t)
	config := withKey(m) + validationTimeout(environment) + `
resource "terraform_data" "certificate" {
  input = pineconebyoc_dns_delegation_validation.this.fqdn

  lifecycle {
    create_before_destroy = true
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { m.HoldDelegations(true) },
				Config:      config,
				ExpectError: regexp.MustCompile(`aws-us-east-1-0001\.byoc\.pinecone\.io is not delegated: context\s+deadline\s+exceeded`),
			},
			{
				PreConfig: func() { m.HoldDelegations(false); m.ResetCalls() },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(valAddr, plancheck.ResourceActionCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(allOne(m), onlyWaited(m, "ns-1.example", "ns-2.example")),
			},
		},
	})
}

func TestAccDNSDelegationValidationWaitsAgainAfterAFailedUpdate(t *testing.T) {
	m, factories := setup(t)
	changed := withKey(m) + validationTimeout(strings.Replace(environment, `["ns-1.example", "ns-2.example"]`, `["ns-3.example"]`, 1))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{Config: withKey(m) + environment},
			{
				PreConfig:   func() { m.HoldDelegations(true) },
				Config:      changed,
				ExpectError: regexp.MustCompile(`is not delegated: context\s+deadline\s+exceeded`),
			},
			{
				PreConfig: func() { m.HoldDelegations(false); m.ResetCalls() },
				Config:    changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(valAddr, plancheck.ResourceActionCreate),
				}},
				Check: onlyWaited(m, "ns-3.example"),
			},
			{
				Config: changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(valAddr, plancheck.ResourceActionNoop),
				}},
			},
		},
	})
}
