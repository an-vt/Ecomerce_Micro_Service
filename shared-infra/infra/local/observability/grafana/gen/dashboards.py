#!/usr/bin/env python3
"""Generates the Grafana dashboards of docs/14 §4 into ../dashboards/*.json.

The JSON is what Grafana provisions; this file is how it is maintained (a panel is one line here and sixty in JSON).
Run `make dashboards` after changing it. `scripts/check-observability.py` verifies that every metric used below is
really exported, so a dashboard cannot silently show "No data" because a name drifted.
"""
import json
import os
import pathlib

PROM = {"type": "prometheus", "uid": "prometheus"}
# DASHBOARDS_OUT lets `make check-infra` generate into a temporary directory and compare with what is committed
OUT = pathlib.Path(os.environ.get("DASHBOARDS_OUT") or pathlib.Path(__file__).resolve().parent.parent / "dashboards")

# error codes that mean "the service failed" (the other gRPC codes are normal answers: NotFound, InvalidArgument, ...)
FAILING = "Unavailable|Internal|Unknown|DeadlineExceeded|DataLoss|ResourceExhausted"
API = 'route!~"/health.*|/metrics|unmatched"'


class Board:
    """A dashboard whose panels flow left to right on a 24-column grid."""

    def __init__(self, uid, title, description, variables=None):
        self.uid, self.title, self.description = uid, title, description
        self.variables = variables or []
        self.panels, self._x, self._y, self._row_h, self._id = [], 0, 0, 0, 0

    def _place(self, w, h):
        if self._x + w > 24:
            self._x, self._y, self._row_h = 0, self._y + self._row_h, 0
        pos = {"x": self._x, "y": self._y, "w": w, "h": h}
        self._x += w
        self._row_h = max(self._row_h, h)
        return pos

    def _panel(self, kind, title, targets, w, h, unit=None, desc="", **extra):
        self._id += 1
        defaults = {"unit": unit} if unit else {}
        p = {"id": self._id, "type": kind, "title": title, "description": desc, "datasource": PROM, "gridPos": self._place(w, h),
             "targets": [{"refId": chr(65 + i), "datasource": PROM, "expr": e, "legendFormat": legend, **({"instant": True, "format": "table"} if extra.get("table") else {})}
                         for i, (e, legend) in enumerate(targets)],
             "fieldConfig": {"defaults": defaults, "overrides": []}, "options": {}}
        extra.pop("table", None)
        for k, v in extra.items():
            if k == "defaults":
                p["fieldConfig"]["defaults"].update(v)
            elif k == "overrides":
                p["fieldConfig"]["overrides"] = v
            else:
                p[k] = v
        self.panels.append(p)
        return p

    def row(self, title):
        self._x, self._y, self._row_h = 0, self._y + self._row_h, 0
        self._id += 1
        self.panels.append({"id": self._id, "type": "row", "title": title, "collapsed": False, "gridPos": {"x": 0, "y": self._y, "w": 24, "h": 1}, "panels": []})
        self._y += 1
        self._row_h = 0

    def ts(self, title, targets, unit=None, w=12, h=8, desc="", stack=False, **extra):
        if isinstance(targets, str):
            targets = [(targets, "")]
        custom = {"drawStyle": "line", "lineWidth": 1, "fillOpacity": 12 if stack else 0, "showPoints": "never", "spanNulls": False}
        if stack:
            custom["stacking"] = {"mode": "normal", "group": "A"}
        d = {"custom": custom, **extra.pop("defaults", {})}
        return self._panel("timeseries", title, targets, w, h, unit, desc, defaults=d,
                           options={"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["lastNotNull", "max"]}, "tooltip": {"mode": "multi", "sort": "desc"}}, **extra)

    def stat(self, title, expr, unit=None, w=4, h=4, desc="", thresholds=None, color_mode="value", **extra):
        steps = thresholds or [{"color": "green", "value": None}]
        return self._panel("stat", title, [(expr, "")], w, h, unit, desc,
                           defaults={"thresholds": {"mode": "absolute", "steps": steps}, "noValue": "0"},
                           options={"colorMode": color_mode, "graphMode": "area", "reduceOptions": {"calcs": ["lastNotNull"]}}, **extra)

    def bars(self, title, expr, legend, unit=None, w=12, h=8, desc=""):
        return self._panel("bargauge", title, [(expr, legend)], w, h, unit, desc,
                           defaults={"color": {"mode": "palette-classic"}, "noValue": "none yet"},
                           options={"displayMode": "gradient", "orientation": "horizontal", "reduceOptions": {"calcs": ["lastNotNull"]}, "showUnfilled": True})

    def table(self, title, expr, w=12, h=8, desc="", **extra):
        return self._panel("table", title, [(expr, "")], w, h, None, desc, table=True,
                           options={"showHeader": True}, transformations=[{"id": "organize", "options": {"excludeByName": {"Time": True, "__name__": True, "job": True}}}], **extra)

    def json(self):
        return {"uid": self.uid, "title": self.title, "description": self.description, "tags": ["ecom"], "schemaVersion": 39, "version": 1, "editable": True,
                "graphTooltip": 1, "refresh": "10s", "time": {"from": "now-1h", "to": "now"}, "timezone": "browser",
                "templating": {"list": self.variables}, "annotations": {"list": []}, "links": [], "panels": self.panels}


def service_var():
    return {"name": "service", "label": "service", "type": "query", "datasource": PROM, "query": {"query": 'label_values(up{job="ecom-services"}, service)', "refId": "svc"},
            "refresh": 2, "includeAll": True, "multi": True, "current": {"text": "All", "value": "$__all"}, "allValue": ".+", "sort": 1}


def q(p, expr):  # histogram_quantile over a bucket rate
    return f"histogram_quantile({p}, {expr})"


# ---- 1. services overview ---------------------------------------------------------------------------------------------
def services():
    b = Board("ecom-services", "Services overview", "Rate, errors and latency of the API and of every service (docs/14 §4)", [service_var()])
    b.row("Availability")
    b.stat("Services up", 'sum(up{job="ecom-services"})', w=4, thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 9}], color_mode="background")
    b.stat("Services down", 'count(up{job="ecom-services"} == 0) or vector(0)', w=4, thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}], color_mode="background")
    b.table("Targets", 'up{job="ecom-services"}', w=16, h=4, desc="1 = scraped, 0 = down",
            overrides=[{"matcher": {"id": "byName", "options": "Value"}, "properties": [{"id": "mappings", "value": [{"type": "value", "options": {"0": {"text": "DOWN", "color": "red"}, "1": {"text": "up", "color": "green"}}}]},
                                                                                 {"id": "custom.cellOptions", "value": {"type": "color-background"}}]}])
    b.row("REST API (api-gateway)")
    b.ts("Requests per second by status", f'sum by (status) (rate(http_server_duration_seconds_count{{{API}}}[1m]))', "reqps", stack=True)
    b.ts("5xx ratio", f'sum(rate(http_server_duration_seconds_count{{{API},status=~"5.."}}[5m])) / sum(rate(http_server_duration_seconds_count{{{API}}}[5m]))', "percentunit",
         desc="Alert GatewayHighErrorRate fires above 5 % for 5 minutes")
    b.ts("Latency p50 / p95 / p99", [(q(p, f'sum by (le) (rate(http_server_duration_seconds_bucket{{{API}}}[5m]))'), f"p{int(p*100)}") for p in (0.5, 0.95, 0.99)], "s")
    b.ts("Slowest routes (p95)", f'topk(8, {q(0.95, f"sum by (le, route) (rate(http_server_duration_seconds_bucket{{{API}}}[5m]))")})', "s", desc="Alert GatewayHighLatency fires above 1 s")
    b.row("gRPC between services")
    b.ts("Calls per second by service", 'sum by (service) (rate(grpc_server_duration_seconds_count{service=~"$service"}[1m]))', "reqps", stack=True)
    b.ts("Failing calls ratio by service", f'sum by (service) (rate(grpc_server_duration_seconds_count{{service=~"$service",code=~"{FAILING}"}}[5m])) / sum by (service) (rate(grpc_server_duration_seconds_count{{service=~"$service"}}[5m]))',
         "percentunit", desc="Only codes that mean the service failed. NotFound, InvalidArgument, FailedPrecondition... are normal answers")
    b.ts("Latency p95 by service", q(0.95, 'sum by (le, service) (rate(grpc_server_duration_seconds_bucket{service=~"$service"}[5m]))'), "s")
    b.ts("Answers by code", 'sum by (code) (rate(grpc_server_duration_seconds_count{service=~"$service"}[5m]))', "reqps", stack=True)
    b.row("Resources")
    b.ts("CPU (cores)", 'rate(process_cpu_seconds_total{job="ecom-services",service=~"$service"}[1m])', "short", w=8)
    b.ts("Memory (resident)", 'process_resident_memory_bytes{job="ecom-services",service=~"$service"}', "bytes", w=8)
    b.ts("Goroutines", 'go_goroutines{job="ecom-services",service=~"$service"}', "short", w=8, desc="A steady climb is a leak")
    return b


# ---- 2. circuit breakers ----------------------------------------------------------------------------------------------
def breakers():
    b = Board("ecom-breakers", "Circuit breakers", "State and traffic of every circuit breaker (docs/09, docs/14 §4)")
    b.row("State")
    b.stat("Open circuits", "count(circuit_breaker_state == 2) or vector(0)", w=4, thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}], color_mode="background")
    b.stat("Half-open", "count(circuit_breaker_state == 1) or vector(0)", w=4, thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 1}], color_mode="background")
    b.table("Breakers", "circuit_breaker_state", w=16, h=4, desc="0 closed, 1 half-open, 2 open. Seen from the service that holds the breaker",
            overrides=[{"matcher": {"id": "byName", "options": "Value"}, "properties": [
                {"id": "mappings", "value": [{"type": "value", "options": {"0": {"text": "closed", "color": "green"}, "1": {"text": "half-open", "color": "orange"}, "2": {"text": "OPEN", "color": "red"}}}]},
                {"id": "custom.cellOptions", "value": {"type": "color-background"}}]}])
    b.row("Traffic through the breakers")
    b.ts("Calls by result", "sum by (target, method, event) (rate(circuit_breaker_events_total[1m]))", "ops", desc="success / failure / reject: reject = failed fast because the circuit was open")
    b.ts("Fast-failed calls (open circuit)", 'sum by (service, target, method) (rate(circuit_breaker_events_total{event="reject"}[1m]))', "ops")
    b.ts("Failures", 'sum by (service, target, method) (rate(circuit_breaker_events_total{event="failure"}[1m]))', "ops", desc="Only infrastructure failures count; business answers never trip a breaker")
    b.ts("Client latency p95 by target", q(0.95, "sum by (le, target) (rate(grpc_client_duration_seconds_bucket[5m]))"), "s")
    b.ts("Client calls by target and code", "sum by (target, code) (rate(grpc_client_duration_seconds_count[1m]))", "reqps", stack=True)
    return b


# ---- 3. checkout saga -------------------------------------------------------------------------------------------------
def saga():
    b = Board("ecom-saga", "Checkout saga", "Orders from PENDING to CONFIRMED / CANCELLED (docs/06, docs/14 §4)")
    b.row("Outcome (last hour)")
    b.stat("Started", "sum(increase(saga_started_total[1h]))", w=4)
    b.stat("Confirmed", "sum(increase(saga_completed_total[1h]))", w=4, thresholds=[{"color": "green", "value": None}])
    b.stat("Cancelled", "sum(increase(saga_failed_total[1h]))", w=4, thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 1}])
    b.stat("Confirmed share", "sum(increase(saga_completed_total[1h])) / (sum(increase(saga_completed_total[1h])) + sum(increase(saga_failed_total[1h])))", "percentunit", w=4,
           thresholds=[{"color": "red", "value": None}, {"color": "orange", "value": 0.6}, {"color": "green", "value": 0.8}])
    b.stat("In flight", "sum(saga_started_total) - sum(saga_completed_total) - sum(saga_failed_total)", w=4, desc="Started minus finished since the service started: approximate across restarts")
    b.stat("Stale replies (1h)", "sum(increase(saga_stale_replies_total[1h]))", w=4, desc="Replies that arrived after the saga moved on: harmless, but a rising number points at slow participants")
    b.row("Flow")
    b.ts("Rate: started / confirmed / cancelled", [("sum(rate(saga_started_total[5m]))", "started"), ("sum(rate(saga_completed_total[5m]))", "confirmed"), ("sum(rate(saga_failed_total[5m]))", "cancelled")], "ops")
    b.bars("Why orders are cancelled", "sum by (reason) (increase(saga_failed_total[$__range]))", "{{reason}}", desc="CARD_DECLINED, INSUFFICIENT_STOCK, DISCOUNT_*, PROVIDER_UNAVAILABLE, STEP_TIMEOUT")
    b.ts("Duration p50 / p95 by outcome", [(q(p, "sum by (le, outcome) (rate(saga_duration_seconds_bucket[5m]))"), f"p{int(p*100)} {{{{outcome}}}}") for p in (0.5, 0.95)], "s")
    b.row("Trouble")
    b.ts("Compensations sent by command", "sum by (step) (rate(saga_compensations_total[5m]))", "ops", w=8, desc="What had to be undone, and how often it was re-sent")
    b.ts("Step timeouts by step", "sum by (step) (increase(saga_step_timeouts_total[10m]))", "short", w=8, desc="Alert SagaStuck fires above 5 in 10 minutes")
    b.ts("Stale replies", "sum(rate(saga_stale_replies_total[5m]))", "ops", w=8)
    return b


# ---- 4. kafka & outbox ------------------------------------------------------------------------------------------------
def kafka():
    b = Board("ecom-kafka", "Kafka & outbox", "Consumer lag, consumption results, dead letters and the outbox of every service (docs/08, docs/14 §4)")
    b.row("Broker")
    b.stat("Brokers", "kafka_brokers", w=4, thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}], color_mode="background")
    b.stat("Dead-lettered (1h)", "sum(increase(kafka_dlq_total[1h]))", w=4, thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}], color_mode="background", desc="Each one needs a human: Kafka UI -> <topic>.dlq")
    b.stat("Total lag", "sum(kafka_consumergroup_lag)", w=4, thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 100}, {"color": "red", "value": 1000}])
    b.ts("Group members", "sum by (consumergroup) (kafka_consumergroup_members)", "short", w=12, h=4)
    b.row("Consumers")
    b.ts("Consumer lag", "sum by (consumergroup, topic) (kafka_consumergroup_lag)", "short", desc="Alert ConsumerLagHigh fires above 1000 for 5 minutes")
    b.ts("Messages consumed by result", "sum by (topic, result) (rate(kafka_messages_consumed_total[1m]))", "ops", stack=True, desc="ok / duplicate / retry / dlq")
    b.ts("Retries", 'sum by (topic) (rate(kafka_messages_consumed_total{result="retry"}[5m]))', "ops", desc="A handler failing and being retried with backoff")
    b.ts("Dead letters by topic", "sum by (topic) (increase(kafka_dlq_total[1h]))", "short")
    b.row("Outbox (events and saga commands waiting to be published)")
    b.ts("Pending rows", "outbox_pending", "short", desc="Per service. Should hover near zero")
    b.ts("Oldest pending row", "outbox_oldest_age_seconds", "s", desc="Alert OutboxBacklog fires above 60 s: checkouts will time out")
    b.ts("Published per second", "sum by (service, topic) (rate(outbox_published_total[1m]))", "ops")
    b.ts("Publish errors", "sum by (service, topic) (rate(outbox_publish_errors_total[5m]))", "ops")
    return b


# ---- 5. databases -----------------------------------------------------------------------------------------------------
def databases():
    b = Board("ecom-databases", "Databases", "PostgreSQL, MongoDB and Redis (docs/14 §4)")
    b.row("PostgreSQL")
    b.ts("Connections by database", 'sum by (datname) (pg_stat_database_numbackends{datname=~".+_db"})', "short", w=8)
    b.ts("Transactions per second", 'sum by (datname) (rate(pg_stat_database_xact_commit{datname=~".+_db"}[1m]) + rate(pg_stat_database_xact_rollback{datname=~".+_db"}[1m]))', "ops", w=8)
    b.ts("Rollbacks per second", 'sum by (datname) (rate(pg_stat_database_xact_rollback{datname=~".+_db"}[1m]))', "ops", w=8, desc="Business refusals do not roll back; a rise points at errors")
    b.ts("Locks by mode", "sum by (mode) (pg_locks_count)", "short", w=8)
    b.ts("Deadlocks", 'sum by (datname) (increase(pg_stat_database_deadlocks{datname=~".+_db"}[5m]))', "short", w=8, desc="The saga takes row locks in a fixed order precisely to keep this at zero")
    b.ts("Cache hit ratio", "sum(pg_stat_database_blks_hit) / (sum(pg_stat_database_blks_hit) + sum(pg_stat_database_blks_read))", "percentunit", w=8)
    b.ts("Longest open transaction", 'max by (datname) (pg_stat_activity_max_tx_duration{state="active"})', "s", w=24, h=6, desc="A transaction held open by a slow call blocks the rows it locked")
    b.row("MongoDB")
    b.ts("Connections", 'mongodb_ss_connections{conn_type="current"}', "short", w=8)
    b.ts("Operations per second", "sum by (legacy_op_type) (rate(mongodb_ss_opcounters[1m]))", "ops", w=8, stack=True)
    b.stat("Replica set member state", "mongodb_mongod_replset_my_state", w=8, h=8, desc="1 = PRIMARY. Transactions need a primary",
           thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}], color_mode="background")
    b.row("Redis")
    b.ts("Memory used", "redis_memory_used_bytes", "bytes", w=6)
    b.ts("Connected clients", "redis_connected_clients", "short", w=6)
    b.ts("Hit ratio", "rate(redis_keyspace_hits_total[5m]) / (rate(redis_keyspace_hits_total[5m]) + rate(redis_keyspace_misses_total[5m]))", "percentunit", w=6)
    b.ts("Commands per second", "rate(redis_commands_processed_total[1m])", "ops", w=6)
    return b


# ---- 6. business ------------------------------------------------------------------------------------------------------
def calls(service, method, window="$__interval", code="OK"):
    return f'sum(increase(grpc_server_duration_seconds_count{{service="{service}",method="{method}",code="{code}"}}[{window}]))'


def business():
    b = Board("ecom-business", "Business", "What people are doing: sign-ups, logins, catalogue, orders and payments (docs/14 §4). Derived from the call counts of the services, so it needs no extra instrumentation")
    b.row("Orders and payments (last hour)")
    b.stat("Orders placed", calls("order-service", "PlaceOrder", "1h"), w=4)
    b.stat("Confirmed", "sum(increase(saga_completed_total[1h]))", w=4)
    b.stat("Cancelled", "sum(increase(saga_failed_total[1h]))", w=4, thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 1}])
    b.stat("Cards declined", 'sum(increase(saga_failed_total{reason="CARD_DECLINED"}[1h]))', w=4)
    b.stat("Payment provider trouble", 'sum(increase(saga_failed_total{reason="PROVIDER_UNAVAILABLE"}[1h]))', w=4, thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}])
    b.stat("Out of stock", 'sum(increase(saga_failed_total{reason="INSUFFICIENT_STOCK"}[1h]))', w=4)
    b.ts("Orders by outcome", [("sum(increase(saga_completed_total[$__interval]))", "confirmed"), ("sum(increase(saga_failed_total[$__interval]))", "cancelled"), (calls("order-service", "PlaceOrder"), "placed")], "short")
    b.bars("Cancelled orders by reason", "sum by (reason) (increase(saga_failed_total[$__range]))", "{{reason}}")
    b.row("Accounts")
    b.ts("Sign-ups", [(calls("auth-service", "SignUpShop"), "shops"), (calls("auth-service", "VerifyEmail"), "buyers (email verified)")], "short", w=8)
    b.ts("Logins by answer", 'sum by (code) (increase(grpc_server_duration_seconds_count{service="auth-service",method="Login"}[$__interval]))', "short", w=8, stack=True, desc="OK vs Unauthenticated (wrong credentials)")
    b.ts("Refresh token reuse", "sum(increase(auth_refresh_reuse_detected_total[$__interval]))", "short", w=8, desc="Alert RefreshTokenReuse fires above 3 in 5 minutes")
    b.row("Catalogue and shopping")
    b.ts("Products created / published", [(calls("catalog-service", "CreateProduct"), "created"), (calls("catalog-service", "PublishProduct"), "published")], "short", w=8)
    b.ts("Carts", [(calls("cart-service", "AddToCart"), "added to cart"), (calls("cart-service", "UpdateCart"), "cart updates")], "short", w=8)
    b.ts("Stock added by shops", calls("inventory-service", "AddStock"), "short", w=8)
    b.row("Discounts and notifications")
    b.ts("Discounts created", calls("discount-service", "CreateDiscount"), "short", w=8)
    b.ts("Code previews by answer", 'sum by (code) (increase(grpc_server_duration_seconds_count{service="discount-service",method="CalculateDiscount"}[$__interval]))', "short", w=8, stack=True,
         desc="FailedPrecondition = the code was refused (expired, used up, below the minimum)")
    b.ts("Notification lists read", calls("notification-service", "ListNotifications"), "short", w=8)
    b.row("Protection")
    b.ts("Requests refused by the rate limiter", "sum by (route) (increase(rate_limited_total[$__interval]))", "short", w=24, h=6)
    return b


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    for build in (services, breakers, saga, kafka, databases, business):
        board = build()
        path = OUT / f"{board.uid.removeprefix('ecom-')}.json"
        path.write_text(json.dumps(board.json(), indent=2, sort_keys=False) + "\n")
        print("wrote", path)


if __name__ == "__main__":
    main()
