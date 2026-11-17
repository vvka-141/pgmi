"""
End-to-end harness for the advanced template's reference gateways, the same
for every language. It deploys the template (env=dev) into PostgreSQL, runs
a mock OIDC provider, starts the gateway under test and speaks HTTP to it:

    rest   the gateways/PROTOCOL.md wire rules on /api, /auth/dev-login,
           /auth/logout, /health and unknown paths
    rpc    POST /rpc: pgmi's JSON-RPC endpoint and its example action
    mcp    POST /mcp: both protocol eras, header checks, status mapping,
           the serialization-failure retry, pool exhaustion
    login  OIDC sign-in: state, PKCE, ID token checks, provider failures
    intercept  the extension seams: bytes untouched without interceptors,
           the example interceptors (INTERCEPTORS, EMBED_FAILURE_POLICY),
           both failure policies, concurrent load with and without them
    oauth  /mcp as an OAuth resource server (MCP_RESOURCE,
           MCP_AUTHORIZATION_SERVERS): metadata, the 401 challenge, strict
           audience, pairing and approval, rate limits (RATE_LIMIT_PER_IP,
           RATE_LIMIT_PER_CLIENT)
    deadline  REQUEST_DEADLINE_SECONDS reaches PostgreSQL as statement_timeout:
           a slow handler is cancelled on the server and answered 504
    hangup  a client that hangs up mid-request cancels its statement

    PGMI_BIN=/path/to/pgmi \\
    PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:5432/postgres \\
    GATEWAY_DIR=/tmp/project/gateways/go GATEWAY_COMMAND=./gateway \\
    python run.py

GATEWAY_COMMAND is an argv (shell-quoted) run in GATEWAY_DIR; the harness
sets DATABASE_URL, HOST, PORT, ALLOWED_ORIGINS, DEV_LOGIN, LOGIN_CSRF_SECRET,
POOL_MAX_SIZE, RETRY_MAX_ATTEMPTS, RETRY_BASE_DELAY, REQUEST_DEADLINE_SECONDS and OIDC_*. For a
container, {env_file} and {port} in GATEWAY_COMMAND and GATEWAY_STOP are
substituted:

    GATEWAY_COMMAND='docker run --rm --name gw-{port} --network host
        --env-file {env_file} -v .:/src -w /src image ./gateway'
    GATEWAY_STOP='docker rm -f gw-{port}'

E2E_SUITES (default rest,rpc,mcp,login,intercept,oauth,deadline,hangup) picks suites. EXPECTED_MISSING names
suites the gateway does not implement yet: they still run, their failures
are reported and do not fail the run, and a suite on that list that passes
fails it, so the list cannot go stale. GATEWAY_READY_SECONDS (default 120)
bounds the wait for GET /health.
"""

import io
import os
import sys
import unittest

import harness

SUITES = ("rest", "rpc", "mcp", "login", "intercept", "oauth", "deadline", "hangup")


def main():
    chosen = [s.strip() for s in os.environ.get("E2E_SUITES", ",".join(SUITES)).split(",") if s.strip()]
    missing = {s.strip() for s in os.environ.get("EXPECTED_MISSING", "").split(",") if s.strip()}
    unknown = (set(chosen) | missing) - set(SUITES)
    if unknown:
        sys.exit(f"unknown suite(s): {', '.join(sorted(unknown))}; known: {', '.join(SUITES)}")

    summary = []
    failed = False
    try:
        harness.world()
        for suite in chosen:
            tests = unittest.defaultTestLoader.loadTestsFromName("test_" + suite)
            if suite in missing:
                result = unittest.TextTestRunner(stream=io.StringIO(), verbosity=0).run(tests)
                broken = {getattr(t, "test_case", t).id()
                          for t, *_ in result.failures + result.errors + [(t,) for t in result.unexpectedSuccesses]}
                if result.wasSuccessful():
                    failed = True
                    summary.append(f"{suite}: PASSES but is listed in EXPECTED_MISSING; remove it from the list")
                else:
                    summary.append(f"{suite}: EXPECTED-MISSING ({len(broken)} of {result.testsRun} fail)")
            else:
                print(f"\n=== {suite} ===", flush=True)
                result = unittest.TextTestRunner(stream=sys.stdout, verbosity=2).run(tests)
                failed = failed or not result.wasSuccessful()
                summary.append(f"{suite}: {'PASS' if result.wasSuccessful() else 'FAIL'} "
                               f"({result.testsRun} tests, {len(result.expectedFailures)} expected failures)")
    finally:
        world = harness._world
        if failed and world is not None:
            for gateway in world.instances:
                print(f"\n--- gateway on port {gateway.port} ---\n{gateway.output()}")
        harness.close_world()

    print("\n" + "\n".join(summary))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
