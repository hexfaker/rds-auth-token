# rds-auth-token

A tiny, statically-linked Go binary that generates an **AWS RDS IAM database
authentication token** — a drop-in replacement for
`aws rds generate-db-auth-token`.

An RDS IAM auth token is a short-lived (15-minute) credential you use *in place
of a database password* to connect to an RDS/Aurora instance that has IAM
database authentication enabled. Under the hood the token is just a SigV4
**presigned URL** for `GET https://<host>:<port>/?Action=connect&DBUser=<user>`
against the `rds-db` service, with the `https://` scheme stripped off.

## Why this exists

- **The official AWS CLI v2 is a Python application.** On many distributions
  `aws` is a Python script bound to the system Python and its libraries. You
  can't always run it where you actually need a token — notably **inside a
  Flatpak sandbox**, which ships its own runtime and has neither that script
  nor its Python dependencies.
- **This binary is fully static** (`CGO_ENABLED=0`), about 9 MB, with zero
  runtime dependencies. Drop it anywhere — a container, a sandbox, a minimal
  host — and it just works.
- **It is flag-compatible** with `aws rds generate-db-auth-token`
  (`--hostname --port --username --region`) **and adds an explicit `--profile`
  flag**. That lets it slot into tools that shell out a per-connection command
  (pgAdmin, scripts, etc.) without juggling the `AWS_PROFILE` environment
  variable.
- It resolves the **full AWS credential chain, including SSO**, from `~/.aws`
  via the AWS SDK for Go v2 (`config.LoadDefaultConfig`).

## Install

With Go installed:

```sh
go install github.com/hexfaker/rds-auth-token@latest
```

This produces a binary named `rds-auth-token` in `$(go env GOPATH)/bin`. Set
`GOBIN` to install elsewhere, e.g. `GOBIN=~/.local/bin go install ...`.

Or **download a prebuilt static binary** from the
[Releases](https://github.com/hexfaker/rds-auth-token/releases) page
(linux/darwin, amd64/arm64), extract the `tar.gz`, and drop the binary
wherever you need it.

> **Running inside a sandbox/container?** Use a **static** binary. The release
> artifacts are static (`CGO_ENABLED=0`); a bare `go install` is *dynamically*
> linked by default and won't run against a different runtime's glibc. Build a
> static one with `CGO_ENABLED=0 go install github.com/hexfaker/rds-auth-token@latest`.

## Usage

```
rds-auth-token --hostname H --port N --username U [--region R] [--profile P]
```

| Flag         | Description                                                                 |
| ------------ | --------------------------------------------------------------------------- |
| `--hostname` | RDS endpoint hostname (required).                                           |
| `--port`     | Port, e.g. `5432` for Postgres, `3306` for MySQL (required).                |
| `--username` | Database user to authenticate as (`--user` is accepted as an alias).        |
| `--region`   | AWS region. Optional — see region resolution below.                         |
| `--profile`  | Shared-config profile to load from `~/.aws` (optional).                     |

The token is printed to stdout. Errors and usage go to stderr.

**Basic example:**

```sh
PGPASSWORD="$(rds-auth-token \
  --profile prod \
  --hostname mydb.abc123xyz.eu-central-1.rds.amazonaws.com \
  --port 5432 \
  --username app_user)" \
  psql "host=mydb.abc123xyz.eu-central-1.rds.amazonaws.com port=5432 dbname=app user=app_user sslmode=require"
```

**Region resolution** (highest priority first):

1. The explicit `--region` flag.
2. The region resolved from the profile / environment.
3. The region **extracted from the RDS hostname**
   (`<name>.<id>.<region>.rds.amazonaws.com`). This keeps the tool working even
   when a profile has no default region configured.

**AWS profiles / SSO:** credentials are resolved through the standard SDK chain,
so environment variables, `AWS_PROFILE`, static credentials, assumed roles, and
**IAM Identity Center (SSO)** all work. If you use SSO, run `aws sso login`
(or `aws sso login --profile <profile>`) on the host first so a valid cached SSO
token exists; this binary then consumes that cached token. Generating the token
performs no RDS API call, but resolving SSO credentials does make the SSO
`GetRoleCredentials` network call the SDK needs.

## Worked example: pgAdmin inside a Flatpak sandbox

This is the use case that motivated the tool.

**Problem.** The pgAdmin Flatpak (`org.pgadmin.pgadmin4`) runs in its own
sandbox with its own runtime. It cannot run the host's Python `aws` CLI to fetch
an IAM token. You *could* reach for `flatpak-spawn --host`, but that is a full
sandbox escape — it lets the app run arbitrary commands on your host, which is
exactly what the sandbox is supposed to prevent.

**Solution — least privilege.** Give the sandbox a few narrow, read-only-ish
things: the static binary mounted read-only, read-only access to your AWS
config, and read-write access to *only* the SSO token cache. No host exec.

1. Install the **static** binary on the host (a dynamic build won't run inside
   the Flatpak runtime — see the install note above):

   ```sh
   CGO_ENABLED=0 GOBIN=~/.local/bin go install github.com/hexfaker/rds-auth-token@latest
   ```

2. Grant the sandbox three minimal things in one override:

   ```sh
   flatpak override --user \
     --filesystem=~/.local/bin/rds-auth-token:ro \
     --filesystem=~/.aws:ro \
     --filesystem=~/.aws/sso/cache \
     org.pgadmin.pgadmin4
   ```

   - **the binary, read-only** — mounted, not copied, so a `go install` update
     flows through (restart pgAdmin to pick it up);
   - **`~/.aws`, read-only** — credentials/config are read but never altered;
   - **`~/.aws/sso/cache`, read-write** — required so the SDK can refresh an
     expired SSO token, which it writes back here. The nested override keeps the
     rest of `~/.aws` read-only. *If you only use static credentials (no SSO),
     drop this line and keep `~/.aws` fully read-only.*

   (Mounting the binary at its host path makes it appear at the same absolute
   path inside the sandbox.)

3. In the pgAdmin server's connection settings, set **"exec command to set the
   password"** (the `passexec_cmd` field) to:

   ```
   /home/<you>/.local/bin/rds-auth-token --profile <profile> --hostname <rds-endpoint> --port %PORT% --username %USERNAME%
   ```

   pgAdmin substitutes `%PORT%` and `%USERNAME%` from the connection's own
   fields, so the command stays in sync with the rest of the server definition.

**SSO note.** `aws sso login` still happens on the **host**, where the AWS CLI
lives. The binary *consumes* the cached SSO token from `~/.aws` and refreshes it
when it expires (hence the cache being writable). Token generation makes no RDS
API call, but resolving SSO credentials makes the `GetRoleCredentials` network
call — pgAdmin's sandbox already has network access.

**Why this is least-privilege:** the sandbox gains no ability to execute host
processes (no `flatpak-spawn --host`); the binary and your AWS config are
*read-only*; and the only writable grant is the SSO token cache, which it
already reads. A compromised pgAdmin can, at worst, read your AWS config and
churn your SSO token cache — not spawn a shell or touch the rest of your home.

## Prior art

[`keymon/rds-generate-db-auth-token-go`](https://github.com/keymon/rds-generate-db-auth-token-go)
is a similar Go tool that also wraps `BuildAuthToken`. `rds-auth-token` adds an
explicit `--profile` flag (and SSO-aware, hostname-derived region resolution),
making it a flag-compatible, profile/SSO-aware drop-in for the AWS CLI command.

## How it works

The token is a SigV4 *presigned* request, computed locally:
`rds-auth-token` resolves credentials via the AWS SDK for Go v2 and calls
`feature/rds/auth.BuildAuthToken`, which signs a
`GET https://<host>:<port>/?Action=connect&DBUser=<user>` request for the
`rds-db` service with a 15-minute expiry and strips the scheme. Producing the
token makes **no AWS API call** for the token itself — only credential
resolution (e.g. SSO/STS) may hit the network.

## License

[MIT](LICENSE) © 2026 hexfaker
