# Web Admin Authentication and Authorization

> Status: implementation specification. The current management API does not yet enforce administrator authentication or RBAC. Until the implementation and security tests are complete, keep the server bound to loopback and do not expose it through a reverse proxy or public interface.

## Goals

- Require an authenticated administrator for every management API operation.
- Keep health checks public but disclose no configuration or database details.
- Support a first-run local administrator bootstrap without a public registration endpoint.
- Store password verifiers and opaque session-token hashes, never plaintext passwords or session tokens.
- Make session expiry, logout, and server-side revocation effective immediately.
- Add role-based authorization only after authentication is stable.
- Preserve the existing separation between the management metadata database and target databases.

## Threat model and deployment boundary

The management API can create and execute database migrations, roll back schema changes, alter tenant mappings, and create physical shard tables. A local-only listener is a defense-in-depth boundary, not an authentication mechanism.

The production command must continue to default to `127.0.0.1:8081`. A non-loopback bind must require an explicit deployment decision and must not be treated as safe merely because a password is configured. Deployments that need remote access should place the service behind TLS and a trusted access layer.

Do not trust `X-Forwarded-For` or similar headers for authentication, audit identity, or login throttling unless a future explicit trusted-proxy configuration is implemented.

## First-run administrator bootstrap

- Username comes from `METEOR_ADMIN_USERNAME`; default: `admin`.
- On first initialization only, require `METEOR_ADMIN_BOOTSTRAP_PASSWORD`.
- Require a strong password (minimum 12 characters; reject whitespace-only and oversized values).
- Persist only a salted password verifier.
- Once an administrator exists, ignore the bootstrap password environment variable; never reset an existing password from environment on every restart.
- Do not expose a public registration or password-reset endpoint.
- If the metadata store has no administrator and no bootstrap password is configured, fail startup with a clear configuration error rather than starting an unauthenticated management API.
- Password rotation should be a separate authenticated operation with current-password verification and session revocation.

## Password and session storage

The initial implementation should use a reviewed password hashing algorithm with a per-password cryptographic salt and versioned parameters. Prefer Argon2id; if the repository avoids adding a dependency, use a carefully implemented standard-library PBKDF2-HMAC-SHA256 with a high iteration count and constant-time comparison, and document the trade-off. Never use plain SHA-256, reversible encryption, or a fast hash for passwords.

Session tokens must be generated from a cryptographically secure random source (at least 256 bits). Return the raw token only in a cookie; persist only its SHA-256 hash. Store username, creation time, expiry time, and revocation state. Use a bounded lifetime (initially 12 hours), HttpOnly, SameSite=Strict, Path=/api/v1 cookies, and Secure when served over HTTPS. Expired sessions must be rejected even if cleanup has not run.

Logout revokes the server-side session and clears the cookie. Password change and administrator disablement revoke all active sessions for that account.

## HTTP contract

| Method | Endpoint | Authentication | Purpose |
|---|---|---|---|
| POST | `/api/v1/auth/login` | Public, rate-limited | Verify credentials and issue session cookie |
| GET | `/api/v1/auth/session` | Required | Return current administrator identity and session expiry |
| POST | `/api/v1/auth/logout` | Required + CSRF checks | Revoke current session and clear cookie |
| POST | `/api/v1/auth/password` | Required + CSRF checks | Change password and revoke other sessions |
| GET | `/api/v1/health` | Public | Minimal liveness response |

All other `/api/v1/**` endpoints must require a valid session. Unknown API routes must not bypass authentication.

Login accepts username/password in a JSON body and returns generic authentication errors so callers cannot enumerate usernames. Responses must never contain password hashes, session tokens, or bootstrap secrets.

## CSRF and browser protections

- Use a host-only cookie (do not set Domain), HttpOnly, SameSite=Strict, and a narrow API path.
- Validate the request Origin against the request host for cookie-authenticated state-changing requests. Reject cross-origin requests; do not enable wildcard CORS.
- Require JSON content type for state-changing authentication endpoints.
- Keep the existing no-store, nosniff, and no-referrer response headers.
- Do not put session tokens in localStorage, URLs, logs, analytics, or frontend application state.
- If cross-origin administration is ever required, design an explicit CSRF-token and trusted-origin model rather than weakening these defaults.

## Login abuse controls

Implement a bounded in-memory rate limiter keyed by the direct peer address and normalized username, with a generic error response and a short cooldown. Bound the number of tracked keys and periodically discard expired entries. Do not trust forwarded headers by default. Record authentication success/failure without recording passwords, cookies, or raw tokens. A future multi-instance deployment must move throttling to shared storage.

## RBAC model (second milestone)

Start with explicit least-privilege roles:

- `viewer`: read data sources, migration status, tenants, shard rules, task details, and audit events.
- `migration_operator`: viewer permissions plus migration preview and migration execution; rollback remains separately gated.
- `shard_operator`: viewer permissions plus shard clone preview and shard DDL execution.
- `administrator`: user/session management and role assignment, plus all operational permissions.

Authorization must be enforced in backend handlers/services, not only by hiding frontend buttons. Destructive rollback and physical DDL retain explicit confirmation requirements. Audit records should store the authenticated actor and a stable permission/action identifier.

## Required security tests

- First boot refuses to start without a bootstrap password; creates exactly one administrator when configured.
- Restart does not overwrite an existing password from environment variables.
- Password hashes are salted and never returned by API responses.
- Correct and incorrect credentials; generic error behavior for unknown usernames.
- Session cookie attributes, token-hash-only persistence, expiry, logout, and server-side revocation.
- Every management endpoint rejects missing, invalid, expired, and revoked sessions.
- Health and login remain reachable without a session; no other endpoint does.
- Cross-origin and missing-Origin state-changing requests are rejected according to the chosen browser contract.
- Login throttling is bounded and does not trust spoofed forwarding headers.
- Password change revokes previous sessions.
- RBAC tests prove backend enforcement for every read/write/execute class.
- Logs and audit messages never contain passwords, cookie values, or raw session tokens.

## Merge gate

Do not mark the management-platform PR ready for review until authentication and authorization are enforced server-side, the security tests above pass, the local-only bind default is verified, and the latest Go, integration, lint, and frontend CI jobs pass on the exact PR head SHA.
