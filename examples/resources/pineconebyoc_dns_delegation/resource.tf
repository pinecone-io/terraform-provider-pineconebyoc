resource "aws_route53_zone" "environment" {
  name = pineconebyoc_environment.this.environment_fqdn
}

resource "pineconebyoc_dns_delegation" "this" {
  cpgw_api_key = pineconebyoc_environment.this.cpgw_api_key
  subdomain    = pineconebyoc_environment.this.subdomain
  name_servers = aws_route53_zone.environment.name_servers
}
