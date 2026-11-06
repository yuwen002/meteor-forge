# Web Admin Authentication and Authorization

> Status: administrator bootstrap, password verification, persistent sessions, login/logout/password-change endpoints, backend RBAC, administrator account management, frontend login/role guards, and authenticated task-audit attribution are implemented in code. Persistent security audit events now cover login success/failure, logout, password changes, and administrator account creation/role changes/deletion; a read-only administrator-only endpoint exposes these events. CI/security-test execution remains outstanding. Startup enforces loopback-only binding; do not expose the service through a reverse proxy or public interface.

## Goals

- Require an authenticated administrator for every management API operation.
- Keep health checks public but disclose no configuration or database details.
- Support a first-run local administrator bootstrap without a public registration endpoint.
- Store password verifiers and opaque session-token hashes, never plaintext passwords or session tokens.
- Make session expiry, logout, and server-side revocation effective immediately.
- Enforce role-based authorization in the backend and expose administrator-only account/security controls.
- Preserve the existing separation between the management metadata database and target databases.

## Threat model and deployment boundary

The management API can create and execute database migrations, roll back schema changes, alter tenant mappings, and create physical shard tables. A local-only listener is a defense-in-depth boundary, not an authentication mechanism.

The command defaults to `127.0.0.1:8081` and currently rejects any `METEOR_ADMIN_ADDR` that is not `localhost`, `127.0.0.1`, or `::1`. A future remote-deployment milestone must separately define TLS termination, trusted proxy handling, secure cookies, access policy, audit retention/integrity, and deployment hardening before relaxing this restriction.

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

The current implementation uses standard-library PBKDF2-HMAC-SHA256 with 310,000 iterations, a 16-byte random per-password salt, a 32-byte derived key, versioned storage encoding, and constant-time comparison. This avoids a new dependency but is a deliberate trade-off versus Argon2id; reassess the work factor and consider Argon2id before wider deployment. Never use plain SHA-256, reversible encryption, or a fast hash for passwords.

Session tokens must be generated from a cryptographically secure random source (at least 256 bits). Return the raw token only in a cookie; persist only its SHA-256 hash. Store username, creation time, expiry time, and revocation state. Use a bounded lifetime (initially 12 hours), HttpOnly, SameSite=Strict, Path=/api/v1 cookies, and Secure when served over HTTPS. Expired sessions must be rejected even if cleanup has not run.

Logout revokes the server-side session and clears the cookie. Password change and administrator disablement revoke all active sessions for that account.

## Implemented bootstrap configuration

- `METEOR_ADMIN_USERNAME`: optional; defaults to `admin`.
- `METEOR_ADMIN_BOOTSTRAP_PASSWORD`: required only when the metadata store has no administrator; minimum 12 characters. Once an administrator exists, the environment variable is ignored and cannot reset its password.
- `POST /api/v1/auth/login` accepts JSON credentials and issues the 12-hour HttpOnly, SameSite=Strict cookie `meteor_admin_session`. The raw token is never persisted; only its SHA-256 digest is stored.
- `POST /api/v1/auth/logout` revokes the current session. `POST /api/v1/auth/password` verifies the current password, changes the password, and revokes all sessions.
- The Vite development proxy preserves the browser Host header so same-origin checks compare the browser Origin to the original UI host.

## HTTP contract

| Method | Endpoint | Authentication | Purpose |
|---|---|---|---|
| POST | `/api/v1/auth/login` | Public, rate-limited | Verify credentials and issue session cookie |
| GET | `/api/v1/auth/session` | Required | Return current administrator identity and session expiry |
| POST | `/api/v1/auth/logout` | Required + CSRF checks | Revoke current session and clear cookie |
| POST | `/api/v1/auth/password` | Required + CSRF checks | Change password and revoke other sessions |
| GET | `/api/v1/auth/users` | Administrator | List administrator accounts (password verifiers are never returned) |
| POST | `/api/v1/auth/users` | Administrator + CSRF checks | Create an administrator account |
| PUT | `/api/v1/auth/users/{id}/role` | Administrator + CSRF checks | Change role and revoke existing sessions |
| DELETE | `/api/v1/auth/users/{id}` | Administrator + CSRF checks | Delete an account; last administrator cannot be removed |
| GET | `/api/v1/auth/security-events` | Administrator | List authentication and administrator lifecycle audit events |
| GET | `/api/v1/health` | Public | Minimal liveness response |

All other `/api/v1/**` endpoints require a valid session. Unknown API routes are behind the same authentication middleware.

Login accepts username/password in a JSON body and returns generic authentication errors so callers cannot enumerate usernames. Responses must never contain password hashes, session tokens, or bootstrap secrets.

## CSRF and browser protections

- Use a host-only cookie (do not set Domain), HttpOnly, SameSite=Strict, and a narrow API path.
- Validate the request Origin against the request host for cookie-authenticated state-changing requests. Reject cross-origin requests; do not enable wildcard CORS.
- Require JSON content type for state-changing authentication endpoints.
- Keep the existing no-store, nosniff, and no-referrer response headers.
- Do not put session tokens in localStorage, URLs, logs, analytics, or frontend application state.
- If cross-origin administration is ever required, design an explicit CSRF-token and trusted-origin model rather than weakening these defaults.

## Login abuse controls

A bounded in-memory limiter is implemented with two independent buckets: direct peer IP and direct peer IP + normalized username. Each bucket allows five attempts, then applies a 30-second cooldown; attempts after the cooldown begin a fresh window. It caps tracked keys at 4,096 and prunes expired entries. It does not trust forwarded headers. A future multi-instance deployment must move throttling to shared storage.

## Implemented RBAC model

Start with explicit least-privilege roles:

- `viewer`: read data sources, migration status, tenants, shard rules, task details, and audit events.
- `migration_operator`: viewer permissions plus migration preview and migration execution; rollback remains separately gated.
- `shard_operator`: viewer permissions plus shard clone preview and shard DDL execution.
- `administrator`: user/session management and role assignment, plus all operational permissions.

The backend currently restricts migration execution and tenant migration batches to `migration_operator` (with rollback/version-changing actions separately restricted to `administrator`); shard clone operations to `shard_operator`; and administrator account/security-audit endpoints to `administrator`. Migration operators may cancel only `up` tasks; cancellation of rollback or version-change tasks is administrator-only. All authenticated roles can read operational status and audit views.

Authorization must be enforced in backend handlers/services, not only by hiding frontend buttons. Destructive rollback and physical DDL retain explicit confirmation requirements. Operational task audit records store the authenticated actor. Security audit records store actor/subject, action, outcome, direct peer address, a short static detail, and timestamp. Administrator creation, role changes, deletion, password changes, login-session creation, and logout are committed in the same metadata-database transaction as their success audit event; an audit insert failure rolls back the associated mutation. Passwords, cookies, session tokens, DSNs, and raw database errors must never be written.

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
- RBAC tests prove backend enforcement for every read/write/execute class, including migration-task cancellation by action type.
- Security audit event persistence, administrator-only access, and absence of passwords, cookie values, and raw session tokens.

## Merge gate

Do not mark the management-platform PR ready for review until authentication and authorization are enforced server-side, the security tests above pass, the local-only bind default is verified, and the latest Go, integration, lint, and frontend CI jobs pass on the exact PR head SHA.


## Security audit events

The Web Admin exposes these events on the administrator-only **系统管理 → 安全审计** page.

`GET /api/v1/auth/security-events` is administrator-only and supports `limit` (1–200, default 50) and `offset` (0–10,000,000, default 0). Events include login success/failure, logout, password changes, and administrator account creation, role changes, and deletion. Each event contains actor, subject, action, outcome, direct peer address, a short static detail, and UTC timestamp. The event stream never stores submitted passwords, session cookies, token values, DSNs, or raw driver errors. Failed credential checks are recorded without distinguishing unknown usernames from incorrect passwords.
