#!/usr/bin/env python3
"""Fails when a dashboard panel or an alert rule uses a metric that nothing exports.

A typo, or a metric that was renamed (or only ever described in a document), makes a panel say "No data" and an alert never
fire; nobody notices until the day it matters. This check reads

  * the alert rules   shared-infra/infra/local/observability/prometheus/rules/*.yml
  * the dashboards    shared-infra/infra/local/observability/grafana/dashboards/*.json

extracts the metric names from every PromQL expression and compares them with

  * the metrics the services register (read from the Go source: prometheus.New* ... Name: "x"; a histogram also exposes
    _bucket, _sum and _count)
  * `up` and the Go / process collectors every service has
  * the exporters (pg_, mongodb_, redis_, kafka_): by prefix offline, and by exact name when Prometheus answers on
    $PROMETHEUS_URL (default http://127.0.0.1:9090)

When Prometheus answers, every expression is also EXECUTED (Grafana variables replaced by plausible values): a query that
Prometheus rejects (bad syntax, wrong label matcher, mismatched vector matching) is reported. An empty result is fine: a
counter that never moved has no series yet.

Usage: scripts/check-observability.py   (exit 1 with the list of problems)
"""
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
OBS = ROOT / "shared-infra/infra/local/observability"

EXPORTER_PREFIXES = ("pg_", "mongodb_", "redis_", "kafka_")
BUILTIN_PREFIXES = ("go_", "process_", "promhttp_")
BUILTIN_NAMES = {"up"}
SUFFIXES = ("_bucket", "_sum", "_count")

KEYWORDS = {"by", "without", "on", "ignoring", "group_left", "group_right", "and", "or", "unless", "bool", "offset", "inf", "nan"}


def service_metrics(root=ROOT):
    """Metric names registered by the services, from the Go source (test files excluded)."""
    names = set()
    for path in root.rglob("*.go"):
        if path.name.endswith("_test.go") or "node_modules" in path.parts or "gen" in path.parts:
            continue
        text = path.read_text(errors="ignore")
        if "prometheus." not in text:
            continue
        names.update(re.findall(r'Name:\s*"([a-z][a-z0-9_]*)"', text))
    return names


def alert_exprs(path):
    """(alert name, expression) pairs of a rules file. Handles `expr: x` and `expr: |` blocks; no YAML library needed."""
    out, alert, lines = [], None, path.read_text().splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        if m := re.match(r"\s*-?\s*alert:\s*(\S+)", line):
            alert = m.group(1)
        elif m := re.match(r"(\s*)expr:\s*(.*)$", line):
            indent, rest = len(m.group(1)), m.group(2).strip()
            if rest in ("|", ">", "|-", ">-"):
                body = []
                i += 1
                while i < len(lines) and (not lines[i].strip() or len(lines[i]) - len(lines[i].lstrip()) > indent):
                    body.append(lines[i].strip())
                    i += 1
                out.append((alert, " ".join(body)))
                continue
            out.append((alert, rest.strip("'\"")))
        i += 1
    return out


def dashboard_exprs(path):
    d = json.loads(path.read_text())
    out = [(p.get("title", "?"), t["expr"]) for p in d.get("panels", []) for t in p.get("targets", []) if t.get("expr")]
    for v in d.get("templating", {}).get("list", []):
        q = v.get("query")
        q = q.get("query") if isinstance(q, dict) else q
        if v.get("type") == "query" and q:
            out.append((f"variable {v.get('name')}", re.sub(r"^label_values\((.*),\s*\w+\)$", r"\1", q)))
    return out


def metric_names(expr):
    """The metric names in a PromQL expression."""
    s = re.sub(r"\$\{?[A-Za-z_]+\}?", "", expr)          # Grafana variables: $service, $__interval, $__range
    s = re.sub(r'"(?:\\.|[^"\\])*"', '""', s)             # string literals (label values, regexes)
    s = re.sub(r"`[^`]*`", '""', s)
    s = re.sub(r"\{[^{}]*\}", "", s)                      # label matchers
    s = re.sub(r"\[[^\]]*\]", "", s)                      # range selectors
    s = re.sub(r"\b(by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)", " ", s)
    found = []
    for m in re.finditer(r"[A-Za-z_:][A-Za-z0-9_:]*", s):
        word, rest = m.group(0), s[m.end():].lstrip()
        if word in KEYWORDS or rest.startswith("("):      # operators, and functions like rate( sum( histogram_quantile(
            continue
        found.append(word)
    return found


def known(name, services, live):
    base = name
    for suf in SUFFIXES:
        if name.endswith(suf):
            base = name[: -len(suf)]
            break
    if name in BUILTIN_NAMES or name.startswith(BUILTIN_PREFIXES):
        return True
    if name in services or base in services:
        return True
    if name.startswith(EXPORTER_PREFIXES):
        return name in live if live is not None else True
    return False


def live_names():
    url = os.environ.get("PROMETHEUS_URL", "http://127.0.0.1:9090") + "/api/v1/label/__name__/values"
    try:
        with urllib.request.urlopen(url, timeout=2) as r:
            return set(json.load(r)["data"])
    except Exception:
        return None


GRAFANA_VARS = {"$__interval": "1m", "$__rate_interval": "1m", "$__range": "1h", "$service": ".+", "${service}": ".+"}


def query_error(base, expr):
    """The error Prometheus gives for expr, or None. An empty result is not an error."""
    for var, value in GRAFANA_VARS.items():
        expr = expr.replace(var, value)
    url = base + "/api/v1/query?" + urllib.parse.urlencode({"query": expr})
    try:
        with urllib.request.urlopen(url, timeout=5) as r:
            body = json.load(r)
    except urllib.error.HTTPError as e:
        try:
            return json.load(e).get("error", str(e))
        except Exception:
            return str(e)
    except Exception:
        return None  # Prometheus went away: nothing to say
    return None if body.get("status") == "success" else body.get("error", "query failed")


def check(rule_files, dashboard_files, services, live, prometheus=None):
    problems, checked = [], 0
    sources = [(f.name, f"alert {a}", e) for f in rule_files for a, e in alert_exprs(f)] + \
              [(f.name, f"panel {t!r}", e) for f in dashboard_files for t, e in dashboard_exprs(f)]
    for file, what, expr in sources:
        for name in metric_names(expr):
            checked += 1
            if not known(name, services, live):
                problems.append(f"{file}: {what}: unknown metric {name!r}")
        if prometheus and (err := query_error(prometheus, expr)):
            problems.append(f"{file}: {what}: Prometheus rejects the query: {err}")
    return problems, checked


def main():
    rules = sorted((OBS / "prometheus/rules").glob("*.yml"))
    boards = sorted((OBS / "grafana/dashboards").glob("*.json"))
    if not rules or not boards:
        print("check-observability: no rules or no dashboards found", file=sys.stderr)
        return 1
    services, live = service_metrics(), live_names()
    prometheus = os.environ.get("PROMETHEUS_URL", "http://127.0.0.1:9090") if live is not None else None
    problems, checked = check(rules, boards, services, live, prometheus)
    mode = "exporter names verified and every query executed against the running Prometheus" if live is not None else "exporter names checked by prefix only (Prometheus not reachable)"
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print(f"check-observability: {len(problems)} problem(s) in {checked} metric references", file=sys.stderr)
        return 1
    print(f"check-observability: ok, {checked} metric references in {len(rules)} rule file(s) and {len(boards)} dashboard(s); {mode}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
