resource "pineconebyoc_dns_delegation_validation" "this" {
  fqdn         = pineconebyoc_dns_delegation.this.fqdn
  name_servers = pineconebyoc_dns_delegation.this.name_servers
}

resource "aws_acm_certificate" "environment" {
  domain_name       = "*.${pineconebyoc_dns_delegation_validation.this.fqdn}"
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}
