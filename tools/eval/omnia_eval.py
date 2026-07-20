#!/usr/bin/env python3
"""
Omnia recall eval harness.

Runs against an ISOLATED data dir (OMNIA_DATA_DIR) so it never touches the
real ~/.omnia. Seeds a known corpus, runs recall scenarios through the omnia
CLI, and scores retrieval quality (hit@1, hit@3, MRR, latency).

Two recall surfaces are exercised:
  * `omnia search`     — general keyword/relevance recall (ranked list)
  * `omnia recall-fix` — signature-lane recall for recurring errors (proven fixes only)

Usage:
  OMNIA_DATA_DIR=$(mktemp -d) OMNIA_BIN=/path/to/omnia python3 tools/eval/omnia_eval.py

OMNIA_BIN defaults to "omnia" (resolved via PATH). OMNIA_DATA_DIR must be set
by the caller to an isolated, disposable directory — this script does not
create or default one itself, since isolation is the caller's responsibility.
"""
import json
import os
import re
import subprocess
import sys
import time

OMNIA = os.environ.get("OMNIA_BIN", "omnia")
NOISE = re.compile(r"(Update available|To update|brew update|brew upgrade)")
SAVED_RE = re.compile(r"Memory saved:\s*#(\d+)")
RANK_RE = re.compile(r"^\s*\[(\d+)\]\s+#(\d+)")


def run(args, stdin=None):
    """Run an omnia command; return (clean_stdout, elapsed_ms)."""
    t0 = time.perf_counter()
    p = subprocess.run(
        [OMNIA, *args],
        input=stdin,
        capture_output=True,
        text=True,
    )
    ms = (time.perf_counter() - t0) * 1000.0
    out = "\n".join(
        l for l in (p.stdout + "\n" + p.stderr).splitlines() if not NOISE.search(l)
    )
    return out.strip(), ms


def save(title, msg, mtype, project):
    out, _ = run(["save", title, msg, "--type", mtype, "--project", project])
    m = SAVED_RE.search(out)
    if not m:
        print(f"  !! save failed for {title!r}: {out[:120]}", file=sys.stderr)
        return None
    return int(m.group(1))


def search_ids(query, project, limit=5, mtype=None):
    args = ["search", query, "--project", project, "--limit", str(limit)]
    if mtype:
        args += ["--type", mtype]
    out, ms = run(args)
    ids = [int(m.group(2)) for m in (RANK_RE.match(l) for l in out.splitlines()) if m]
    return ids, ms


def recallfix_ids(query, project, limit=3):
    out, ms = run(["recall-fix", query, "--project", project, "--limit", str(limit), "--json"])
    try:
        data = json.loads(out) if out else []
        ids = [int(h["id"]) for h in data]
    except (json.JSONDecodeError, KeyError, TypeError):
        ids = []
    return ids, ms


# ---------------------------------------------------------------- seed corpus
P = "eval-suite"
POTHER = "other-project"
print("Seeding corpus ...", file=sys.stderr)
M = {}
M["redis_session"] = save(
    "Configured Redis cache for session store",
    "Added Redis-backed session storage to cut Postgres load; pool size 20, TTL 30m in internal/cache/redis.go",
    "config", P)
M["redis_pubsub"] = save(
    "Redis pub/sub for realtime notifications",
    "Used Redis pub/sub channels to fan out realtime notification events to websocket clients",
    "config", P)
M["nil_deref"] = save(
    "Fix nil deref in request handler",
    "panic: runtime error: invalid memory address or nil pointer dereference [signal SIGSEGV: "
    "segmentation violation code=0x1 addr=0x0]\ngoroutine 42 [running]:\n"
    "main.(*Server).handleRequest(0x0)\n\t/app/server.go:88 +0x2c\n"
    "Fixed by nil-checking req.Session before deref",
    "bugfix", P)
M["idx_range"] = save(
    "Fix slice bounds panic in parser",
    "panic: runtime error: index out of range [5] with length 3\n"
    "main.parseTokens(...)\n\t/app/parser.go:120\nGuarded the loop with len() check",
    "bugfix", P)
M["prose_bug"] = save(
    "Login button did nothing on click",
    "The login button did nothing when clicked because the onClick handler was never bound to the "
    "component after the refactor; re-bound it in LoginForm.tsx",
    "bugfix", P)
M["db_decision"] = save(
    "Chose PostgreSQL over MongoDB",
    "Picked PostgreSQL over MongoDB for the orders service because we need multi-row transactional "
    "integrity and strong foreign-key constraints",
    "decision", P)
M["hexagonal"] = save(
    "Adopted hexagonal architecture",
    "Domain core is isolated from adapters via ports; infrastructure (db, http) depends inward. "
    "Screaming architecture at the package layout",
    "architecture", P)
M["argocd"] = save(
    "Deploy pipeline uses ArgoCD GitOps",
    "CD is handled by ArgoCD watching the gitops repo; sync waves order the rollout",
    "config", POTHER)

if None in M.values():
    print("Seed failed, aborting.", file=sys.stderr)
    sys.exit(1)

# ---------------------------------------------------------------- test cases
# kind: "search" | "recallfix"
# expect_empty=True  -> pass when NOTHING is returned (documents a limitation)
CASES = [
    dict(id="S1", desc="Keyword recall — session cache", kind="search",
         q="Redis session cache store", proj=P, want=[M["redis_session"]]),
    dict(id="S2", desc="Looser wording — session storage backend", kind="search",
         q="where are user sessions stored", proj=P, want=[M["redis_session"]]),
    dict(id="S3", desc="Decision recall — DB choice", kind="search",
         q="why did we pick our database", proj=P, want=[M["db_decision"]]),
    dict(id="S4", desc="Architecture recall", kind="search",
         q="how is the domain isolated from infrastructure", proj=P, want=[M["hexagonal"]]),
    dict(id="S5", desc="Type filter — only config for 'Redis'", kind="search",
         q="Redis", proj=P, mtype="config",
         want=[M["redis_session"], M["redis_pubsub"]],
         forbid=[M["nil_deref"], M["idx_range"], M["db_decision"]]),
    dict(id="S6", desc="Cross-project isolation — ArgoCD NOT visible from eval-suite",
         kind="search", q="ArgoCD GitOps deploy", proj=P, expect_empty=True),
    dict(id="S7", desc="Cross-project — ArgoCD IS visible in its own project",
         kind="search", q="ArgoCD GitOps deploy", proj=POTHER, want=[M["argocd"]]),
    dict(id="R1", desc="Signature lane — nil deref (distinctive phrase)", kind="recallfix",
         q="nil pointer dereference", proj=P, want=[M["nil_deref"]]),
    dict(id="R2", desc="Signature lane — index out of range variant", kind="recallfix",
         q="index out of range with length", proj=P, want=[M["idx_range"]]),
    dict(id="R3", desc="Signature lane — full panic line", kind="recallfix",
         q="panic: runtime error: invalid memory address or nil pointer dereference",
         proj=P, want=[M["nil_deref"]]),
    dict(id="R4", desc="[LIMITATION] prose bug has no signature -> no fix recall",
         kind="recallfix", q="login button did nothing when clicked", proj=P,
         expect_empty=True, limitation=True),
    dict(id="R5", desc="[LIMITATION] noise dilutes score below threshold -> miss",
         kind="recallfix", q="nil pointer dereference SIGSEGV in handleRequest",
         proj=P, expect_empty=True, limitation=True),
]

# ---------------------------------------------------------------- run + score
rows = []
for c in CASES:
    if c["kind"] == "search":
        got, ms = search_ids(c["q"], c["proj"], mtype=c.get("mtype"))
    else:
        got, ms = recallfix_ids(c["q"], c["proj"])

    if c.get("expect_empty"):
        forbid = c.get("forbid")
        passed = (len(got) == 0)
        rr, h1, h3 = None, None, None
    else:
        want = set(c["want"])
        first_rank = next((i + 1 for i, g in enumerate(got) if g in want), None)
        rr = (1.0 / first_rank) if first_rank else 0.0
        h1 = 1 if got[:1] and got[0] in want else 0
        h3 = 1 if any(g in want for g in got[:3]) else 0
        passed = h3 == 1
        if c.get("forbid"):
            passed = passed and not (set(got) & set(c["forbid"]))
    rows.append(dict(c=c, got=got, ms=ms, rr=rr, h1=h1, h3=h3, passed=passed))

# ---------------------------------------------------------------- report
def cell(v):
    return "-" if v is None else str(v)

print("\n" + "=" * 78)
print("OMNIA RECALL EVAL — RESULTS")
print("=" * 78)
print(f"{'ID':<4}{'kind':<10}{'hit@1':<7}{'hit@3':<7}{'RR':<6}{'ms':<7}{'pass':<6}desc")
print("-" * 78)
for r in rows:
    c = r["c"]
    rr = f"{r['rr']:.2f}" if r["rr"] is not None else "-"
    print(f"{c['id']:<4}{c['kind']:<10}{cell(r['h1']):<7}{cell(r['h3']):<7}"
          f"{rr:<6}{r['ms']:<7.0f}{'PASS' if r['passed'] else 'FAIL':<6}{c['desc']}")

# aggregates over scored (non-empty-expected) cases
scored = [r for r in rows if not r["c"].get("expect_empty")]
lim = [r for r in rows if r["c"].get("limitation")]
iso = [r for r in rows if r["c"].get("expect_empty") and not r["c"].get("limitation")]
n = len(scored)
mrr = sum(r["rr"] for r in scored) / n
h1 = sum(r["h1"] for r in scored) / n
h3 = sum(r["h3"] for r in scored) / n
search_ms = [r["ms"] for r in rows if r["c"]["kind"] == "search"]
rf_ms = [r["ms"] for r in rows if r["c"]["kind"] == "recallfix"]
npass = sum(1 for r in rows if r["passed"])

print("-" * 78)
print(f"Ranked-recall cases: {n}   hit@1={h1:.0%}   hit@3={h3:.0%}   MRR={mrr:.2f}")
print(f"Latency: search avg={sum(search_ms)/len(search_ms):.0f}ms   "
      f"recall-fix avg={sum(rf_ms)/len(rf_ms):.0f}ms")
print(f"Isolation checks: {sum(1 for r in iso if r['passed'])}/{len(iso)} passed")
print(f"Documented limitations reproduced: {sum(1 for r in lim if r['passed'])}/{len(lim)}")
print(f"TOTAL: {npass}/{len(rows)} cases behaved as expected")
print("=" * 78)
