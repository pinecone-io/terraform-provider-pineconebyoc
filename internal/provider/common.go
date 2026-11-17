package provider

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/client"
)

func clientFrom(data any) *client.Client {
	c, _ := data.(*client.Client)
	return c
}

func errorFrom(d diag.Diagnostics) error {
	var errs []error
	for _, e := range d.Errors() {
		errs = append(errs, errors.New(e.Summary()+": "+e.Detail()))
	}
	return errors.Join(errs...)
}
