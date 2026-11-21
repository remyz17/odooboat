"""Odooboat helper (ADR 0008 §12).

Embedded in the odooboat binary and copied into every environment image at
/opt/odooboat/helper, never into base images, so its protocol always matches
the binary. Standard library only, written for the oldest Python version of
the matrix.

    python -I odooboat_helper.py check --series 18.0 --postgres-major 16

prints a JSON report and exits 1 when a check fails.

    python -I odooboat_helper.py ready --url http://127.0.0.1:8069/web/health

makes one request from inside the container and prints a JSON report; it
exits 1 when the server is not ready yet. The caller polls (ADR 0009 §5).
"""

import argparse
import importlib.util
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

PROTOCOL = 1
SOURCE_ROOT = "/opt/odoo"
COMMUNITY = os.path.join(SOURCE_ROOT, "community")

# Same reading as internal/source.ReadSeries: one top-level assignment.
VERSION_INFO = re.compile(
    r"""^version_info[ \t]*=[ \t]*\([ \t]*(\d+|'saas~\d+'|"saas~\d+")[ \t]*,[ \t]*(\d+)[ \t]*,""",
    re.MULTILINE,
)
PG_DUMP_VERSION = re.compile(r"\(PostgreSQL\) (\d+)(?:\.\d+)?")


def read_series(community):
    """Return the series of a community tree, or None if it has no release.py."""
    path = os.path.join(community, "odoo", "release.py")
    try:
        with open(path, encoding="utf-8") as handle:
            content = handle.read()
    except FileNotFoundError:
        return None
    matches = VERSION_INFO.findall(content)
    if len(matches) != 1:
        raise ValueError("%s: expected one version_info assignment, found %d" % (path, len(matches)))
    major, minor = matches[0]
    return "%s.%s" % (major.strip("'\""), minor)


def odoo_outside_sources():
    """Return where odoo is importable from outside the source root, if anywhere."""
    spec = importlib.util.find_spec("odoo")
    if spec is None:
        return None
    locations = list(spec.submodule_search_locations or []) or [spec.origin or ""]
    outside = [p for p in locations if not os.path.realpath(p).startswith(SOURCE_ROOT + os.sep)]
    return outside[0] if outside else None


def pg_dump_major():
    output = subprocess.run(["pg_dump", "--version"], check=True, capture_output=True, text=True).stdout
    match = PG_DUMP_VERSION.search(output)
    if not match:
        raise ValueError("unrecognised pg_dump version: %r" % output.strip())
    return int(match.group(1))


def check(series, postgres_major, community=COMMUNITY):
    report = {
        "protocol": PROTOCOL,
        "python": "%d.%d" % sys.version_info[:2],
        "problems": [],
    }
    problems = report["problems"]

    location = odoo_outside_sources()
    if location:
        problems.append("odoo is importable outside %s: %s" % (SOURCE_ROOT, location))

    try:
        report["postgresClient"] = pg_dump_major()
        if report["postgresClient"] != postgres_major:
            problems.append("pg_dump is PostgreSQL %d, expected %d" % (report["postgresClient"], postgres_major))
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        problems.append("pg_dump: %s" % error)

    # Sources are present only in embedded mode; mounted ones arrive at run time.
    try:
        detected = read_series(community)
    except ValueError as error:
        problems.append(str(error))
        detected = None
    if detected is not None:
        report["series"] = detected
        if detected != series:
            problems.append("community source is series %s, expected %s" % (detected, series))
    return report


def ready(url, timeout=5.0):
    """Report whether url answers 200. Never raises for a server not up yet."""
    report = {"protocol": PROTOCOL, "ready": False}
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            report["status"] = response.status
    except urllib.error.HTTPError as error:
        report["status"] = error.code
    except (urllib.error.URLError, OSError, ValueError) as error:
        report["error"] = str(getattr(error, "reason", error))
    report["ready"] = report.get("status") == 200
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(prog="odooboat_helper")
    commands = parser.add_subparsers(dest="command", required=True)
    check_parser = commands.add_parser("check", help="verify an environment image")
    check_parser.add_argument("--series", required=True)
    check_parser.add_argument("--postgres-major", type=int, required=True)
    ready_parser = commands.add_parser("ready", help="probe an HTTP endpoint once")
    ready_parser.add_argument("--url", required=True)
    ready_parser.add_argument("--timeout", type=float, default=5.0)
    args = parser.parse_args(argv)

    if args.command == "ready":
        report = ready(args.url, args.timeout)
        failed = not report["ready"]
    else:
        report = check(args.series, args.postgres_major)
        failed = bool(report["problems"])
    json.dump(report, sys.stdout, sort_keys=True)
    sys.stdout.write("\n")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
