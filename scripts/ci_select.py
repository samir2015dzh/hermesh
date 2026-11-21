#!/usr/bin/env python3
"""What a pull request's Linux line runs (work order #33): its unit tests,
whether the end-to-end suite runs, and which of its binaries are built with
the race detector.

    ci_select.py --base origin/main [--event pull_request]

Anything but a pull request runs everything (main's pushes, the daily run, a
run by hand). A pull request runs everything too when it touches a file in
FULL, or a file this script cannot place; otherwise it runs the unit tests of
the packages its changes reach (Go's dependency graph, tests' imports
included), the end-to-end suite when a binary it builds or a file it reads is
changed, and the race detector on the binaries the changes reach. When in
doubt it runs more, never less.

It prints what it chose and why, and writes for the workflow, in
$GITHUB_OUTPUT: mode (full, selective, docs), unit (packages, or empty),
e2e (true/false), race (AIDE_E2E_RACE: 1 for all, a comma list, or empty);
and the same report to $GITHUB_STEP_SUMMARY.
"""

import argparse
import os
import subprocess
import sys

# Files and folders whose change runs everything: the module, the build and CI,
# the scripts (the installers e2e runs, the release key a unit test reads, this
# one), the relay image's context, the protocol and its generated code.
FULL_FILES = {"go.mod", "go.sum", "Makefile", ".dockerignore",
              "buf.yaml", "buf.gen.yaml", "buf.gen.ts.yaml"}
FULL_PREFIXES = (".github/", "scripts/", "deploy/", "proto/", "gen/")
# The shared core: packages that half or more of cmd/, internal/ and pkg/
# depend on (go list, 2026-11-22). The dependency graph already reaches their
# dependents; a change here runs everything all the same.
CORE = {"internal/legacy", "internal/brandenv", "internal/shellenv",
        "internal/spawn", "internal/provider", "pkg/metrics"}
# No test of the Linux line reads these.
DOCS_PREFIXES = ("docs/", "assets/")
DOCS_FILES = {"LICENSE"}
ANDROID_PREFIXES = ("apps/android/",)  # android.yml builds and tests it
# Read by the end-to-end suite (the desktop app, the DSH bridge, Her), not by
# Go's unit tests: the suite runs, without race on their account.
E2E_READS_PREFIXES = ("apps/", "packages/", "her/")
E2E_READS_FILES = {"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml",
                   "tsconfig.base.json"}
# The binaries e2e/harness_test.go builds; AIDE_E2E_RACE names them.
BINARIES = ["./cmd/aide-relay", "./cmd/aided", "./cmd/aide",
            "./internal/provider/acp/fakeacp", "./bench/fakecodex"]
UNIT_ROOTS = ("cmd/", "internal/", "pkg/")  # make test's packages


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def module():
    for line in open("go.mod", encoding="utf-8"):
        if line.startswith("module "):
            return line.split()[1]
    sys.exit("no module line in go.mod")


def packages(mod):
    """Every package of the module: its directory (relative) -> import path."""
    dirs = {}
    root = os.getcwd()
    for line in run("go", "list", "-f", "{{.ImportPath}}|{{.Dir}}", "./...").splitlines():
        ip, d = line.split("|", 1)
        rel = os.path.relpath(d, root).replace(os.sep, "/")
        dirs[rel] = ip
    return dirs


def deps(mod, pkgs, test):
    """Import path -> the module's packages it depends on, itself included
    (with its tests' imports when test)."""
    args = ["go", "list"] + (["-test"] if test else []) + ["-f", "{{.ImportPath}}|{{.ForTest}}|{{join .Deps \" \"}}"] + pkgs
    out = {}
    for line in run(*args).splitlines():
        ip, for_test, ds = line.split("|", 2)
        base = for_test or ip.split(" ")[0]
        if base.endswith(".test"):
            base = base[: -len(".test")]
        s = out.setdefault(base, {base})
        s.update(d.split(" ")[0] for d in ds.split() if d.startswith(mod + "/") or d == mod)
    return out


def package_of(path, dirs):
    """The package whose folder holds path (the nearest one up), or None."""
    d = os.path.dirname(path)
    while d:
        if d in dirs:
            return d
        d = os.path.dirname(d)
    return None


def choose(files, mod, dirs):
    report = []
    full = []
    changed = set()       # packages whose code changed
    tests_only = set()    # packages whose _test.go files alone changed
    e2e_reads = []
    e2e_code = False
    quiet = []            # docs and Android: nothing on this line reads them
    for f in files:
        pkg = package_of(f, dirs)
        if f in FULL_FILES or f.startswith(FULL_PREFIXES):
            full.append(f + " (build, CI, scripts or protocol)")
        elif pkg in CORE:
            full.append(f + " (shared core package %s)" % pkg)
        elif f.startswith("e2e/"):
            e2e_code = True
        elif pkg is not None:
            (tests_only if f.endswith("_test.go") else changed).add(pkg)
        elif f.endswith(".go"):
            full.append(f + " (a Go file in no package: moved or deleted)")
        elif f.startswith(DOCS_PREFIXES) or f in DOCS_FILES or f.endswith(".md"):
            quiet.append(f)
        elif f.startswith(ANDROID_PREFIXES):
            quiet.append(f)
        elif f.startswith(E2E_READS_PREFIXES) or f in E2E_READS_FILES:
            e2e_reads.append(f)
        else:
            full.append(f + " (not placed: run everything)")
    tests_only -= changed

    if full:
        report += ["Everything runs, for:"] + ["  " + x for x in full]
        return "full", None, True, "1", report
    if not (changed or tests_only or e2e_reads or e2e_code):
        report += ["Only docs or the Android app changed: the Go tests and the end-to-end suite are skipped."]
        report += ["  " + f for f in quiet]
        return "docs", [], False, "", report

    changed_ip = {dirs[d] for d in changed}
    tests_ip = {dirs[d] for d in tests_only}
    unit_all = run("go", "list", "./cmd/...", "./internal/...", "./pkg/...").split()
    graph = deps(mod, unit_all, test=True)
    unit = sorted(p for p in unit_all if graph.get(p, {p}) & changed_ip or p in tests_ip)

    bins = deps(mod, BINARIES, test=False)
    e2e_pkg = deps(mod, ["./e2e"], test=True)
    names = {b: b.rsplit("/", 1)[1] for b in BINARIES}
    race = [names[b] for b in BINARIES if bins.get(mod + b[1:], set()) & changed_ip]
    e2e_deps = set().union(*bins.values(), *e2e_pkg.values())
    e2e = bool(e2e_code or e2e_reads or e2e_deps & changed_ip)
    if e2e_code:
        race = list(names.values())  # the harness itself changed: all of them

    report.append("Packages changed: " + (", ".join(sorted(changed)) or "none"))
    if tests_only:
        report.append("Tests changed alone (their own package's tests run): " + ", ".join(sorted(tests_only)))
    if e2e_code:
        report.append("The end-to-end suite's own code changed.")
    if e2e_reads:
        report.append("Files the end-to-end suite reads changed: " + ", ".join(e2e_reads))
    if quiet:
        report.append("Not read by this line (docs, Android): " + ", ".join(quiet))
    skipped = [p for p in unit_all if p not in unit]
    report.append("Unit tests run (%d): %s" % (len(unit), " ".join(p[len(mod) + 1:] for p in unit) or "none"))
    report.append("Unit tests skipped (%d): %s" % (len(skipped), " ".join(p[len(mod) + 1:] for p in skipped) or "none"))
    report.append("End-to-end suite: " + ("runs, every shard" if e2e else "skipped (no binary it builds, no file it reads, changed)"))
    if e2e:
        report.append("Race detector on: " + (", ".join(race) or "none (no binary's code changed); main's push and the daily run race them all"))
    race_env = "1" if len(race) == len(BINARIES) else ",".join(race)
    return "selective", unit, e2e, race_env, report


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="the ref the pull request merges into, e.g. origin/main")
    ap.add_argument("--event", default=os.environ.get("GITHUB_EVENT_NAME", "pull_request"))
    a = ap.parse_args()

    mod = module()
    if a.event != "pull_request":
        mode, unit, e2e, race = "full", None, True, "1"
        report = ["%s: everything runs, the race detector on every binary." % a.event]
    else:
        files = run("git", "diff", "--name-only", "--no-renames", a.base + "...HEAD").split()
        mode, unit, e2e, race, report = choose(files, mod, packages(mod))
        report.insert(0, "Files changed against %s: %d" % (a.base, len(files)))

    unit_out = "./cmd/... ./internal/... ./pkg/..." if unit is None else " ".join(unit)
    report.insert(0, "Mode: " + mode)
    print("\n".join(report))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as o:
            o.write("mode=%s\nunit=%s\ne2e=%s\nrace=%s\n" % (mode, unit_out, "true" if e2e else "false", race))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as o:
            o.write("### What this run tests (scripts/ci_select.py)\n```\n" + "\n".join(report) + "\n```\n")


if __name__ == "__main__":
    main()
