"""Validate every PromQL expression that lives in the provisioned dashboards.

Reads the dashboard JSON files, substitutes the Grafana-only variables the same
way Grafana would, and asks Prometheus to evaluate each query. Run it after the
monitoring stack is up:

    python deployments/monitoring/validate_dashboards.py
    PROMETHEUS_URL=http://host:9090 python deployments/monitoring/validate_dashboards.py
"""

import json
import os
import pathlib
import urllib.error
import urllib.parse
import urllib.request

PROMETHEUS_URL = os.environ.get("PROMETHEUS_URL", "http://127.0.0.1:9090")
DASHBOARDS = pathlib.Path(__file__).resolve().parent / "grafana" / "dashboards"


def grafana_substitutions(expr: str) -> str:
    """Replace the variables Grafana expands at render time."""
    return (
        expr.replace("$__rate_interval", "1m")
        .replace("$__range", "5m")
        .replace("$route", ".*")
    )


def evaluate(expr: str) -> tuple[str, str]:
    url = f"{PROMETHEUS_URL}/api/v1/query?" + urllib.parse.urlencode({"query": expr})
    try:
        with urllib.request.urlopen(url, timeout=15) as response:
            payload = json.load(response)
    except urllib.error.URLError as err:
        return "UNREACHABLE", str(err.reason)

    if payload["status"] != "success":
        return "ERROR", payload.get("error", "")
    result = payload["data"]["result"]
    if not result:
        return "EMPTY", "no series"
    return "OK", f"{len(result)} series"


def main() -> None:
    failures = 0
    for path in sorted(DASHBOARDS.glob("*.json")):
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        print(f"\n{path.name}  ({dashboard['title']})")
        for panel in dashboard["panels"]:
            if panel.get("type") == "row":
                continue
            for target in panel.get("targets", []):
                expr = grafana_substitutions(target["expr"])
                status, detail = evaluate(expr)
                if status in {"ERROR", "UNREACHABLE"}:
                    failures += 1
                print(f"  [{status:11}] {panel['title'][:42]:44} {detail}")

    print(f"\nfailed queries: {failures}")
    raise SystemExit(1 if failures else 0)


if __name__ == "__main__":
    main()
