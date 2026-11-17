package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
)

var _ resource.ResourceWithConfigure = &ampAccessResource{}

type ampAccessResource struct{ c *client.Client }

type ampAccessModel struct {
	ID                     types.String `tfsdk:"id"`
	CpgwAPIKey             types.String `tfsdk:"cpgw_api_key"`
	WorkloadRoleARN        types.String `tfsdk:"workload_role_arn"`
	PineconeRoleARN        types.String `tfsdk:"pinecone_role_arn"`
	AMPRemoteWriteEndpoint types.String `tfsdk:"amp_remote_write_endpoint"`
	AMPRegion              types.String `tfsdk:"amp_region"`
}

const ampAttempts = 5

func (r *ampAccessResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "pineconebyoc_amp_access"
}

func (r *ampAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func (r *ampAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "The `pineconebyoc_amp_access` resource lets the environment's Prometheus write metrics to Pinecone's Amazon Managed Service for Prometheus (AMP).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The workload role the access was granted to."},
			"cpgw_api_key": schema.StringAttribute{
				Required: true, Sensitive: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description:   "The environment's `cpgw_api_key`. Changing it forces a new resource.",
			},
			"workload_role_arn": schema.StringAttribute{
				Required:    true,
				Description: "ARN of the environment's metrics ingest role. Updates in place.",
			},
			"pinecone_role_arn":         schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The role in Pinecone's account the ingest role assumes."},
			"amp_remote_write_endpoint": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "AMP remote-write endpoint."},
			"amp_region":                schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "AMP region."},
		},
	}
}

// grant retries a 5xx up to five times: Pinecone validates a role IAM may not have propagated yet,
// moments after the module created it.
func (r *ampAccessResource) grant(ctx context.Context, m *ampAccessModel) error {
	var err error
	for attempt := 0; attempt < ampAttempts; attempt++ {
		if attempt > 0 {
			if werr := r.c.Backoff(ctx, attempt-1); werr != nil {
				return fmt.Errorf("%w, waiting to retry after %v", werr, err)
			}
		}
		var a client.AMPAccess
		a, err = r.c.CreateAMPAccess(ctx, m.CpgwAPIKey.ValueString(), m.WorkloadRoleARN.ValueString())
		if err == nil {
			m.ID = m.WorkloadRoleARN
			m.PineconeRoleARN = types.StringValue(a.PineconeRoleARN)
			m.AMPRemoteWriteEndpoint = types.StringValue(a.AMPRemoteWriteEndpoint)
			m.AMPRegion = types.StringValue(a.AMPRegion)
			return nil
		}
		if !client.IsServerError(err) {
			return err
		}
	}
	return fmt.Errorf("after %d attempts: %w", ampAttempts, err)
}

func (r *ampAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m ampAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.grant(ctx, &m); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_amp_access: create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *ampAccessResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {}

func (r *ampAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m ampAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.grant(ctx, &m); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_amp_access: update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *ampAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m ampAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteAMPAccess(ctx, m.CpgwAPIKey.ValueString()); err != nil {
		resp.Diagnostics.AddError("pineconebyoc_amp_access: delete", err.Error())
	}
}
