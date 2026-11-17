# Terraform Provider for Pinecone BYOC

The Terraform Provider for Pinecone BYOC registers a Pinecone BYOC environment with Pinecone. The
[BYOC module](https://github.com/pinecone-io/terraform-pinecone-byoc) calls it, and the cloud's
own provider builds the rest of the environment. To install an environment, follow the module's
[install steps](https://github.com/pinecone-io/terraform-pinecone-byoc#install).

Note: We take Terraform's security and our users' trust very seriously. If you believe you have
found a security issue in the Terraform Provider for Pinecone BYOC, please responsibly disclose it
by contacting us.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/install) 1.10 or later, or
  [OpenTofu](https://opentofu.org/docs/intro/install/) 1.11 or later. The BYOC module needs Terraform 1.11 or later
  ([compatibility](https://github.com/pinecone-io/terraform-pinecone-byoc#compatibility)).

## Installing the provider

`terraform init` installs the provider from its `source` address, `pinecone-io/pineconebyoc`. You can
also download a release for your platform from the
[releases](https://github.com/pinecone-io/terraform-provider-pineconebyoc/releases) page.

## Usage

```terraform
terraform {
  required_providers {
    pineconebyoc = {
      source  = "pinecone-io/pineconebyoc"
      version = "~> 0.1.0"
    }
  }
}

provider "pineconebyoc" {}

resource "pineconebyoc_environment" "this" {
  cloud  = "aws"
  region = "us-east-1"
}
```

### Authentication

The provider reads the API key of the Pinecone project that registers the environment from
`PINECONE_API_KEY`, or from `api_key` set to an ephemeral value. See
[Authentication](docs/index.md#authentication).

### Resources

| Resource | Purpose |
| --- | --- |
| [`pineconebyoc_environment`](docs/resources/environment.md) | Registers the environment and mints its credentials |
| [`pineconebyoc_dns_delegation`](docs/resources/dns_delegation.md) | Delegates the environment's subdomain of `pinecone.io` |
| [`pineconebyoc_dns_delegation_validation`](docs/resources/dns_delegation_validation.md) | Waits until the parent zone serves the delegation |
| [`pineconebyoc_amp_access`](docs/resources/amp_access.md) | Lets the environment's Prometheus write to Pinecone's AMP |

## Documentation

The [provider docs](docs/index.md) cover every argument. For BYOC itself, see
[Bring your own cloud](https://docs.pinecone.io/guides/production/bring-your-own-cloud) in the Pinecone docs.

## Examples

See the [examples](examples/) for each resource, and the
[BYOC module](https://github.com/pinecone-io/terraform-pinecone-byoc) for a complete environment.

## Support

Please create an [issue](https://github.com/pinecone-io/terraform-provider-pineconebyoc/issues) for any
support requests.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building the provider, testing local changes, and running the
test suite.

## License

[MPL-2.0](LICENSE).
