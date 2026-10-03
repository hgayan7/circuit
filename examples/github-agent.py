#!/usr/bin/env python3
"""A minimal agent workflow through Circuit; never receives a GitHub token."""
import argparse
import base64
import json
import os
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--demo", action="store_true", help="Use the explicit local simulation")
parser.add_argument("--url", default="http://127.0.0.1:8080")
parser.add_argument("--repo")
parser.add_argument("--base-sha", help="Full commit SHA used to create the agent branch")
parser.add_argument("--task-id", default=str(uuid.uuid4()), help="Persist and reuse this ID on retries")
args = parser.parse_args()
token = "circuit-demo-engineering-local-only-token" if args.demo else os.environ.get("CIRCUIT_AGENT_TOKEN")
repo = "demo/project" if args.demo else args.repo
base_sha = "a" * 40 if args.demo else args.base_sha
if not token or not repo or not base_sha:
    parser.error("Real mode needs CIRCUIT_AGENT_TOKEN, --repo, and --base-sha")
print("Task ID (reuse on retries):", args.task_id, flush=True)

def request(path, payload=None, key=None):
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(args.url.rstrip("/") + path,
        data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        detail = json.load(error)
        raise RuntimeError(detail.get("error", detail.get("reason", "Action rejected"))) from None

def action(operation, values):
    result = request("/v1/actions", {"operation": operation, "repository": repo, "args": values},
                     args.task_id + ":" + operation)
    print(operation, result["id"], result["state"], flush=True)
    deadline = time.monotonic() + 3600
    while result["state"] in ("pending", "approved", "executing"):
        if time.monotonic() > deadline:
            raise RuntimeError("Polling timed out; preserve the task ID and check the existing action")
        time.sleep(2)
        result = request("/v1/actions/" + result["id"])
    if result["state"] != "succeeded":
        raise RuntimeError(result["state"] + ": " + result["reason"])
    outcome = result.get("outcome", {})
    if outcome.get("warning"):
        raise RuntimeError(outcome["warning"] + "; the action already completed, do not repeat it")
    return outcome.get("body", {})

branch = "circuit/" + args.task_id
action("create_branch", {"branch": branch, "sha": base_sha})
action("put_file", {"branch": branch, "path": "circuit-demo.txt",
    "content": base64.b64encode(b"A change proposed through Circuit.\n").decode(),
    "message": "Propose a Circuit demonstration change"})
pr = action("create_pr", {"title": "Circuit demonstration", "head": branch, "base": "main"})
# Read the current PR head; the approval and merge are bound to this exact commit.
current = action("get_pr", {"number": pr["number"]})
action("merge_pr", {"number": pr["number"], "sha": current["head"]["sha"]})
print("Workflow completed through Circuit.", flush=True)
