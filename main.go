package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/pinecone-io/terraform-provider-pineconebyoc/internal/provider"
)

//go:generate terraform fmt -recursive ./examples/

// --provider-name keeps generation working from a worktree whose directory has another name.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name terraform-provider-pineconebyoc

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "serve with support for debuggers such as delve")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version, provider.Options{}), providerserver.ServeOpts{
		Address: "registry.terraform.io/pinecone-io/pineconebyoc",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
