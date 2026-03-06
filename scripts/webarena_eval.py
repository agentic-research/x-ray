#!/usr/bin/env python3
"""WebArena evaluation bridge for X-Ray.

Reads x-ray's results.jsonl and produces:
  1. Basic self-reported scores (from x-ray's own success tracking)
  2. WebArena-Verified official scores (via the webarena-verified CLI)

The bridge converts x-ray's output format into the per-task directory
structure that webarena-verified eval-tasks expects.

Usage:
    uv run scripts/webarena_eval.py results/webarena_latest/
    uv run scripts/webarena_eval.py results/webarena_latest/ --verified
"""

import ast
import json
import os
import re
import subprocess
import sys
from pathlib import Path


def load_results(results_dir: Path) -> list[dict]:
    """Load results from a JSONL file."""
    jsonl = results_dir / "results.jsonl"
    if not jsonl.exists():
        print(f"Error: {jsonl} not found", file=sys.stderr)
        sys.exit(1)

    results = []
    with open(jsonl) as f:
        for line in f:
            line = line.strip()
            if line:
                results.append(json.loads(line))
    return results


def basic_score(results: list[dict]) -> dict:
    """Compute basic pass/fail statistics from x-ray's own success field."""
    total = len(results)
    succeeded = 0
    failed = 0
    timeouts = 0
    errors = 0

    for r in results:
        if r.get("success", False):
            succeeded += 1

        status = r.get("status", "").lower()
        if status == "failed":
            failed += 1
        elif status == "timeout":
            timeouts += 1
        elif status == "error":
            errors += 1

    return {
        "total": total,
        "succeeded": succeeded,
        "failed": failed,
        "timeouts": timeouts,
        "errors": errors,
        "score_pct": (succeeded / total * 100) if total > 0 else 0,
    }


def _normalize_answer(raw: str, task_status: str) -> tuple:
    """Normalize an agent answer into (status, retrieved_data) for webarena-verified.

    Handles the many formats the Planner might produce:
      - "N/A", "[]", "none", ""  → ("not_found_error", None)
      - '["Alice", "Bob"]'       → ("success", ["Alice", "Bob"])
      - "* Alice\\n* Bob"         → ("success", ["Alice", "Bob"])
      - "Alice, Bob, Charlie"    → ("success", ["Alice", "Bob", "Charlie"])
      - "Alice\\nBob\\nCharlie"   → ("success", ["Alice", "Bob", "Charlie"])
      - "42" / "some answer"     → ("success", "42")
    """
    raw = raw.strip()

    # Non-success task statuses.
    if task_status.lower() not in ("done", "success", "completed", "failed", ""):
        return ("error", raw or None)

    # Empty / not-found answers.
    if raw in ("", "N/A", "n/a", "None", "none", "[]", "[ ]"):
        return ("not_found_error", None)

    # Try JSON array parse: '["Alice", "Bob"]' or '[{"key":"val"}]'.
    if raw.startswith("["):
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, list):
                if not parsed:
                    return ("not_found_error", None)
                # Preserve dicts/objects for structured answers (e.g. task 27).
                # Only stringify primitive items (strings, numbers).
                items = []
                for x in parsed:
                    if isinstance(x, dict):
                        items.append(x)
                    elif isinstance(x, str):
                        s = x.strip()
                        if s:
                            items.append(s)
                    elif x is not None:
                        items.append(x)
                if not items:
                    return ("not_found_error", None)
                return ("success", items)
        except (json.JSONDecodeError, ValueError):
            # Handle Python-style single-quote lists: "['Alice', 'Bob']"
            try:
                parsed = ast.literal_eval(raw)
                if isinstance(parsed, list):
                    if not parsed:
                        return ("not_found_error", None)
                    items = []
                    for x in parsed:
                        if isinstance(x, dict):
                            items.append(x)
                        elif isinstance(x, str):
                            s = x.strip()
                            if s:
                                items.append(s)
                        elif x is not None:
                            items.append(x)
                    if not items:
                        return ("not_found_error", None)
                    return ("success", items)
            except (ValueError, SyntaxError):
                pass

    # Markdown bullet list: "* Alice\n* Bob" or "- Alice\n- Bob".
    if re.search(r"^[\*\-]\s+", raw, re.MULTILINE):
        items = []
        for line in raw.split("\n"):
            line = re.sub(r"^[\*\-]\s+", "", line).strip()
            if line:
                items.append(line)
        if items:
            return ("success", items)

    # Comma-separated: "Alice, Bob, Charlie".
    if "," in raw:
        items = [item.strip().rstrip(".") for item in raw.split(",") if item.strip()]
        if items:
            return ("success", items)

    # "and"-separated: "Rachel and T. Gannon."
    if re.search(r"\band\b", raw):
        items = [item.strip().rstrip(".") for item in re.split(r"\band\b", raw) if item.strip()]
        if len(items) > 1:
            return ("success", items)

    # Newline-separated (multiple lines without bullets).
    if "\n" in raw:
        items = [line.strip() for line in raw.split("\n") if line.strip()]
        if len(items) > 1:
            return ("success", items)

    # Single value — return as string (not wrapped in list).
    return ("success", raw)


def _infer_task_type(intent: str, explicit_type: str) -> str:
    """Infer task type from intent when not explicitly provided.

    NAVIGATE: "Open...", "Go to...", "Navigate to...", "Show me the..."
    RETRIEVE: default for data extraction tasks.
    """
    if explicit_type:
        return explicit_type

    intent_lower = intent.lower().strip()
    navigate_prefixes = (
        "open ", "go to ", "navigate to ", "show me the ", "show me my ",
        "visit ", "take me to ",
    )
    for prefix in navigate_prefixes:
        if intent_lower.startswith(prefix):
            return "navigate"

    return "RETRIEVE"


def _make_har_entry(url: str) -> dict:
    """Create a single HAR entry for a URL visit."""
    return {
        "request": {
            "method": "GET",
            "url": url,
            "httpVersion": "HTTP/1.1",
            "headers": [],
            "queryString": [],
            "cookies": [],
            "headersSize": -1,
            "bodySize": 0,
        },
        "response": {
            "status": 200,
            "statusText": "OK",
            "httpVersion": "HTTP/1.1",
            "headers": [],
            "cookies": [],
            "content": {"size": 0, "mimeType": "text/html"},
            "redirectURL": "",
            "headersSize": -1,
            "bodySize": 0,
        },
        "cache": {},
        "timings": {"send": 0, "wait": 0, "receive": 0},
        "time": 0,
        "startedDateTime": "2026-03-03T12:00:00.000Z",
    }


def prepare_eval_dir(results: list[dict], eval_dir: Path) -> None:
    """Convert x-ray results into webarena-verified's expected directory structure.

    webarena-verified eval-tasks expects:
      output_dir/
        <task_id>/
          agent_response.json   — {task_type, status, retrieved_data}
          network.har           — (optional) HTTP archive
    """
    for r in results:
        task_dir = eval_dir / str(r["task_id"])
        task_dir.mkdir(parents=True, exist_ok=True)

        # Map x-ray result to webarena-verified agent_response format.
        intent = r.get("intent", "")
        task_type = _infer_task_type(intent, r.get("task_type", "") or "")
        raw_data = r.get("summary", "")

        # Determine status and normalize retrieved_data.
        status, retrieved_data = _normalize_answer(raw_data, r.get("status", ""))

        # For NAVIGATE tasks, retrieved_data should be null (the URL is the answer).
        if task_type.lower() == "navigate":
            retrieved_data = None
            # If the agent navigated somewhere, it's a success.
            if r.get("success", False):
                status = "success"

        agent_response = {
            "task_type": task_type,
            "status": status,
            "retrieved_data": retrieved_data,
            "final_url": r.get("url_final", ""),
        }

        with open(task_dir / "agent_response.json", "w") as f:
            json.dump(agent_response, f, indent=2)

        # Build HAR entries from action trace + final URL.
        # For NAVIGATE tasks the NetworkEventEvaluator checks that the agent
        # actually visited the target URL, so we synthesize entries from
        # open_url actions and the final URL.
        har_entries = []
        for a in (r.get("actions") or []):
            tool = a.get("tool", "")
            args = a.get("args", {})
            if tool == "open_url" and isinstance(args.get("url"), str):
                har_entries.append(_make_har_entry(args["url"]))
        # Always include the final URL (most important for NAVIGATE evaluation).
        final_url = r.get("url_final", "")
        if final_url:
            har_entries.append(_make_har_entry(final_url))
        # Fallback: at least one entry so the HAR is valid.
        if not har_entries:
            har_entries.append(_make_har_entry(r.get("start_url", "http://localhost/")))

        har = {
            "log": {
                "version": "1.2",
                "creator": {"name": "x-ray", "version": "1.0"},
                "entries": har_entries,
            }
        }
        with open(task_dir / "network.har", "w") as f:
            json.dump(har, f)


def verified_score(results: list[dict], results_dir: Path) -> dict | None:
    """Score using webarena-verified eval-tasks CLI."""
    eval_dir = results_dir / "wa_eval"
    prepare_eval_dir(results, eval_dir)

    config_path = Path("docker/webarena-config.json")
    task_ids = ",".join(str(r["task_id"]) for r in results)

    cmd = [
        "uvx", "webarena-verified", "eval-tasks",
        "--output-dir", str(eval_dir),
        "--task-ids", task_ids,
    ]
    if config_path.exists():
        cmd.extend(["--config", str(config_path)])

    print(f"Running: {' '.join(cmd)}")
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
        print(result.stdout)
        if result.stderr:
            print(result.stderr, file=sys.stderr)

        # Parse eval_result.json if produced.
        eval_result_path = eval_dir / "eval_result.json"
        if eval_result_path.exists():
            with open(eval_result_path) as f:
                return json.load(f)

        return {"raw_output": result.stdout}
    except FileNotFoundError:
        print("webarena-verified not found. Install with: pip install webarena-verified", file=sys.stderr)
        return None
    except subprocess.TimeoutExpired:
        print("webarena-verified eval timed out", file=sys.stderr)
        return None


def write_run_summary(results: list[dict], results_dir: Path) -> Path:
    """Write a compact markdown run summary (< 30 lines per task).

    Data sources:
      - results.jsonl (loaded as `results`)
      - wa_eval/<id>/eval_result.json (per-task verified results, if they exist)
    """
    from datetime import datetime

    # Aggregate stats.
    total = len(results)
    n_pass = sum(1 for r in results if r.get("success", False))
    n_fail = 0
    n_timeout = 0
    for r in results:
        st = r.get("status", "").lower()
        if st in ("failed", "done") and not r.get("success", False):
            n_fail += 1
        elif st == "timeout":
            n_timeout += 1
    elapsed_list = [r.get("elapsed_ms", 0) / 1000.0 for r in results]
    avg_elapsed = sum(elapsed_list) / len(elapsed_list) if elapsed_list else 0
    pct = (n_pass / total * 100) if total > 0 else 0

    ts = datetime.now().strftime("%Y%m%d %H%M%S")
    lines = [
        f"# X-Ray Run — {ts}",
        f"**{n_pass}/{total} ({pct:.1f}%)** | {n_pass} pass, {n_fail} fail, {n_timeout} timeout | avg {avg_elapsed:.1f}s",
        "---",
    ]

    for r in results:
        task_id = r["task_id"]
        status_raw = r.get("status", "unknown").upper()
        elapsed_s = r.get("elapsed_ms", 0) / 1000.0
        success = r.get("success", False)
        verdict = "PASS" if success else "FAIL"

        lines.append(f"### Task {task_id} — {status_raw} ({elapsed_s:.1f}s) {verdict}")
        lines.append(f"**Intent:** {r.get('intent', 'N/A')}")

        answer = r.get("summary", "N/A")
        lines.append(f"**Answer:** {answer}")

        # Try to load verified eval result for expected answer + reason.
        eval_path = results_dir / "wa_eval" / str(task_id) / "eval_result.json"
        if eval_path.exists():
            try:
                with open(eval_path) as f:
                    ev = json.load(f)
                expected = ev.get("expected", ev.get("reference_answer", ""))
                if expected:
                    lines.append(f"**Expected:** {expected}")
                reason = ev.get("reason", ev.get("explanation", ""))
                if reason:
                    lines.append(f"**Reason:** {reason}")
            except (json.JSONDecodeError, KeyError):
                pass

        # Action trace from results.jsonl.
        actions = r.get("actions", [])
        if actions:
            trace_parts = []
            for a in actions:
                tool = a.get("tool", a.get("action", "?"))
                args_str = a.get("args", a.get("payload", ""))
                if isinstance(args_str, dict):
                    goal = args_str.get("goal", "")
                    if len(goal) > 60:
                        goal = goal[:57] + "..."
                    trace_parts.append(f"{tool}({goal})")
                else:
                    s = str(args_str)
                    if len(s) > 60:
                        s = s[:57] + "..."
                    trace_parts.append(f"{tool}({s})")
            lines.append(f"**Trace:** {' → '.join(trace_parts)}")
        else:
            lines.append("**Trace:** _(no actions captured)_")

        lines.append("")  # blank line between tasks

    summary_path = results_dir / "run_summary.md"
    summary_path.write_text("\n".join(lines) + "\n")
    print(f"Run summary written to {summary_path}")
    return summary_path


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <results_dir> [--verified]", file=sys.stderr)
        sys.exit(1)

    results_dir = Path(sys.argv[1])
    use_verified = "--verified" in sys.argv

    results = load_results(results_dir)
    print(f"Loaded {len(results)} results from {results_dir}")
    print()

    # Basic scoring (x-ray's own success tracking).
    basic = basic_score(results)
    print("=== X-Ray Self-Reported Score ===")
    print(f"  Total:     {basic['total']}")
    print(f"  Succeeded: {basic['succeeded']}")
    print(f"  Failed:    {basic['failed']}")
    print(f"  Timeouts:  {basic['timeouts']}")
    print(f"  Errors:    {basic['errors']}")
    print(f"  Score:     {basic['score_pct']:.1f}%")
    print()

    # Per-task breakdown.
    print("=== Per-Task Breakdown ===")
    print(f"{'TaskID':<8} {'Status':<10} {'Time(ms)':<10} {'Intent'}")
    print("-" * 70)
    for r in results:
        intent = r.get("intent", "")
        if len(intent) > 40:
            intent = intent[:37] + "..."
        print(
            f"{r['task_id']:<8} {r['status']:<10} {r.get('elapsed_ms', 0):<10} {intent}"
        )
    print()

    # Compact markdown summary for sharing.
    write_run_summary(results, results_dir)

    # WebArena-Verified official scoring (run once, reuse result).
    verified_result = None
    if use_verified:
        print("=== WebArena-Verified Official Score ===")
        verified_result = verified_score(results, results_dir)
        if verified_result:
            print(json.dumps(verified_result, indent=2))
        print()

    # Write machine-readable scores.
    scores_path = results_dir / "scores.json"
    scores: dict = {"basic": basic}
    if verified_result:
        scores["verified"] = verified_result
    with open(scores_path, "w") as f:
        json.dump(scores, f, indent=2)
    print(f"Scores written to {scores_path}")


if __name__ == "__main__":
    main()
