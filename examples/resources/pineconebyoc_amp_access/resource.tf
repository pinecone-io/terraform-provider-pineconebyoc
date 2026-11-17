resource "pineconebyoc_amp_access" "this" {
  cpgw_api_key      = pineconebyoc_environment.this.cpgw_api_key
  workload_role_arn = aws_iam_role.amp_ingest.arn
}

# Lets the ingest role assume the role Pinecone returns.
resource "aws_iam_role_policy" "assume_pinecone_role" {
  name = "amp-assume-pinecone-role"
  role = aws_iam_role.amp_ingest.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sts:AssumeRole"
      Resource = pineconebyoc_amp_access.this.pinecone_role_arn
    }]
  })
}
