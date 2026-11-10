# 0009: Google Cloud SQL

Status: accepted, 2026-11-10. Ticket: PGMI-829. Builds on spike B.
Checked on crates.io / docs.rs / rustsec.org, 2026-11-10: google-cloud-auth
1.18.0 (2026-10-21; default features `default-rustls-provider` and
`default-idtoken-backend`, both of which enable aws-lc-rs), reqwest 0.13.5,
rsa 0.10.0 (2026-11-08; RUSTSEC-2023-0071, the Marvin timing attack, is
patched from 0.10.0), x509-cert 0.3.0, cloud-sql-connector 0.8.0 (ruled out
by spike B).

## Context

Spike B decided: pgmi writes its own Cloud SQL dialer (two Admin API calls,
an RSA key, TLS 1.3 to the instance with the ephemeral client certificate,
the IAM login token as the password) and takes Application Default
Credentials from `google-cloud-auth`. The contract adds: `--google` needs
`--google-instance` (exit 10); a sign-in failure is exit 11 (intended change);
no metrics are sent.

## Options

Spike B weighed the community connector against an own dialer. Left to
decide here is how to reach the dialer and keep aws-lc-rs out:

1. google-cloud-auth with default features (aws-lc-rs in the build).
2. google-cloud-auth with `default-features = false`, using the provider
   installed in `main` (record 0021) through reqwest's `rustls-no-provider`.

## Decision

Option 2, with spike B's dialer:

- `GET .../projects/{p}/instances/{i}/connectSettings` and
  `POST .../instances/{i}:generateEphemeralCert` (public key plus the IAM
  login token, scope `sqlservice.login`) over the shared reqwest client.
- An RSA 2048 key from `rsa` 0.10 (key generation only; signing is done by
  the rustls provider from the PKCS#8 export).
- TLS 1.3 to port 3307 of the instance's primary IP, trusting only the
  instance's server CA, and checking the instance's identity: the
  certificate's CN `project:instance` for Google's legacy CA (read with
  `x509-cert`), the DNS name from `connectSettings` for CAS-based instances.
- The stream is handed to the connection layer through `connect_raw` like
  any other (record 0004). One connection per deploy, so there is no
  background certificate refresh.
- The Admin API base URL comes from `CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN`
  when set, gcloud's own variable for the same thing, so the black-box
  `fakes` server can stand in for Google.
- Any credential or Admin API failure is exit 11 and names the dialer.

## Consequences

- About 500 lines in pgmi; the real path stays untested by owner decision.
- Checked in google-cloud-auth 1.18.0's source
  (`src/credentials/crypto_provider.rs`): without `default-rustls-provider`
  it signs service-account JWTs with the key provider of the process-wide
  default rustls provider, and panics if none is installed. pgmi installs
  its provider first thing in `main`, from the same rustls version. A unit
  test on the first build signs a JWT with a test service-account key.
- The test seam is gcloud's own `CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN`,
  so pgmi adds no variable of its own.
- Only the primary IP is supported, as today; private IP and Private Service
  Connect stay out until a flag is asked for.

## Serves

Cloud sign-in; Google sign-in failure is exit 11; Nothing that cannot be
reached is carried over (no metrics).
