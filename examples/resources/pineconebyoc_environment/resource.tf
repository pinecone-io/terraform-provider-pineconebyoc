resource "pineconebyoc_environment" "this" {
  cloud  = "aws"
  region = "us-east-1"

  # Defaults, shown for reference.
  is_public_endpoint_enabled = true

  # Set only to serve the environment under your own domain.
  # domain = "example.com"
}
