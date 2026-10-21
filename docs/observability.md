# Observability

Grantra sends traces, metrics and logs through [OpenTelemetry](https://opentelemetry.io) over OTLP/HTTP. It is off until an endpoint is set with the standard variables, so nothing changes for installs that don't use it:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
# optional, all standard: OTEL_EXPORTER_OTLP_HEADERS, OTEL_SERVICE_NAME,
# OTEL_RESOURCE_ATTRIBUTES, OTEL_TRACES_SAMPLER, OTEL_METRIC_EXPORT_INTERVAL...
```

With the Helm chart, put them under `extraEnv`. Logs keep going to stdout as JSON too.

## What is sent

| Signal | Contents |
|---|---|
| Traces | One span per HTTP request (health checks left out), per database query, and per background job pass (`job aggregate applications`, `job provision applications`...). Logs written during a span carry its trace id. |
| Logs | Every log line at or above `GRANTRA_LOG_LEVEL`. |
| Metrics | The standard HTTP server metrics, plus Grantra's own below. |

| Metric | Type | Attributes | Meaning |
|---|---|---|---|
| `grantra.job.runs` | counter | `job`, `outcome` (ok, error, skipped) | Background job passes; skipped means another replica held the job's lock |
| `grantra.job.duration` | histogram (s) | `job` | How long each pass took |
| `grantra.connector.runs` | counter | `operation` (aggregation, provisioning), `status` | Connector runs |
| `grantra.connector.changes` | counter | `operation`, `count` | The counts each run reports (accounts read, created, disabled...) |
| `grantra.lifecycle.events` | counter | | Joiner, mover and leaver events turned into access changes |
| `grantra.access.changes` | counter | `change` (added, removed) | Birthright access added or removed |
| `grantra.access_requests.pending` | gauge | | Requests waiting for a decision |
| `grantra.access_requests.closed` | counter | `outcome` (expired, rejected, escalated, cancelled, access_ended) | Requests closed without a decision (rejected when the approval flow says so), steps escalated to the approvers' managers, and access ended at its end date |
| `grantra.sod.violations` | gauge | `status` (open, excepted) | Separation of duties violations |
| `grantra.notifications` | counter | `result` (sent, retried, failed, skipped) | Notification emails |
| `grantra.notifications.queued` | gauge | | Emails waiting to be sent |

## Grafana dashboard

[`deploy/grafana/grantra-dashboard.json`](../deploy/grafana/grantra-dashboard.json) shows the backlog, syncs and provisioning, request flows, notifications, job health and API traffic. Import it in Grafana (Dashboards → New → Import) and pick a Prometheus data source that receives Grantra's metrics, for example through the OpenTelemetry Collector's Prometheus exporter or Prometheus' OTLP receiver. Metric names follow the usual OTLP to Prometheus translation (`grantra.job.runs` becomes `grantra_job_runs_total`).
