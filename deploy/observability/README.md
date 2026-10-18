# Observability stand

Jaeger, Prometheus and Grafana in docker compose (profile `observability`) for
the skit-x commands running on the host.

```sh
make up-observability   # postgres and rabbitmq, plus jaeger, prometheus, grafana
OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 make run
make down-observability
```

The two `OTEL_*` variables can live in `.env` instead (see `.env.example`).
`make run-worker` and `make run-consumer` export the same way.

| what | where | |
|---|---|---|
| Jaeger | http://localhost:16686 | the traces of `skit-example`; OTLP/gRPC on `localhost:4317` |
| Prometheus | http://localhost:9091 | scrapes `/metrics` of `serve` (:6060), `worker` (:6061) and `consumer` (:6062) on the host; a command that is not running is a down target |
| Grafana | http://localhost:3000 | no login; the six skit dashboards in the folder `skit`; data sources Prometheus and Jaeger |

## From one signal to another

- **Metric → trace.** A duration histogram carries the trace of a sampled
  request as its exemplar. In Grafana turn on Exemplars in a panel or in
  Explore: a point opens its trace in Jaeger.
- **Log → trace.** A log record written in a request carries `trace_id`; look
  it up in Jaeger, or in Grafana Explore → Jaeger → TraceID.
- **Trace → log.** Search the terminal output for the trace id.

## Notes

- Prometheus reaches the host as `host.docker.internal`. Docker Desktop routes
  it to the host's loopback, where the debug servers listen
  (`localhost:6060`). On Linux it is the bridge gateway: run the commands with
  `DEBUG_ADDR=0.0.0.0:6060` (`6061`, `6062`).
- The dashboards are mounted from `../skit/metrics/dashboards`, the sibling
  skit checkout `go.mod` replaces the SDK with.
- Jaeger keeps traces in memory: `make down-observability` drops them.
- Jaeger is pinned to 2.20: the Jaeger data source of Grafana 11.6 lists
  services by `/api/services`, which Jaeger 2.21 no longer serves.
