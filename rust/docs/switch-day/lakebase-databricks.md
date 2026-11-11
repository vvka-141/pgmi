# Switch-day change: docs/LAKEBASE.md, `--databricks`

Apply on switch day (PGMI-835). Removed from the live docs on 2026-11-11
because the site describes the installed Go binary (owner, PGMI-835).

The patch below restores the section against `docs/LAKEBASE.md` as of
v0.14.1 (`git apply` from the repo root, after stripping this header).
Before applying, update the `sslmode: prefer` note: PGMI-873 removes
`sslmode` from `pgmi init`.

```diff
diff --git a/docs/LAKEBASE.md b/docs/LAKEBASE.md
index 7afae9d8..cb8cddcb 100644
--- a/docs/LAKEBASE.md
+++ b/docs/LAKEBASE.md
@@ -50,7 +50,70 @@ right before the deploy. Lakebase checks its expiry only at login, so a deploy
 that is already connected may run past the hour.
 
 A plain Microsoft Entra access token is refused as the password, so pgmi's
-`--azure` does not work against Lakebase; get the credential first, as above.
+`--azure` does not work against Lakebase; get the credential first, as above,
+or let pgmi get it (next section).
+
+## Sign in with `--databricks`
+
+From pgmi 0.20 (the Rust binary), pgmi gets the database credential itself:
+
+```bash
+export DATABRICKS_HOST=https://<workspace host>     # or a ~/.databrickscfg profile
+pgmi deploy . --databricks \
+  --databricks-endpoint projects/<project>/branches/production/endpoints/primary \
+  --connection "host=<endpoint host> port=5432 dbname=databricks_postgres user=<you>" \
+  -d myapp --param env=dev
+```
+
+> **Checked on real Lakebase** on 2026-11-11 (Azure Databricks, PostgreSQL 18):
+> the Azure CLI sign-in, a personal access token, and OAuth M2M from a
+> workstation and from a serverless job, each deploying with `verify-full`
+> and no `PGPASSWORD`. The Databricks CLI route was not checked: it needs
+> `databricks auth login` (a browser) first. The credential lasts one hour.
+> A project scaffolded by `pgmi init` sets `sslmode: prefer` in `pgmi.yaml`,
+> which `--databricks` refuses: pass `--sslmode verify-full` or edit
+> `pgmi.yaml`.
+
+- **The endpoint** comes from `--databricks-endpoint`, else
+  `PGMI_DATABRICKS_ENDPOINT`, in the form
+  `projects/<project>/branches/<branch>/endpoints/<endpoint>`.
+- **The workspace** comes from `DATABRICKS_HOST`, else a profile in
+  `~/.databrickscfg`: `DATABRICKS_CONFIG_PROFILE`, else the file's
+  `default_profile`, else `DEFAULT` (`DATABRICKS_CONFIG_FILE` moves the file).
+  It must be `https`.
+- **The sign-in**, first match, as the Databricks SDKs order it:
+  1. `DATABRICKS_TOKEN`, a personal access token;
+  2. `DATABRICKS_CLIENT_ID` and `DATABRICKS_CLIENT_SECRET`, OAuth M2M for a
+     service principal (a CI pipeline or a job);
+  3. the Databricks CLI (`databricks auth login` beforehand; pgmi runs
+     `databricks auth token`);
+  4. on Azure Databricks, pgmi's Azure sign-in (the Azure CLI, managed
+     identity, `AZURE_*` service principal), as for `--azure`.
+
+  `DATABRICKS_AUTH_TYPE` picks one: `pat`, `oauth-m2m`, `databricks-cli`,
+  or `azure-cli`, `azure-msi`, `azure-client-secret` (each means pgmi's Azure
+  sign-in; the SDKs' `ARM_*` variables are not read).
+- **Refresh.** pgmi reuses the credential for every connection of the deploy
+  and gets a new one when less than five minutes are left, so a long deploy
+  that connects again does not present an expired credential.
+- **The user** is still yours to name (`user=` above, or `-U`): the
+  Databricks identity's Postgres role, as in the table above. A credential the
+  database refuses fails with exit 11 and a hint naming Databricks, the user
+  and `--username`.
+- **TLS.** Without an sslmode pgmi uses `verify-full` against the system trust
+  store, and the endpoint's certificate passes it. `disable`, `allow` and
+  `prefer` are refused.
+- **From a job.** pgmi started as a subprocess of a job sees no Databricks
+  credentials of its own. The job's script sets `DATABRICKS_HOST`, and either
+  `DATABRICKS_TOKEN` (for example from the SDK's `w.config.authenticate()`, the
+  job's identity) or a service principal's `DATABRICKS_CLIENT_ID` and
+  `DATABRICKS_CLIENT_SECRET` from a secret scope.
+- **Failures.** No endpoint, no workspace, an `http` workspace or an unknown
+  `DATABRICKS_AUTH_TYPE` stop the deploy before connecting (exit 10). A
+  workspace that refuses (401, 403, 404) or stays unavailable after three
+  tries is exit 11, with the reason the API gave. An invalid personal access
+  token is answered with 403, not 401. No token is printed, also
+  not with `--verbose` or `--json`.
 
 Only the identity that created the project has a Postgres role from the start.
 Another user or service principal needs one before it can connect: after
```
