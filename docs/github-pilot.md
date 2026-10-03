# Live GitHub pilot — 2026-10-03

Circuit passed **43 checks** using the authorized `hgayan7` account and synthetic test fixtures. Both pull requests were merged through the gateway:

- [Private repository workflow, PR #1](https://github.com/hgayan7/circuit-gateway-pilot-20261003/pull/1)
- [Protected public repository workflow, PR #1](https://github.com/hgayan7/circuit-gateway-protection-pilot-20261003/pull/1)

The repositories remain available as evidence. No product source or account credentials were published. Test issues were closed. The public repository retains its branch protection, including enforcement for administrators.

## Verified behavior

| Check | Observed result |
| --- | --- |
| Branch, file, PR, issue, and read operations | Real GitHub API operations succeeded |
| File and merge approval | Actions stayed pending until operator API approval of the exact digest |
| Idempotent submission | Repeated branch submission returned the original action ID |
| Default-branch writes and workflow-file writes | Denied by Circuit before dispatch |
| Out-of-scope repository | Denied by Circuit; no write made to the product repository |
| Stale PR head | Circuit rejected the previously proposed commit after a new commit |
| Required status check | GitHub returned HTTP 405 for the approved merge; PR remained unmerged |
| Required status satisfied | A separately approved merge succeeded without changing protection |
| Action allowance | Second issue creation was denied after the configured limit was used |
| Gateway restart | Original action IDs and consumed allowance remained effective |
| Execution history | Durable action transitions were retrievable |

GitHub rejected enabling branch protection in the private fixture repository with HTTP 403 and a plan-upgrade message. The separate public fixture repository supports the same protection on this account's plan. The required `circuit-pilot/verified` status was set by the test operator as a synthetic fixture; this did not test a CI integration or a GitHub App-bound status check.

## Limits of this result

This pilot used the account's existing GitHub CLI OAuth token, held in process memory and supplied only to the gateway environment. It did not test least-privilege fine-grained tokens, GitHub App installations, organization authorization, or sandbox isolation. The same test harness acts as agent and operator to exercise both roles. Manual dashboard approvals were tested separately against the local simulation.

The next permission test should use a repository-scoped credential or GitHub App installation with Contents, Pull requests, and Issues permissions. An independently isolated agent must receive only its Circuit credential.

## Reproduce

The opt-in [pilot script](../examples/github-pilot.py) requires two initialized fixture repositories and existing `gh` authentication. It only accepts repository names containing `circuit-gateway-` and `pilot-`. Configure the protected repository's default `main` branch to require `circuit-pilot/verified`, with administrator enforcement. It intentionally creates and merges synthetic PRs and creates then closes test issues.

```sh
go build -o bin/circuit ./cmd/circuit
python3 examples/github-pilot.py \
  --repo owner/circuit-gateway-pilot-your-date \
  --protected-repo owner/circuit-gateway-protection-pilot-your-date \
  --github-user owner \
  --run-id unique-run-id
```

The script starts a loopback gateway, generates separate operator/agent tokens, and shuts the gateway down afterward. It keeps its private database, configuration, and report under the ignored `.circuit/pilot-<run-id>/` directory. Credentials are not written there. Each invocation requires a new run directory; failures stop execution and preserve evidence rather than silently replaying writes.

The completed pilot's sanitized machine-readable evidence is in [github-pilot-results.json](github-pilot-results.json).
