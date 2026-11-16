# Neon API Reference

Base URL: https://console.neon.tech/api/v2
Auth: `Authorization: Bearer $NEON_API_KEY`

Interface-specific indexes:

- [API endpoint index](https://neon.com/docs/reference/api.md): all endpoints grouped by resource, each with curl and SDK examples
- [Neon CLI](https://neon.com/docs/cli.md): Neon CLI commands, options, and usage
- [OpenAPI spec](https://neon.com/api_spec/release/v2.json): machine-readable schemas for request/response validation and codegen

## AI Gateway

[All ai-gateway endpoints](https://neon.com/docs/reference/api/ai-gateway.md)

- [Get branch AI Gateway endpoint](https://neon.com/docs/reference/api/ai-gateway/get-project-branch-ai-gateway.md) `GET /projects/{project_id}/branches/{branch_id}/ai_gateway`

## API Keys

[All api-keys endpoints](https://neon.com/docs/reference/api/api-keys.md)

- [Create API key](https://neon.com/docs/reference/api/api-keys/create-api-key.md) `POST /api_keys`
- [List API keys](https://neon.com/docs/reference/api/api-keys/list-api-keys.md) `GET /api_keys`
- [Revoke API key](https://neon.com/docs/reference/api/api-keys/revoke-api-key.md) `DELETE /api_keys/{key_id}`

## Authentication

[All auth endpoints](https://neon.com/docs/reference/api/auth.md)

- [Add an OAuth provider](https://neon.com/docs/reference/api/auth/add-branch-neon-auth-oauth-provider.md) `POST /projects/{project_id}/branches/{branch_id}/auth/oauth_providers`
- [Add domain to redirect_uri whitelist](https://neon.com/docs/reference/api/auth/add-branch-neon-auth-trusted-domain.md) `POST /projects/{project_id}/branches/{branch_id}/auth/domains`
- [Create new auth user](https://neon.com/docs/reference/api/auth/create-branch-neon-auth-new-user.md) `POST /projects/{project_id}/branches/{branch_id}/auth/users`
- [Delete auth user](https://neon.com/docs/reference/api/auth/delete-branch-neon-auth-user.md) `DELETE /projects/{project_id}/branches/{branch_id}/auth/users/{auth_user_id}`
- [Delete domain from redirect_uri whitelist](https://neon.com/docs/reference/api/auth/delete-branch-neon-auth-trusted-domain.md) `DELETE /projects/{project_id}/branches/{branch_id}/auth/domains`
- [Delete OAuth provider](https://neon.com/docs/reference/api/auth/delete-branch-neon-auth-oauth-provider.md) `DELETE /projects/{project_id}/branches/{branch_id}/auth/oauth_providers/{oauth_provider_id}`
- [Disable Neon Auth for the branch](https://neon.com/docs/reference/api/auth/disable-neon-auth.md) `DELETE /projects/{project_id}/branches/{branch_id}/auth`
- [Enable Neon Auth for the branch](https://neon.com/docs/reference/api/auth/create-neon-auth.md) `POST /projects/{project_id}/branches/{branch_id}/auth`
- [List domains in redirect_uri whitelist](https://neon.com/docs/reference/api/auth/list-branch-neon-auth-trusted-domains.md) `GET /projects/{project_id}/branches/{branch_id}/auth/domains`
- [List OAuth providers for the branch](https://neon.com/docs/reference/api/auth/list-branch-neon-auth-oauth-providers.md) `GET /projects/{project_id}/branches/{branch_id}/auth/oauth_providers`
- [Retrieve email and password configuration](https://neon.com/docs/reference/api/auth/get-neon-auth-email-and-password-config.md) `GET /projects/{project_id}/branches/{branch_id}/auth/email_and_password`
- [Retrieve email provider configuration](https://neon.com/docs/reference/api/auth/get-neon-auth-email-provider.md) `GET /projects/{project_id}/branches/{branch_id}/auth/email_provider`
- [Retrieve localhost allow setting](https://neon.com/docs/reference/api/auth/get-neon-auth-allow-localhost.md) `GET /projects/{project_id}/branches/{branch_id}/auth/allow_localhost`
- [Retrieve Neon Auth details for the branch](https://neon.com/docs/reference/api/auth/get-neon-auth.md) `GET /projects/{project_id}/branches/{branch_id}/auth`
- [Retrieve Neon Auth plugin configurations](https://neon.com/docs/reference/api/auth/get-neon-auth-plugin-configs.md) `GET /projects/{project_id}/branches/{branch_id}/auth/plugins`
- [Retrieve Neon Auth webhook configuration](https://neon.com/docs/reference/api/auth/get-neon-auth-webhook-config.md) `GET /projects/{project_id}/branches/{branch_id}/auth/webhooks`
- [Retrieve phone number plugin configuration](https://neon.com/docs/reference/api/auth/get-neon-auth-phone-number-plugin.md) `GET /projects/{project_id}/branches/{branch_id}/auth/plugins/phone-number`
- [Send test email using the saved email provider](https://neon.com/docs/reference/api/auth/send-neon-auth-email-provider-test.md) `POST /projects/{project_id}/branches/{branch_id}/auth/email_provider/test`
- [Update auth configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-config.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/config`
- [Update auth user role](https://neon.com/docs/reference/api/auth/update-neon-auth-user-role.md) `PUT /projects/{project_id}/branches/{branch_id}/auth/users/{auth_user_id}/role`
- [Update email and password configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-email-and-password-config.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/email_and_password`
- [Update email provider configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-email-provider.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/email_provider`
- [Update localhost allow setting](https://neon.com/docs/reference/api/auth/update-neon-auth-allow-localhost.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/allow_localhost`
- [Update magic link plugin configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-magic-link-plugin.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/plugins/magic-link`
- [Update Neon Auth webhook configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-webhook-config.md) `PUT /projects/{project_id}/branches/{branch_id}/auth/webhooks`
- [Update OAuth provider](https://neon.com/docs/reference/api/auth/update-branch-neon-auth-oauth-provider.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/oauth_providers/{oauth_provider_id}`
- [Update organization plugin configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-organization-plugin.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/plugins/organization`
- [Update phone number plugin configuration](https://neon.com/docs/reference/api/auth/update-neon-auth-phone-number-plugin.md) `PATCH /projects/{project_id}/branches/{branch_id}/auth/plugins/phone-number`
- [Send test email](https://neon.com/docs/reference/api/auth/send-neon-auth-test-email.md) `POST /projects/{project_id}/branches/{branch_id}/auth/send_test_email`

## Branches

[All branches endpoints](https://neon.com/docs/reference/api/branches.md)

- [Compare database schema](https://neon.com/docs/reference/api/branches/get-project-branch-schema-comparison.md) `GET /projects/{project_id}/branches/{branch_id}/compare_schema`
- [Create anonymized branch](https://neon.com/docs/reference/api/branches/create-project-branch-anonymized.md) `POST /projects/{project_id}/branch_anonymized`
- [Create branch](https://neon.com/docs/reference/api/branches/create-project-branch.md) `POST /projects/{project_id}/branches`
- [Create database](https://neon.com/docs/reference/api/branches/create-project-branch-database.md) `POST /projects/{project_id}/branches/{branch_id}/databases`
- [Create role](https://neon.com/docs/reference/api/branches/create-project-branch-role.md) `POST /projects/{project_id}/branches/{branch_id}/roles`
- [Delete branch](https://neon.com/docs/reference/api/branches/delete-project-branch.md) `DELETE /projects/{project_id}/branches/{branch_id}`
- [Delete database](https://neon.com/docs/reference/api/branches/delete-project-branch-database.md) `DELETE /projects/{project_id}/branches/{branch_id}/databases/{database_name}`
- [Delete role](https://neon.com/docs/reference/api/branches/delete-project-branch-role.md) `DELETE /projects/{project_id}/branches/{branch_id}/roles/{role_name}`
- [Finalize branch restore from snapshot](https://neon.com/docs/reference/api/branches/finalize-restore-branch.md) `POST /projects/{project_id}/branches/{branch_id}/finalize_restore`
- [List branch endpoints](https://neon.com/docs/reference/api/branches/list-project-branch-endpoints.md) `GET /projects/{project_id}/branches/{branch_id}/endpoints`
- [List branches](https://neon.com/docs/reference/api/branches/list-project-branches.md) `GET /projects/{project_id}/branches`
- [List databases](https://neon.com/docs/reference/api/branches/list-project-branch-databases.md) `GET /projects/{project_id}/branches/{branch_id}/databases`
- [List roles](https://neon.com/docs/reference/api/branches/list-project-branch-roles.md) `GET /projects/{project_id}/branches/{branch_id}/roles`
- [Reset role password](https://neon.com/docs/reference/api/branches/reset-project-branch-role-password.md) `POST /projects/{project_id}/branches/{branch_id}/roles/{role_name}/reset_password`
- [Restore branch to a historical state](https://neon.com/docs/reference/api/branches/restore-project-branch.md) `POST /projects/{project_id}/branches/{branch_id}/restore`
- [Retrieve anonymized branch status](https://neon.com/docs/reference/api/branches/get-anonymized-branch-status.md) `GET /projects/{project_id}/branches/{branch_id}/anonymized_status`
- [Retrieve branch details](https://neon.com/docs/reference/api/branches/get-project-branch.md) `GET /projects/{project_id}/branches/{branch_id}`
- [Retrieve database details](https://neon.com/docs/reference/api/branches/get-project-branch-database.md) `GET /projects/{project_id}/branches/{branch_id}/databases/{database_name}`
- [Retrieve database schema](https://neon.com/docs/reference/api/branches/get-project-branch-schema.md) `GET /projects/{project_id}/branches/{branch_id}/schema`
- [Retrieve masking rules](https://neon.com/docs/reference/api/branches/get-masking-rules.md) `GET /projects/{project_id}/branches/{branch_id}/masking_rules`
- [Retrieve number of branches](https://neon.com/docs/reference/api/branches/count-project-branches.md) `GET /projects/{project_id}/branches/count`
- [Retrieve role details](https://neon.com/docs/reference/api/branches/get-project-branch-role.md) `GET /projects/{project_id}/branches/{branch_id}/roles/{role_name}`
- [Retrieve role password](https://neon.com/docs/reference/api/branches/get-project-branch-role-password.md) `GET /projects/{project_id}/branches/{branch_id}/roles/{role_name}/reveal_password`
- [Set branch as default](https://neon.com/docs/reference/api/branches/set-default-project-branch.md) `POST /projects/{project_id}/branches/{branch_id}/set_as_default`
- [Start anonymization](https://neon.com/docs/reference/api/branches/start-anonymization.md) `POST /projects/{project_id}/branches/{branch_id}/anonymize`
- [Update branch](https://neon.com/docs/reference/api/branches/update-project-branch.md) `PATCH /projects/{project_id}/branches/{branch_id}`
- [Update database](https://neon.com/docs/reference/api/branches/update-project-branch-database.md) `PATCH /projects/{project_id}/branches/{branch_id}/databases/{database_name}`
- [Update masking rules](https://neon.com/docs/reference/api/branches/update-masking-rules.md) `PATCH /projects/{project_id}/branches/{branch_id}/masking_rules`

## Buckets

[All buckets endpoints](https://neon.com/docs/reference/api/buckets.md)

- [Create a bucket on the branch](https://neon.com/docs/reference/api/buckets/create-project-branch-bucket.md) `POST /projects/{project_id}/branches/{branch_id}/buckets`
- [Delete a bucket on the branch](https://neon.com/docs/reference/api/buckets/delete-project-branch-bucket.md) `DELETE /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}`
- [Delete an object in a bucket](https://neon.com/docs/reference/api/buckets/delete-project-branch-bucket-object.md) `DELETE /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}/objects/{object_key}`
- [Delete every object under a key prefix (folder) in a bucket](https://neon.com/docs/reference/api/buckets/delete-project-branch-bucket-objects-by-prefix.md) `DELETE /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}/objects-by-prefix`
- [Download an object&#39;s bytes](https://neon.com/docs/reference/api/buckets/get-project-branch-bucket-object.md) `GET /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}/objects/{object_key}/download`
- [List buckets on the branch](https://neon.com/docs/reference/api/buckets/list-project-branch-buckets.md) `GET /projects/{project_id}/branches/{branch_id}/buckets`
- [List objects in a bucket](https://neon.com/docs/reference/api/buckets/list-project-branch-bucket-objects.md) `GET /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}/objects`
- [Presign an upload or download for an object in a bucket](https://neon.com/docs/reference/api/buckets/presign-project-branch-bucket-object.md) `POST /projects/{project_id}/branches/{branch_id}/buckets/{bucket_name}/objects/{object_key}/presign`

## Consumption

[All consumption endpoints](https://neon.com/docs/reference/api/consumption.md)

- [Retrieve branch consumption metrics](https://neon.com/docs/reference/api/consumption/get-consumption-history-per-branch-v2.md) `GET /consumption_history/v2/branches`
- [Retrieve project consumption metrics](https://neon.com/docs/reference/api/consumption/get-consumption-history-per-project-v2.md) `GET /consumption_history/v2/projects`
- [Retrieve project consumption metrics (legacy plans)](https://neon.com/docs/reference/api/consumption/get-consumption-history-per-project.md) `GET /consumption_history/projects`

## Credentials

[All credentials endpoints](https://neon.com/docs/reference/api/credentials.md)

- [Issue a scoped credential on the branch](https://neon.com/docs/reference/api/credentials/create-credential.md) `POST /projects/{project_id}/branches/{branch_id}/credentials`
- [List credentials on the branch](https://neon.com/docs/reference/api/credentials/list-credentials.md) `GET /projects/{project_id}/branches/{branch_id}/credentials`
- [Reveal a credential&#39;s secrets](https://neon.com/docs/reference/api/credentials/reveal-credential.md) `POST /projects/{project_id}/branches/{branch_id}/credentials/{token_id}/reveal`
- [Revoke a credential](https://neon.com/docs/reference/api/credentials/revoke-credential.md) `DELETE /projects/{project_id}/branches/{branch_id}/credentials/{token_id}`
- [Rotate a credential&#39;s secrets](https://neon.com/docs/reference/api/credentials/rotate-credential.md) `POST /projects/{project_id}/branches/{branch_id}/credentials/{token_id}/rotate`

## Data API

[All dataapi endpoints](https://neon.com/docs/reference/api/dataapi.md)

- [Create Neon Data API](https://neon.com/docs/reference/api/dataapi/create-project-branch-data-api.md) `POST /projects/{project_id}/branches/{branch_id}/data-api/{database_name}`
- [Delete Neon Data API](https://neon.com/docs/reference/api/dataapi/delete-project-branch-data-api.md) `DELETE /projects/{project_id}/branches/{branch_id}/data-api/{database_name}`
- [Get advisor issues](https://neon.com/docs/reference/api/dataapi/get-project-advisor-security-issues.md) `GET /projects/{project_id}/advisors`
- [Retrieve Neon Data API configuration](https://neon.com/docs/reference/api/dataapi/get-project-branch-data-api.md) `GET /projects/{project_id}/branches/{branch_id}/data-api/{database_name}`
- [Update Neon Data API](https://neon.com/docs/reference/api/dataapi/update-project-branch-data-api.md) `PATCH /projects/{project_id}/branches/{branch_id}/data-api/{database_name}`

## Endpoints

[All endpoints endpoints](https://neon.com/docs/reference/api/endpoints.md)

- [Create compute endpoint](https://neon.com/docs/reference/api/endpoints/create-project-endpoint.md) `POST /projects/{project_id}/endpoints`
- [Delete compute endpoint](https://neon.com/docs/reference/api/endpoints/delete-project-endpoint.md) `DELETE /projects/{project_id}/endpoints/{endpoint_id}`
- [List compute endpoints](https://neon.com/docs/reference/api/endpoints/list-project-endpoints.md) `GET /projects/{project_id}/endpoints`
- [Restart compute endpoint](https://neon.com/docs/reference/api/endpoints/restart-project-endpoint.md) `POST /projects/{project_id}/endpoints/{endpoint_id}/restart`
- [Retrieve compute endpoint details](https://neon.com/docs/reference/api/endpoints/get-project-endpoint.md) `GET /projects/{project_id}/endpoints/{endpoint_id}`
- [Start compute endpoint](https://neon.com/docs/reference/api/endpoints/start-project-endpoint.md) `POST /projects/{project_id}/endpoints/{endpoint_id}/start`
- [Suspend compute endpoint](https://neon.com/docs/reference/api/endpoints/suspend-project-endpoint.md) `POST /projects/{project_id}/endpoints/{endpoint_id}/suspend`
- [Update compute endpoint](https://neon.com/docs/reference/api/endpoints/update-project-endpoint.md) `PATCH /projects/{project_id}/endpoints/{endpoint_id}`

## Functions

[All functions endpoints](https://neon.com/docs/reference/api/functions.md)

- [Delete a custom domain from a branch](https://neon.com/docs/reference/api/functions/delete-project-branch-custom-domain.md) `DELETE /projects/{project_id}/branches/{branch_id}/custom-domains/{domain}`
- [Delete a function on the branch](https://neon.com/docs/reference/api/functions/delete-project-branch-function.md) `DELETE /projects/{project_id}/branches/{branch_id}/functions/{slug}`
- [Deploy code to a function](https://neon.com/docs/reference/api/functions/create-project-branch-function-deployment.md) `POST /projects/{project_id}/branches/{branch_id}/functions/{slug}/deployments`
- [Get function details](https://neon.com/docs/reference/api/functions/get-project-branch-function.md) `GET /projects/{project_id}/branches/{branch_id}/functions/{slug}`
- [List functions on the branch](https://neon.com/docs/reference/api/functions/list-project-branch-functions.md) `GET /projects/{project_id}/branches/{branch_id}/functions`
- [List the custom domains on a branch](https://neon.com/docs/reference/api/functions/list-project-branch-custom-domains.md) `GET /projects/{project_id}/branches/{branch_id}/custom-domains`
- [Register a custom domain on a branch](https://neon.com/docs/reference/api/functions/register-project-branch-custom-domain.md) `POST /projects/{project_id}/branches/{branch_id}/custom-domains`
- [Update a function](https://neon.com/docs/reference/api/functions/update-project-branch-function.md) `PATCH /projects/{project_id}/branches/{branch_id}/functions/{slug}`

## Logs

[All logs endpoints](https://neon.com/docs/reference/api/logs.md)

- [List branch log field values](https://neon.com/docs/reference/api/logs/list-project-branch-log-field-values.md) `GET /projects/{project_id}/branches/{branch_id}/logs/fields/{field_name}/values`
- [List branch log fields](https://neon.com/docs/reference/api/logs/list-project-branch-log-fields.md) `GET /projects/{project_id}/branches/{branch_id}/logs/fields`
- [Query branch logs](https://neon.com/docs/reference/api/logs/query-project-branch-logs.md) `POST /projects/{project_id}/branches/{branch_id}/logs/query`

## Operations

[All operations endpoints](https://neon.com/docs/reference/api/operations.md)

- [List operations](https://neon.com/docs/reference/api/operations/list-project-operations.md) `GET /projects/{project_id}/operations`
- [Retrieve operation details](https://neon.com/docs/reference/api/operations/get-project-operation.md) `GET /projects/{project_id}/operations/{operation_id}`

## Organizations

[All organizations endpoints](https://neon.com/docs/reference/api/organizations.md)

- [Assign or update VPC endpoint](https://neon.com/docs/reference/api/organizations/assign-organization-vpc-endpoint.md) `POST /organizations/{org_id}/vpc/region/{region_id}/vpc_endpoints/{vpc_endpoint_id}`
- [Create organization API key](https://neon.com/docs/reference/api/organizations/create-org-api-key.md) `POST /organizations/{org_id}/api_keys`
- [Create organization invitations](https://neon.com/docs/reference/api/organizations/create-organization-invitations.md) `POST /organizations/{org_id}/invitations`
- [Delete VPC endpoint](https://neon.com/docs/reference/api/organizations/delete-organization-vpc-endpoint.md) `DELETE /organizations/{org_id}/vpc/region/{region_id}/vpc_endpoints/{vpc_endpoint_id}`
- [List organization API keys](https://neon.com/docs/reference/api/organizations/list-org-api-keys.md) `GET /organizations/{org_id}/api_keys`
- [List organization invitations](https://neon.com/docs/reference/api/organizations/get-organization-invitations.md) `GET /organizations/{org_id}/invitations`
- [List organization members](https://neon.com/docs/reference/api/organizations/get-organization-members.md) `GET /organizations/{org_id}/members`
- [List VPC endpoints](https://neon.com/docs/reference/api/organizations/list-organization-vpc-endpoints.md) `GET /organizations/{org_id}/vpc/region/{region_id}/vpc_endpoints`
- [List VPC endpoints across all regions](https://neon.com/docs/reference/api/organizations/list-organization-vpc-endpoints-all-regions.md) `GET /organizations/{org_id}/vpc/vpc_endpoints`
- [Remove organization member](https://neon.com/docs/reference/api/organizations/remove-organization-member.md) `DELETE /organizations/{org_id}/members/{member_id}`
- [Remove organization spending limit](https://neon.com/docs/reference/api/organizations/delete-organization-spending-limit.md) `DELETE /organizations/{org_id}/billing/spending_limit`
- [Retrieve organization details](https://neon.com/docs/reference/api/organizations/get-organization.md) `GET /organizations/{org_id}`
- [Retrieve organization member details](https://neon.com/docs/reference/api/organizations/get-organization-member.md) `GET /organizations/{org_id}/members/{member_id}`
- [Retrieve organization spending limit](https://neon.com/docs/reference/api/organizations/get-organization-spending-limit.md) `GET /organizations/{org_id}/billing/spending_limit`
- [Retrieve VPC endpoint details](https://neon.com/docs/reference/api/organizations/get-organization-vpc-endpoint-details.md) `GET /organizations/{org_id}/vpc/region/{region_id}/vpc_endpoints/{vpc_endpoint_id}`
- [Revoke organization API key](https://neon.com/docs/reference/api/organizations/revoke-org-api-key.md) `DELETE /organizations/{org_id}/api_keys/{key_id}`
- [Set organization spending limit](https://neon.com/docs/reference/api/organizations/set-organization-spending-limit.md) `PUT /organizations/{org_id}/billing/spending_limit`
- [Transfer projects between organizations](https://neon.com/docs/reference/api/organizations/transfer-projects-from-org-to-org.md) `POST /organizations/{source_org_id}/projects/transfer`
- [Update role for organization member](https://neon.com/docs/reference/api/organizations/update-organization-member.md) `PATCH /organizations/{org_id}/members/{member_id}`

## Projects

[All projects endpoints](https://neon.com/docs/reference/api/projects.md)

- [Accept a project transfer request](https://neon.com/docs/reference/api/projects/accept-project-transfer-request.md) `PUT /projects/{project_id}/transfer_requests/{request_id}`
- [Add JWKS URL](https://neon.com/docs/reference/api/projects/add-project-jwks.md) `POST /projects/{project_id}/jwks`
- [Create a project transfer request](https://neon.com/docs/reference/api/projects/create-project-transfer-request.md) `POST /projects/{project_id}/transfer_requests`
- [Create project](https://neon.com/docs/reference/api/projects/create-project.md) `POST /projects`
- [Delete JWKS URL](https://neon.com/docs/reference/api/projects/delete-project-jwks.md) `DELETE /projects/{project_id}/jwks/{jwks_id}`
- [Delete project](https://neon.com/docs/reference/api/projects/delete-project.md) `DELETE /projects/{project_id}`
- [Delete VPC endpoint restriction](https://neon.com/docs/reference/api/projects/delete-project-vpc-endpoint.md) `DELETE /projects/{project_id}/vpc_endpoints/{vpc_endpoint_id}`
- [Grant project access](https://neon.com/docs/reference/api/projects/grant-permission-to-project.md) `POST /projects/{project_id}/permissions`
- [List available shared preload libraries](https://neon.com/docs/reference/api/projects/get-available-preload-libraries.md) `GET /projects/{project_id}/available_preload_libraries`
- [List JWKS URLs](https://neon.com/docs/reference/api/projects/get-project-jwks.md) `GET /projects/{project_id}/jwks`
- [List org members and their project roles](https://neon.com/docs/reference/api/projects/list-project-members.md) `GET /projects/{project_id}/members`
- [List project access](https://neon.com/docs/reference/api/projects/list-project-permissions.md) `GET /projects/{project_id}/permissions`
- [List projects](https://neon.com/docs/reference/api/projects/list-projects.md) `GET /projects`
- [List shared projects](https://neon.com/docs/reference/api/projects/list-shared-projects.md) `GET /projects/shared`
- [List VPC endpoint restrictions](https://neon.com/docs/reference/api/projects/list-project-vpc-endpoints.md) `GET /projects/{project_id}/vpc_endpoints`
- [Recover a deleted project](https://neon.com/docs/reference/api/projects/recover-project.md) `POST /projects/{project_id}/recover`
- [Remove an org member&#39;s role on a project](https://neon.com/docs/reference/api/projects/remove-project-member-role.md) `DELETE /projects/{project_id}/members/{member_id}/role`
- [Retrieve connection URI](https://neon.com/docs/reference/api/projects/get-connection-uri.md) `GET /projects/{project_id}/connection_uri`
- [Retrieve project details](https://neon.com/docs/reference/api/projects/get-project.md) `GET /projects/{project_id}`
- [Revoke project access](https://neon.com/docs/reference/api/projects/revoke-permission-from-project.md) `DELETE /projects/{project_id}/permissions/{permission_id}`
- [Set an org member&#39;s role on a project](https://neon.com/docs/reference/api/projects/set-project-member-role.md) `PUT /projects/{project_id}/members/{member_id}/role`
- [Set VPC endpoint restriction](https://neon.com/docs/reference/api/projects/assign-project-vpc-endpoint.md) `POST /projects/{project_id}/vpc_endpoints/{vpc_endpoint_id}`
- [Update project](https://neon.com/docs/reference/api/projects/update-project.md) `PATCH /projects/{project_id}`

## Regions

[All regions endpoints](https://neon.com/docs/reference/api/regions.md)

- [List supported regions](https://neon.com/docs/reference/api/regions/get-active-regions.md) `GET /regions`

## Snapshots

[All snapshots endpoints](https://neon.com/docs/reference/api/snapshots.md)

- [Create snapshot](https://neon.com/docs/reference/api/snapshots/create-snapshot.md) `POST /projects/{project_id}/branches/{branch_id}/snapshot`
- [Delete snapshot](https://neon.com/docs/reference/api/snapshots/delete-snapshot.md) `DELETE /projects/{project_id}/snapshots/{snapshot_id}`
- [List project snapshots](https://neon.com/docs/reference/api/snapshots/list-snapshots.md) `GET /projects/{project_id}/snapshots`
- [Restore snapshot](https://neon.com/docs/reference/api/snapshots/restore-snapshot.md) `POST /projects/{project_id}/snapshots/{snapshot_id}/restore`
- [Retrieve backup schedule](https://neon.com/docs/reference/api/snapshots/get-snapshot-schedule.md) `GET /projects/{project_id}/branches/{branch_id}/backup_schedule`
- [Update backup schedule](https://neon.com/docs/reference/api/snapshots/set-snapshot-schedule.md) `PUT /projects/{project_id}/branches/{branch_id}/backup_schedule`
- [Update snapshot](https://neon.com/docs/reference/api/snapshots/update-snapshot.md) `PATCH /projects/{project_id}/snapshots/{snapshot_id}`

## Storage

[All storage endpoints](https://neon.com/docs/reference/api/storage.md)

- [Get branch object storage state](https://neon.com/docs/reference/api/storage/get-project-branch-storage.md) `GET /projects/{project_id}/branches/{branch_id}/storage`

## Users

[All users endpoints](https://neon.com/docs/reference/api/users.md)

- [List organizations for the current user](https://neon.com/docs/reference/api/users/get-current-user-organizations.md) `GET /users/me/organizations`
- [Retrieve current user details](https://neon.com/docs/reference/api/users/get-current-user-info.md) `GET /users/me`
- [Retrieve request authentication details](https://neon.com/docs/reference/api/users/get-auth-details.md) `GET /auth`
- [Transfer projects from personal account to organization](https://neon.com/docs/reference/api/users/transfer-projects-from-user-to-org.md) `POST /users/me/projects/transfer`

## Legacy Auth

[All auth-legacy endpoints](https://neon.com/docs/reference/api/auth-legacy.md)

- [Create Auth Provider SDK keys](https://neon.com/docs/reference/api/auth-legacy/create-neon-auth-provider-sdk-keys.md) `POST /projects/auth/keys`
- [Transfer Neon-managed auth project to your own account](https://neon.com/docs/reference/api/auth-legacy/transfer-neon-auth-provider-project.md) `POST /projects/auth/transfer_ownership`
- [Add an OAuth provider](https://neon.com/docs/reference/api/auth-legacy/add-neon-auth-oauth-provider.md) `POST /projects/{project_id}/auth/oauth_providers`
- [Add trusted redirect URI domain](https://neon.com/docs/reference/api/auth-legacy/add-neon-auth-domain-to-redirect-uri-whitelist.md) `POST /projects/{project_id}/auth/domains`
- [Create Neon Auth integration](https://neon.com/docs/reference/api/auth-legacy/create-neon-auth-integration.md) `POST /projects/auth/create`
- [Create new auth user](https://neon.com/docs/reference/api/auth-legacy/create-neon-auth-new-user.md) `POST /projects/auth/user`
- [Delete auth user](https://neon.com/docs/reference/api/auth-legacy/delete-neon-auth-user.md) `DELETE /projects/{project_id}/auth/users/{auth_user_id}`
- [Delete integration with auth provider](https://neon.com/docs/reference/api/auth-legacy/delete-neon-auth-integration.md) `DELETE /projects/{project_id}/auth/integration/{auth_provider}`
- [Delete OAuth provider](https://neon.com/docs/reference/api/auth-legacy/delete-neon-auth-oauth-provider.md) `DELETE /projects/{project_id}/auth/oauth_providers/{oauth_provider_id}`
- [Delete trusted redirect URI domain](https://neon.com/docs/reference/api/auth-legacy/delete-neon-auth-domain-from-redirect-uri-whitelist.md) `DELETE /projects/{project_id}/auth/domains`
- [List active integrations with auth providers](https://neon.com/docs/reference/api/auth-legacy/list-neon-auth-integrations.md) `GET /projects/{project_id}/auth/integrations`
- [List OAuth providers](https://neon.com/docs/reference/api/auth-legacy/list-neon-auth-oauth-providers.md) `GET /projects/{project_id}/auth/oauth_providers`
- [List trusted redirect URI domains](https://neon.com/docs/reference/api/auth-legacy/list-neon-auth-redirect-uri-whitelist-domains.md) `GET /projects/{project_id}/auth/domains`
- [Retrieve email server configuration](https://neon.com/docs/reference/api/auth-legacy/get-neon-auth-email-server.md) `GET /projects/{project_id}/auth/email_server`
- [Update email server configuration](https://neon.com/docs/reference/api/auth-legacy/update-neon-auth-email-server.md) `PATCH /projects/{project_id}/auth/email_server`
- [Update OAuth provider](https://neon.com/docs/reference/api/auth-legacy/update-neon-auth-oauth-provider.md) `PATCH /projects/{project_id}/auth/oauth_providers/{oauth_provider_id}`
