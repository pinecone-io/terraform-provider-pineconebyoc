package provider

import (
	"context"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
)

var (
	_ resource.ResourceWithConfigure  = &environmentResource{}
	_ resource.ResourceWithModifyPlan = &environmentResource{}
)

type environmentResource struct{ c *client.Client }

type environmentModel struct {
	ID                      types.String `tfsdk:"id"`
	Cloud                   types.String `tfsdk:"cloud"`
	Region                  types.String `tfsdk:"region"`
	IsPublicEndpointEnabled types.Bool   `tfsdk:"is_public_endpoint_enabled"`
	Domain                  types.String `tfsdk:"domain"`

	APIURL          types.String `tfsdk:"api_url"`
	GCPProject      types.String `tfsdk:"gcp_project"`
	EnvironmentName types.String `tfsdk:"environment_name"`
	OrgID           types.String `tfsdk:"org_id"`
	OrgName         types.String `tfsdk:"org_name"`
	Subdomain       types.String `tfsdk:"subdomain"`
	EnvironmentFQDN types.String `tfsdk:"environment_fqdn"`

	CpgwAPIKeyID               types.String `tfsdk:"cpgw_api_key_id"`
	CpgwAPIKey                 types.String `tfsdk:"cpgw_api_key"`
	ServiceAccountID           types.String `tfsdk:"service_account_id"`
	ServiceAccountClientID     types.String `tfsdk:"service_account_client_id"`
	ServiceAccountClientSecret types.String `tfsdk:"service_account_client_secret"`
	SLIProjectID               types.String `tfsdk:"sli_project_id"`
	SLIAPIKeyID                types.String `tfsdk:"sli_api_key_id"`
	SLIAPIKey                  types.String `tfsdk:"sli_api_key"`
	DatadogKeyID               types.String `tfsdk:"datadog_key_id"`
	DatadogAPIKey              types.String `tfsdk:"datadog_api_key"`
}

func (m *environmentModel) computed() []*types.String {
	return []*types.String{
		&m.ID, &m.APIURL, &m.GCPProject, &m.EnvironmentName, &m.OrgID, &m.OrgName, &m.Subdomain, &m.EnvironmentFQDN,
		&m.CpgwAPIKeyID, &m.CpgwAPIKey,
		&m.ServiceAccountID, &m.ServiceAccountClientID, &m.ServiceAccountClientSecret,
		&m.SLIProjectID, &m.SLIAPIKeyID, &m.SLIAPIKey,
		&m.DatadogKeyID, &m.DatadogAPIKey,
	}
}

func (m *environmentModel) nullUnknowns() {
	for _, v := range m.computed() {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
}

func setNull(vs ...*types.String) {
	for _, v := range vs {
		*v = types.StringNull()
	}
}

func (m *environmentModel) domain() string {
	if d := m.Domain.ValueString(); d != "" {
		return d
	}
	return client.HostedDomain
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// environmentSlug follows the BYOC naming rule for an environment's service account and SLI key.
func environmentSlug(orgName, envName string) string {
	org := nonAlnum.ReplaceAllString(strings.ToLower(orgName), "")
	if len(org) > 16 {
		org = org[:16]
	}
	first, _, _ := strings.Cut(envName, ".")
	if len(first) > 4 {
		first = first[len(first)-4:]
	}
	return org + "-byoc-" + first
}

func (r *environmentResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "pineconebyoc_environment"
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: desc}
	}
	secret := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "The `pineconebyoc_environment` resource registers a BYOC environment with Pinecone and mints the credentials it runs on. Changing any argument forces a new resource.",
		Attributes: map[string]schema.Attribute{
			"id":     computed("Environment ID."),
			"cloud":  schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Cloud provider: `aws`, `gcp` or `azure`."},
			"region": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Cloud region of the environment."},
			"is_public_endpoint_enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
				Description:   "Whether the environment serves a public endpoint. Defaults to true.",
			},
			"domain": schema.StringAttribute{
				Optional: true, PlanModifiers: replace,
				Description: "Domain the environment is served under. Null for " + client.HostedDomain + ".",
			},

			"api_url":          computed("The provider's `api_url` when the environment was created."),
			"gcp_project":      computed("Pinecone's GCP project the environment reports to."),
			"environment_name": computed("Environment name, such as `aws-us-east-1-ab12.byoc`. Cloud resource names derive from it."),
			"org_id":           computed("Organization the API key belongs to."),
			"org_name":         computed("Organization name at creation. A later rename doesn't reach the environment."),
			"subdomain":        computed("`environment_name` without `.byoc`. The input of `pineconebyoc_dns_delegation`."),
			"environment_fqdn": computed("`<environment_name>.<domain>`."),

			"cpgw_api_key_id":               computed("ID of `cpgw_api_key`."),
			"cpgw_api_key":                  secret("API key the environment calls Pinecone with. It also authorizes `pineconebyoc_dns_delegation` and `pineconebyoc_amp_access`."),
			"service_account_id":            computed("ID of the environment's service account."),
			"service_account_client_id":     computed("Client ID of the environment's service account."),
			"service_account_client_secret": secret("Client secret of the environment's service account."),
			"sli_project_id":                computed("ID of the environment's `__SLI__` project."),
			"sli_api_key_id":                computed("ID of `sli_api_key`."),
			"sli_api_key":                   secret("ProjectEditor API key of the `__SLI__` project, for the environment's availability checks."),
			"datadog_key_id":                computed("ID of the environment's Datadog key."),
			"datadog_api_key":               secret("The environment's Datadog key."),
		},
	}
}

// Create saves whatever it minted before a failure. Terraform then taints the environment, and the
// next apply's Delete revokes those credentials before a fresh Create.
func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.nullUnknowns()
	resp.Diagnostics.Append(r.create(ctx, &m)...)
	if m.ID.IsNull() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *environmentResource) create(ctx context.Context, m *environmentModel) (diags diag.Diagnostics) {
	fail := func(step string, err error) diag.Diagnostics {
		diags.AddError("pineconebyoc_environment: "+step, err.Error())
		return diags
	}

	in := client.CreateEnvironmentRequest{
		Cloud:                   m.Cloud.ValueString(),
		Region:                  m.Region.ValueString(),
		GlobalEnv:               r.c.GlobalEnv,
		IsPublicEndpointEnabled: m.IsPublicEndpointEnabled.ValueBool(),
	}
	if d := m.domain(); d != client.HostedDomain {
		in.Domain = &d
	}
	env, err := r.c.CreateEnvironment(ctx, in)
	if err != nil {
		return fail("create environment", err)
	}
	m.ID = types.StringValue(env.ID)
	m.APIURL = types.StringValue(r.c.APIURL)
	m.GCPProject = types.StringValue(r.c.GCPProject)
	m.EnvironmentName = types.StringValue(env.Name)
	m.OrgID = types.StringValue(env.OrgID)
	m.OrgName = types.StringValue(env.OrgName)
	m.Subdomain = types.StringValue(strings.TrimSuffix(env.Name, ".byoc"))
	m.EnvironmentFQDN = types.StringValue(env.Name + "." + m.domain())
	slug := environmentSlug(env.OrgName, env.Name)

	k, err := r.c.CreateCpgwAPIKey(ctx, env.Name)
	if err != nil {
		return fail("create cpgw api key", err)
	}
	m.CpgwAPIKeyID, m.CpgwAPIKey = types.StringValue(k.ID), types.StringValue(k.Key)

	sa, err := r.c.CreateServiceAccount(ctx, k.Key, slug+"-sa")
	if err != nil {
		return fail("create service account", err)
	}
	m.ServiceAccountID = types.StringValue(sa.ID)
	m.ServiceAccountClientID = types.StringValue(sa.ClientID)
	m.ServiceAccountClientSecret = types.StringValue(sa.ClientSecret)

	jwt, err := r.c.Token(ctx, sa.ClientID, sa.ClientSecret)
	if err != nil {
		return fail("service account token", err)
	}
	project, err := r.c.CreateProject(ctx, jwt, env.OrgID, "__SLI__")
	if err != nil {
		return fail("create SLI project", err)
	}
	m.SLIProjectID = types.StringValue(project)
	key, err := r.c.CreateProjectAPIKey(ctx, jwt, project, slug+"-key")
	if err != nil {
		return fail("create SLI api key", err)
	}
	m.SLIAPIKeyID, m.SLIAPIKey = types.StringValue(key.Key.ID), types.StringValue(key.Value)

	dd, err := r.c.CreateDatadogKey(ctx, k.Key)
	if err != nil {
		return fail("create datadog key", err)
	}
	m.DatadogKeyID, m.DatadogAPIKey = types.StringValue(dd.KeyID), types.StringValue(dd.APIKey)
	return diags
}

// ModifyPlan shows api_url in the plan.
func (r *environmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.c == nil {
		return
	}
	var planned types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("api_url"), &planned)...)
	if planned.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("api_url"), r.c.APIURL)...)
	}
}

// Read keeps the prior state: the control plane has no read route for an environment.
func (r *environmentResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Delete revokes in reverse order, so each credential outlives what it's needed to delete. Progress
// is saved on failure, so a retry never asks a deleted service account for a token.
func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.teardown(ctx, &m); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_environment: delete", err.Error())
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	}
}

func (r *environmentResource) teardown(ctx context.Context, m *environmentModel) error {
	cpgw := m.CpgwAPIKey.ValueString()
	if !m.DatadogKeyID.IsNull() {
		if err := r.c.DeleteDatadogKey(ctx, cpgw, m.DatadogKeyID.ValueString()); err != nil {
			return err
		}
		setNull(&m.DatadogKeyID, &m.DatadogAPIKey)
	}
	if !m.SLIProjectID.IsNull() {
		jwt, err := r.c.Token(ctx, m.ServiceAccountClientID.ValueString(), m.ServiceAccountClientSecret.ValueString())
		if err != nil {
			return err
		}
		if err := r.c.DeleteProject(ctx, jwt, m.SLIProjectID.ValueString()); err != nil {
			return err
		}
		setNull(&m.SLIProjectID, &m.SLIAPIKeyID, &m.SLIAPIKey)
	}
	if !m.ServiceAccountID.IsNull() {
		if err := r.c.DeleteServiceAccount(ctx, cpgw, m.ServiceAccountID.ValueString()); err != nil {
			return err
		}
		setNull(&m.ServiceAccountID, &m.ServiceAccountClientID, &m.ServiceAccountClientSecret)
	}
	if !m.CpgwAPIKeyID.IsNull() {
		if err := r.c.DeleteCpgwAPIKey(ctx, m.CpgwAPIKeyID.ValueString()); err != nil {
			return err
		}
		setNull(&m.CpgwAPIKeyID, &m.CpgwAPIKey)
	}
	return r.c.DeleteEnvironment(ctx, m.ID.ValueString())
}
