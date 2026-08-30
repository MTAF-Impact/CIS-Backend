# Authentication

The PRD defines no user model, so this is a deliberately minimal login flow:
email + password, JWT access token, rotating refresh token, and **no roles**.
Every authenticated user can call every endpoint, including F4.

Passwords are hashed with bcrypt (cost configurable via `BCRYPT_COST`).
Refresh tokens are stored only as SHA-256 hashes — the raw value is returned
once and never persisted.

---

## POST /api/v1/auth/register

Creates an account and signs it in.

Disabled when `AUTH_ALLOW_REGISTRATION=false`, which returns `403 FORBIDDEN`.
Turn it off once your team has accounts and use `SEED_USER_*` instead.

**Auth:** none

**Body**

| Field | Type | Rules |
|---|---|---|
| `email` | string | required, valid email, ≤255 |
| `password` | string | required, 8–128 characters |
| `name` | string | required, 2–255 |

```bash
curl -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"analyst@jakarta.go.id","password":"StrongPass123!","name":"Sari Analyst"}'
```

**201 Created**

```json
{
  "success": true,
  "message": "account created",
  "data": {
    "user": {
      "id": "21c4bbdd-f208-4696-a467-9f0edc23e910",
      "email": "analyst@jakarta.go.id",
      "name": "Sari Analyst",
      "last_login_at": null,
      "created_at": "2026-08-30T14:31:46Z"
    },
    "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refresh_token": "V0xQ3n8dR2pK...",
    "token_type": "Bearer",
    "expires_in": 86400
  }
}
```

**Errors** — `409 CONFLICT` email exists · `403 FORBIDDEN` registration disabled ·
`400 VALIDATION_FAILED`

---

## POST /api/v1/auth/login

**Auth:** none

**Body:** `email`, `password` (both required)

```bash
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"analyst@jakarta.go.id","password":"StrongPass123!"}'
```

**200 OK** — same `data` shape as register, with `message: "signed in"`.

**Errors** — `401 UNAUTHORIZED` for both a wrong password and an unknown email.
The two are deliberately indistinguishable, and the endpoint performs a dummy
hash comparison on unknown emails so response timing does not leak which
addresses are registered.

---

## POST /api/v1/auth/refresh

Exchanges a refresh token for a new pair. **The presented token is single-use**:
it is revoked as part of the exchange, so replaying it fails.

**Auth:** none (the refresh token is the credential)

**Body:** `refresh_token` (required)

```bash
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refresh_token":"V0xQ3n8dR2pK..."}'
```

**200 OK** — same shape as login.

**Errors** — `401 UNAUTHORIZED` if the token is unknown, already used, revoked,
expired, or its account was deleted.

---

## GET /api/v1/auth/me

Returns the calling user's profile.

**Auth:** Bearer

```bash
curl http://localhost:8080/api/v1/auth/me -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "current user",
  "data": {
    "id": "21c4bbdd-f208-4696-a467-9f0edc23e910",
    "email": "analyst@jakarta.go.id",
    "name": "Sari Analyst",
    "last_login_at": "2026-08-30T14:32:57Z",
    "created_at": "2026-08-30T14:31:46Z"
  }
}
```

---

## POST /api/v1/auth/logout

Revokes **every** refresh token for the user, ending all sessions.

**Auth:** Bearer

```bash
curl -X POST http://localhost:8080/api/v1/auth/logout -H "Authorization: Bearer $TOKEN"
```

**200 OK** — `{"success": true, "message": "signed out"}`

> Access tokens are stateless and stay valid until they expire. Keep
> `JWT_ACCESS_TTL` short if immediate revocation matters to you.
