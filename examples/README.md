# Examples

These examples feed the generated docs. Each is a snippet, not a configuration to apply on its own.

The docs tool reads only these paths:

* `provider/provider.tf` for the provider page
* `resources/<resource name>/resource.tf` for that resource's page

`templates/` includes other files by name, such as `provider/ephemeral_api_key.tf`.
