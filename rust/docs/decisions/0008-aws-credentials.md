# 0008: AWS credentials

Status: accepted by the owner, 2026-11-10. Ticket: PGMI-829.
Checked on crates.io / docs.rs, 2026-11-10: aws-config 1.13.1 (2026-11-09,
rust-version 1.94.1, default features `default-https-client`, `rt-tokio`,
`credentials-process`, `sso`), aws-smithy-http-client 1.5.2 (feature
`rustls-ring`; `Builder::tls_provider(tls::Provider::Rustls(CryptoMode::Ring))`),
aws-sigv4 1.6.5 (`http_request::sign`, `SignatureLocation`,
`SigningSettings`), aws-credential-types 1.3.1, aws-sdk-rds 1.161.0
(has `auth_token::AuthTokenGenerator`; 2.4 MB compressed source).

## Context

`--aws` sends an RDS IAM token as the password: a SigV4 presigned
`host:port/?Action=connect&DBUser=<user>` with `X-Amz-Expires=900`, service
`rds-db`, built locally from the AWS credentials. Region: `--aws-region`,
else `AWS_REGION`, else `AWS_DEFAULT_REGION`, else exit 10. The black-box
case records the exact token shape against a fake server. AWS is checked
against local fakes only.

## Options

1. **aws-sdk-rds `AuthTokenGenerator`.** Ready-made and maintained, but it
   compiles the whole RDS API for one function.
2. **aws-sigv4 directly**, with aws-config for credentials: the same signing
   code the generator calls, about 60 lines in pgmi.
3. **Hand-written credentials and signing.** Loses the AWS credential chain
   (profiles, SSO, process credentials, web identity, ECS, IMDS), which is
   the hard part.

## Decision

Option 2.

- aws-config with `default-features = false`, features `rt-tokio`,
  `credentials-process`, `sso`; its HTTP client is pgmi's own
  implementation of aws-smithy-runtime-api's `HttpClient` on the shared
  reqwest client (about 80 lines), since aws-smithy-http-client offers only
  ring or aws-lc-rs, both C (record 0021, owner requirement PGMI-855).
- Credentials from the default chain (`aws_config::defaults(...)`), given
  pgmi's own region, never the region from `~/.aws/config` (the contract
  says no region is exit 10).
- The token: an `https://host:port/?Action=connect&DBUser=<user>` request
  signed with `SignatureLocation::QueryParams`, `expires_in` 900 s, service
  `rds-db`, the scheme removed. A session token adds
  `X-Amz-Security-Token`, as AWS's generator does.
- A fresh token for each dial attempt, so retries and multi-host never send
  an expired token.
- No credentials, or a failing provider, is exit 11.
- Default sslmode is `verify-full` with a root certificate configured, else
  `require` (`docs/CONNECTIONS.md`); plaintext modes are exit 10 (record
  0006).

## Consequences

- AWS raises its crates' rust-version roughly monthly; updates are pinned by
  `Cargo.lock` and taken on purpose (record 0001).
- If the token format ever changes, `cloud/aws-iam-token` fails, which is
  the point of the case.

## Serves

Cloud sign-in; TLS (token sign-in refuses plaintext sslmodes).
