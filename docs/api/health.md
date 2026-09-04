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
    "ai_service": { "configured": true, "reachable": true }
  }
}
```

| Field | Meaning |
|---|---|
| `database` | `up`, or `down: <reason>` |
| `storage_driver` | `supabase` or `local` |
| `ai_service.configured` | Whether `AI_SERVICE_URL` is set. `false` disables policy matchmaking and every AI-powered utility. |
| `ai_service.reachable` | Whether the AI service answered `GET /health`. Present only when `configured` is `true`. |
| `ai_service.error` | Present only when `reachable` is `false` — why the probe failed. |

`configured` and `reachable` are separate on purpose, because they fail for
different reasons and are fixed in different places: a URL being set says nothing
about anything listening on it.

```json
{ "ai_service": { "configured": true, "reachable": false, "error": "call AI service: dial tcp 10.0.3.7:8000: connect: connection refused" } }
```

**An unreachable AI service does not fail readiness.** The backend serves all
read endpoints in full without it — every claim read is a plain database query
— so taking pods out of rotation over a dead AI service would turn a partial
degradation into a full outage. Only the write-through flows degrade, and each
returns its own `503` saying so.

The AI probe has its own 2-second timeout, well inside the endpoint's 5-second
budget, so a slow AI service cannot slow down the readiness check.

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
      "ai_service": { "configured": true, "reachable": true }
    }
  }
}
```

The database check has a 5-second timeout; the AI reachability check has 2.
