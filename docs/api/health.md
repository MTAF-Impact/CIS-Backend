# Health Probes

Both routes are public — no token required.

---

## GET /health

Liveness. Confirms the process is running and answering.

It deliberately performs **no dependency checks**, so a transient database blip
never causes an orchestrator to restart an otherwise healthy container. Use this
for Docker/Kubernetes liveness probes.

```bash
curl http://localhost:8080/health
```

**200 OK**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "status": "healthy",
    "service": "CIS Backend",
    "environment": "development",
    "uptime_seconds": 34
  }
}
```

---

## GET /health/ready

Readiness. Confirms the dependencies required to serve traffic are reachable.
Use this for readiness probes and as a post-deploy smoke check.

```bash
curl http://localhost:8080/health/ready
```

**200 OK**

```json
{
  "success": true,
  "message": "ready",
  "data": {
    "database": "up",
    "storage_driver": "local",
    "ai_service": { "configured": false },
    "internal_routes_authenticated": true
  }
}
```

| Field | Meaning |
|---|---|
| `database` | `up`, or `down: <reason>` |
| `storage_driver` | `supabase` or `local` |
| `ai_service.configured` | Whether `AI_SERVICE_URL` is set. `false` disables policy matchmaking and the F4 claim generator. |
| `internal_routes_authenticated` | Whether `INTERNAL_API_KEY` is set. `false` means `/api/v1/internal/*` accepts requests without an `X-Internal-Key` header. |

**503 SERVICE_UNAVAILABLE** when the database ping fails. The same payload is
returned under `error.details` so you can see which dependency is at fault:

```json
{
  "success": false,
  "message": "database is not reachable",
  "error": {
    "code": "SERVICE_UNAVAILABLE",
    "details": {
      "database": "down: dial tcp 127.0.0.1:5432: connect: connection refused",
      "storage_driver": "supabase",
      "ai_service": { "configured": true },
      "internal_routes_authenticated": true
    }
  }
}
```

The database check has a 5-second timeout.
