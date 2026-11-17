package provider

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/delegation"
)

const (
	defaultAPIURL      = "https://api.pinecone.io"
	defaultAuth0Domain = "https://login.pinecone.io"
	defaultGCPProject  = "production-pinecone"
	apiKeyEnv          = "PINECONE_API_KEY"

	globalEnvEnv   = "PINECONE_GLOBAL_ENV"
	apiURLEnv      = "PINECONE_API_URL"
	auth0DomainEnv = "PINECONE_AUTH0_DOMAIN"
	gcpProjectEnv  = "PINECONE_GCP_PROJECT"
)

// Options lets tests refuse any host but the mock, retry without waiting seconds, and stand in for
// public DNS.
type Options struct {
	HTTP           *http.Client
	RetryBaseDelay time.Duration
	WaitDelegated  func(ctx context.Context, fqdn string, nameServers []string) error
}

type pineconeBYOC struct {
	version string
	opts    Options
}

func New(version string, opts Options) func() provider.Provider {
	if opts.RetryBaseDelay == 0 {
		opts.RetryBaseDelay = 2 * time.Second
	}
	if opts.WaitDelegated == nil {
		opts.WaitDelegated = (&delegation.Checker{}).Wait
	}
	return func() provider.Provider { return &pineconeBYOC{version: version, opts: opts} }
}

type providerModel struct {
	APIURL      types.String `tfsdk:"api_url"`
	APIKey      types.String `tfsdk:"api_key"`
	Auth0Domain types.String `tfsdk:"auth0_domain"`
}

func (p *pineconeBYOC) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pineconebyoc"
	resp.Version = p.version
}

func (p *pineconeBYOC) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Registers Pinecone BYOC environments with Pinecone.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				Optional:    true,
				Description: "Pinecone API URL, over https. Defaults to " + defaultAPIURL + ".",
			},
			"auth0_domain": schema.StringAttribute{
				Optional:    true,
				Description: "Auth0 domain that issues service account tokens, over https. Defaults to " + defaultAuth0Domain + ".",
			},
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "API key of the Pinecone project that registers the environment. Defaults to `" + apiKeyEnv + "`. Set it only from an ephemeral value, so it never reaches a saved plan.",
			},
		},
	}
}

func (p *pineconeBYOC) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{"api_url": cfg.APIURL, "api_key": cfg.APIKey, "auth0_domain": cfg.Auth0Domain} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration", name+" must be known when Terraform configures the provider.")
		}
	}
	key := cfg.APIKey.ValueString()
	if key == "" {
		key = os.Getenv(apiKeyEnv)
	}
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Pinecone API key", "Set "+apiKeyEnv+", or api_key from an ephemeral value.")
	}
	apiURL := strings.TrimRight(valueOr(cfg.APIURL, cmp.Or(os.Getenv(apiURLEnv), defaultAPIURL)), "/")
	auth0Domain := strings.TrimRight(valueOr(cfg.Auth0Domain, cmp.Or(os.Getenv(auth0DomainEnv), defaultAuth0Domain)), "/")
	for name, endpoint := range map[string]string{"api_url": apiURL, "auth0_domain": auth0Domain} {
		if err := checkEndpoint(endpoint); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Insecure "+name, err.Error())
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.ResourceData = &client.Client{
		APIURL:         apiURL,
		Auth0Domain:    auth0Domain,
		APIKey:         key,
		GlobalEnv:      cmp.Or(os.Getenv(globalEnvEnv), "prod"),
		GCPProject:     cmp.Or(os.Getenv(gcpProjectEnv), defaultGCPProject),
		UserAgent:      "terraform-provider-pineconebyoc/" + p.version,
		HTTP:           p.opts.HTTP,
		Retries:        3,
		RetryBaseDelay: p.opts.RetryBaseDelay,
	}
}

// checkEndpoint refuses a URL that would carry credentials in cleartext. Plain http stays open to loopback
// hosts, where the acceptance tests' in-process server listens.
func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q isn't an absolute URL", raw)
	}
	if u.Scheme == "https" || u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%q must use https: the provider sends credentials to it", raw)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func valueOr(v types.String, def string) string {
	if v.ValueString() == "" {
		return def
	}
	return v.ValueString()
}

func (p *pineconeBYOC) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return &environmentResource{} },
		func() resource.Resource { return &dnsDelegationResource{} },
		func() resource.Resource { return &dnsDelegationValidationResource{wait: p.opts.WaitDelegated} },
		func() resource.Resource { return &ampAccessResource{} },
	}
}

func (p *pineconeBYOC) DataSources(context.Context) []func() datasource.DataSource { return nil }
