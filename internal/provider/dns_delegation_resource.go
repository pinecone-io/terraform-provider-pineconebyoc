package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
)

var _ resource.ResourceWithConfigure = &dnsDelegationResource{}

type dnsDelegationResource struct {
	c *client.Client
}

type dnsDelegationModel struct {
	ID          types.String `tfsdk:"id"`
	CpgwAPIKey  types.String `tfsdk:"cpgw_api_key"`
	Subdomain   types.String `tfsdk:"subdomain"`
	NameServers types.List   `tfsdk:"name_servers"`
	FQDN        types.String `tfsdk:"fqdn"`
	ChangeID    types.String `tfsdk:"change_id"`
}

func (r *dnsDelegationResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "pineconebyoc_dns_delegation"
}

func (r *dnsDelegationResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func (r *dnsDelegationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "The `pineconebyoc_dns_delegation` resource has Pinecone delegate the environment's subdomain of " + client.HostedDomain + " to the environment's DNS zone. Use it only for environments on Pinecone's domain.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The delegated name."},
			"cpgw_api_key": schema.StringAttribute{
				Required: true, Sensitive: true, PlanModifiers: replace,
				Description: "The environment's `cpgw_api_key`. Changing it forces a new resource.",
			},
			"subdomain": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				Description: "The environment's `subdomain`. Changing it forces a new resource.",
			},
			"name_servers": schema.ListAttribute{
				Required: true, ElementType: types.StringType,
				Description: "Name servers of the environment's DNS zone. Updates in place.",
			},
			"fqdn":      schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The delegated name, `<subdomain>.byoc.pinecone.io`, without a trailing dot."},
			"change_id": schema.StringAttribute{Computed: true, Description: "Change ID Pinecone returned for the last delegation."},
		},
	}
}

func (r *dnsDelegationResource) post(ctx context.Context, m *dnsDelegationModel) error {
	var ns []string
	if d := m.NameServers.ElementsAs(ctx, &ns, false); d.HasError() {
		return errorFrom(d)
	}
	out, err := r.c.CreateDNSDelegation(ctx, m.CpgwAPIKey.ValueString(), m.Subdomain.ValueString(), ns)
	if err != nil {
		return err
	}
	m.ID, m.FQDN, m.ChangeID = types.StringValue(out.FQDN), types.StringValue(out.FQDN), types.StringValue(out.ChangeID)
	return nil
}

func (r *dnsDelegationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m dnsDelegationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.post(ctx, &m); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_dns_delegation: create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsDelegationResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {}

func (r *dnsDelegationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m dnsDelegationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.post(ctx, &m); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_dns_delegation: update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsDelegationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m dnsDelegationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var ns []string
	resp.Diagnostics.Append(m.NameServers.ElementsAs(ctx, &ns, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteDNSDelegation(ctx, m.CpgwAPIKey.ValueString(), m.Subdomain.ValueString(), ns); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_dns_delegation: delete", err.Error())
	}
}
