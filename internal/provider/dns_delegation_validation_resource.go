package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const delegationTimeout = 10 * time.Minute

type dnsDelegationValidationResource struct {
	wait func(ctx context.Context, fqdn string, nameServers []string) error
}

type dnsDelegationValidationModel struct {
	ID          types.String   `tfsdk:"id"`
	FQDN        types.String   `tfsdk:"fqdn"`
	NameServers types.List     `tfsdk:"name_servers"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (r *dnsDelegationValidationResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "pineconebyoc_dns_delegation_validation"
}

func (r *dnsDelegationValidationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The `pineconebyoc_dns_delegation_validation` resource waits until the parent zone serves a `pineconebyoc_dns_delegation`. It creates nothing.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "The delegated name."},
			"fqdn": schema.StringAttribute{
				Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description: "The delegation's `fqdn`. Changing it forces a new resource.",
			},
			"name_servers": schema.ListAttribute{
				Required: true, ElementType: types.StringType, PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				Description: "The delegation's `name_servers`. Changing them forces a new resource, which waits for the new ones.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create:            true,
				CreateDescription: "How long the wait may take. A duration such as `20m`. Defaults to `10m`.",
			}),
		},
	}
}

func (r *dnsDelegationValidationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m dnsDelegationValidationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := m.Timeouts.Create(ctx, delegationTimeout)
	resp.Diagnostics.Append(d...)
	var ns []string
	resp.Diagnostics.Append(m.NameServers.ElementsAs(ctx, &ns, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.wait(ctx, m.FQDN.ValueString(), ns); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_dns_delegation_validation: create", err.Error())
		return
	}
	m.ID = m.FQDN
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsDelegationValidationResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {
}

func (r *dnsDelegationValidationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m dnsDelegationValidationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsDelegationValidationResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
