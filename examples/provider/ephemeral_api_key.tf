ephemeral "aws_secretsmanager_secret_version" "pinecone" {
  secret_id = "pinecone/byoc/api-key"
}

provider "pineconebyoc" {
  api_key = ephemeral.aws_secretsmanager_secret_version.pinecone.secret_string
}
