package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/mock"
)

func TestAccAMPAccessRoleChangesInPlace(t *testing.T) {
	m, factories := setup(t)
	changed := strings.Replace(environment, `pc-amp-ingest-0001`, `pc-amp-ingest-0002`, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{
			{Config: withKey(m) + environment},
			{
				Config: withKey(m) + changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(envAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(dnsAddr, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(ampAddr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					allOne(m),
					func(*terraform.State) error {
						if role := m.AMPRole("aws-us-east-1-0001.byoc"); !strings.HasSuffix(role, "pc-amp-ingest-0002") {
							return fmt.Errorf("AMP trusts %q", role)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccAMPAccessRetriesWhileIAMPropagates(t *testing.T) {
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             noneLive(m),
		Steps: []resource.TestStep{{
			// Four calls per attempt, so six failures land in the second attempt.
			PreConfig: func() { m.FailTimes(mock.AMPCreate, http.StatusInternalServerError, 6) },
			Config:    withKey(m) + environment,
			Check:     resource.TestCheckResourceAttrSet(ampAddr, "pinecone_role_arn"),
		}},
	})
}

func TestAccAMPAccessStopsOnAClientError(t *testing.T) {
	m, factories := setup(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { m.Fail(mock.AMPCreate, http.StatusBadRequest) },
				Config:      withKey(m) + environment,
				ExpectError: regexp.MustCompile(`amp-access: HTTP 400`),
			},
			{
				PreConfig: func() {
					n := 0
					for _, c := range m.Calls() {
						if c.Route == mock.AMPCreate {
							n++
						}
					}
					if n != 1 {
						t.Errorf("a 4xx is final, have %d AMP calls", n)
					}
					m.Clear()
				},
				Config: withKey(m) + environment,
			},
		},
	})
}
