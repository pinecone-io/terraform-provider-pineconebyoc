package provider

import (
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func TestKeysAreSensitiveAtTheSource(t *testing.T) {
	ctx := t.Context()
	want := map[string][]string{
		"pineconebyoc_environment":    {"cpgw_api_key", "service_account_client_secret", "sli_api_key", "datadog_api_key"},
		"pineconebyoc_dns_delegation": {"cpgw_api_key"},
		"pineconebyoc_amp_access":     {"cpgw_api_key"},
	}
	for _, newResource := range New("test", Options{})().Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{}, &meta)
		var s resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &s)
		if d := s.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", meta.TypeName, d)
		}
		for name, attr := range s.Schema.Attributes {
			if attr.IsSensitive() != slices.Contains(want[meta.TypeName], name) {
				t.Errorf("%s.%s sensitive = %v", meta.TypeName, name, attr.IsSensitive())
			}
		}
	}

	var p provider.SchemaResponse
	New("test", Options{})().Schema(ctx, provider.SchemaRequest{}, &p)
	if !p.Schema.Attributes["api_key"].IsSensitive() {
		t.Error("the provider's api_key is not sensitive")
	}
}
