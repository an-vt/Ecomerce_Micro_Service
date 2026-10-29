import importlib.util
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("co", pathlib.Path(__file__).with_name("check-observability.py"))
co = importlib.util.module_from_spec(spec)
spec.loader.exec_module(co)


class MetricNames(unittest.TestCase):
    def names(self, expr):
        return sorted(set(co.metric_names(expr)))

    def test_functions_labels_and_ranges_are_not_metrics(self):
        self.assertEqual(self.names('sum by (route) (rate(http_server_duration_seconds_count{status=~"5..",route!~"/health.*"}[5m]))'), ["http_server_duration_seconds_count"])

    def test_two_metrics_in_a_ratio(self):
        self.assertEqual(self.names("sum(rate(saga_failed_total[15m])) / sum(rate(saga_started_total[15m])) > 0.2"), ["saga_failed_total", "saga_started_total"])

    def test_histogram_quantile_and_grafana_variables(self):
        self.assertEqual(self.names('histogram_quantile(0.95, sum by (le, service) (rate(grpc_server_duration_seconds_bucket{service=~"$service"}[$__interval])))'),
                         ["grpc_server_duration_seconds_bucket"])

    def test_vector_fallback_and_comparisons(self):
        self.assertEqual(self.names('count(up{job="ecom-services"} == 0) or vector(0)'), ["up"])

    def test_without_and_on_clauses(self):
        self.assertEqual(self.names("a_total / on (x) group_left (y) b_total"), ["a_total", "b_total"])


class Known(unittest.TestCase):
    def test_service_exporter_and_builtin_metrics(self):
        s = {"saga_started_total", "saga_duration_seconds"}
        self.assertTrue(co.known("saga_started_total", s, None))
        self.assertTrue(co.known("saga_duration_seconds_bucket", s, None), "histograms expose _bucket")
        self.assertTrue(co.known("up", s, None))
        self.assertTrue(co.known("go_goroutines", s, None))
        self.assertTrue(co.known("pg_locks_count", s, None), "exporters: by prefix offline")
        self.assertFalse(co.known("pg_locks_count", s, {"pg_other"}), "...and by exact name when Prometheus answers")
        self.assertFalse(co.known("payments_total", s, None), "a metric that is only in a document")
        self.assertFalse(co.known("saga_started_totall", s, None), "a typo")


class Files(unittest.TestCase):
    def test_alert_expressions_single_line_and_block(self):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / "a.yml"
            p.write_text("groups:\n  - name: x\n    rules:\n      - alert: One\n        expr: up == 0\n        for: 1m\n"
                         "      - alert: Two\n        expr: |\n          sum(rate(a_total[5m]))\n            / sum(rate(b_total[5m])) > 0.05\n        for: 5m\n")
            self.assertEqual(co.alert_exprs(p), [("One", "up == 0"), ("Two", "sum(rate(a_total[5m])) / sum(rate(b_total[5m])) > 0.05")])

    def test_a_dashboard_with_an_unknown_metric_is_reported(self):
        with tempfile.TemporaryDirectory() as d:
            board = pathlib.Path(d) / "b.json"
            board.write_text('{"panels":[{"title":"P","targets":[{"expr":"rate(made_up_total[1m])"}]}]}')
            problems, checked = co.check([], [board], set(), None)
            self.assertEqual(checked, 1)
            self.assertIn("made_up_total", problems[0])
            self.assertIn("'P'", problems[0])

    def test_a_query_prometheus_rejects_is_reported(self):
        import http.server, threading, json as j

        class H(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                bad = "BAD" in self.path
                self.send_response(400 if bad else 200)
                self.end_headers()
                self.wfile.write(j.dumps({"status": "error", "error": "parse error: unexpected"} if bad else {"status": "success", "data": {"result": []}}).encode())

            def log_message(self, *a):
                pass

        import socketserver

        class Quick(http.server.HTTPServer):
            def server_bind(self):  # HTTPServer.server_bind does a reverse-DNS getfqdn(), which can take half a minute
                socketserver.TCPServer.server_bind(self)
                self.server_name, self.server_port = "localhost", self.socket.getsockname()[1]

        srv = Quick(("127.0.0.1", 0), H)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{srv.server_port}"
        self.assertIsNone(co.query_error(base, "up"))
        self.assertIsNone(co.query_error(base, "rate(up[$__interval])"), "variables are substituted")
        self.assertIn("parse error", co.query_error(base, "BAD(("))
        srv.shutdown()
        srv.server_close()  # the port now refuses connections
        self.assertIsNone(co.query_error(base, "up"), "an unreachable Prometheus is not a finding")

    def test_the_real_rules_and_dashboards_are_clean(self):
        rules = sorted((co.OBS / "prometheus/rules").glob("*.yml"))
        boards = sorted((co.OBS / "grafana/dashboards").glob("*.json"))
        problems, checked = co.check(rules, boards, co.service_metrics(), None)
        self.assertEqual(problems, [])
        self.assertGreater(checked, 100)


if __name__ == "__main__":
    unittest.main()
