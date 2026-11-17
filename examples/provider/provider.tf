terraform {
  required_providers {
    pineconebyoc = {
      source  = "pinecone-io/pineconebyoc"
      version = "~> 0.1.0"
    }
  }
}

# Reads the API key from PINECONE_API_KEY.
provider "pineconebyoc" {}
