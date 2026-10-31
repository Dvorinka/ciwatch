# Agent brief: build `ciwatch` — a CI companion CLI for coding agents

## What you're building

`ciwatch` — a single-binary Go CLI that sits between a coding agent and GitHub Actions. It exists to eliminate two measured wastes in agent workflows:

1. **Waiting:** agents poll `gh run watch` / `gh run list` repeatedly, burning context and wall-clock on "still running". `ciwatch` replaces that with one backgroundable process that exits once, with a structured result.
2. **Unnecessary CI cycles:** pushing a tag/branch whose failure was locally detectable. `ciwatch preflight` maps changed paths to local equivalent checks and runs them before the push leaves the machine.

Plus a third smaller win: **failure memory** — every observed run is appended to a local history file; when a failure signature repeats, `ciwatch` surfaces the previous occurrence so the agent fixes a *known* failure instead of re-diagnosing.

Explicitly out of scope for v1: no daemon, no MCP server, no LLM/AI log summarization, no GitLab/Gitea, no web UI, no CI-time prediction. Heuristics and the `gh` CLI only.

## Why it exists — measured assessment

This tool is scoped by an honest accounting of where agent↔CI time is *actually* lost (observed across a 5-tag release saga: Kotlin metadata crashes, AGP 9 DSL removals, Metro bundle failures):

| Claimed pain | Reality |
|---|---|
| "Agent wastes time polling" | Partially — hosts already background `gh run watch` and get woken on exit. The poll loop is solved by any background primitive. |
| "Agent can't act during CI" | Already solvable — the host can do other work while a watch runs. |
| Failure → relevant log lines | **Real.** Each failure meant manually fetching and grepping thousands of log lines. Automating "fetch failed job → extract error window → structured result" is the genuine win. |
| Preflight local checks | **Real.** Four of five release failures were locally detectable — a path-aware preflight would have collapsed the saga to one iteration. |
| Failure memory ("seen this before → fixed by commit X") | **Real and underrated.** The same root cause (Kotlin metadata mismatch) appeared in three different disguises across runs. A signature store surfaces that instantly. |
| Duration prediction | Nice-to-have. v1 gets only the cheap version: flag jobs exceeding historical p90. |
| MCP/daemon/multi-CI | Speculative. MCP tools are request/response — an MCP server can't proactively wake an agent mid-turn anyway. The background-shell mechanism already covers the wakeup. |

## Additional real problems the spec must solve

These came from the same saga — all cheap, all worth it:

1. **Job granularity over run verdict.** A run "failing" is not the actionable signal. The release workflow ships `deploy` gated only on `release`+`docker` — mobile/desktop failures don't block deployment. `wait` must report **per-job conclusions** and distinguish the jobs that gate the user's actual goal. Parse `needs:` from `.github/workflows/*.yml` to build the dependency map; never emit "CI failed" without saying *which* jobs and whether they were on the deploy path.
2. **Step-level failure extraction.** The jobs API response includes a `steps[]` array with per-step conclusions — use it to name the failing step (`build signed AAB + APK`) rather than just the job.
3. **Log availability constraint.** `gh run view --log` refuses while the run is in progress, and job logs only exist after a job completes. Poll the jobs endpoint for state; fetch `jobs/{id}/logs` only for completed jobs. (Observed empirically — encode it.)
4. **Tag/ref resolution.** Tag pushes don't appear under `gh run list --branch`; resolve runs by `head_sha` first, fall back to ref-name matching for `refs/tags/*`. A release tag push must find its workflow run reliably.
5. **Superseded-run cancellation.** `ciwatch push` should offer `--cancel-superseded` — but only when the workflow file lacks a `concurrency:` group that already does it. Don't fight GitHub's own mechanism.
6. **Flaky rerun.** Optional `--rerun-flaky`: if the sliced failure matches a transient signature (runner lost contact, `503`, Gradle daemon OOM, network timeout), run `gh run rerun --failed` exactly once and resume waiting. Never auto-retry deterministic compile/test failures.
7. **`ciwatch status`** — one-shot structured snapshot of the newest run for a ref, for agents that poll themselves anyway. Non-blocking, cheap.
8. **Job outputs surfacing.** Workflows expose outputs (`version`, `versionCode`) via `needs.<job>.outputs.*`. On success, print captured job outputs — the agent often needs them for deploy confirmation.
9. **Post-run verify hook.** `.ciwatch.yml` gets an optional `verify:` command run after a successful run (e.g. `curl -sf https://app.invico.org/api/health`) — CI green is not the same as deployed-and-healthy.
10. **Overage flag.** While waiting, compare elapsed per-job time against history p90 and emit a `longer than usual` marker — the honest, cheap version of duration prediction.
11. **Optional desktop notification** — `--notify` shells out to `notify-send`/`osascript` on terminal state. For human operators, not agents.
12. **Release artifact gate.** A green mobile job is not proof the artifact is a production app. After a successful job matching `mobile` (or `.ciwatch.yml` `artifact_jobs`), run the checks in "Release artifact gate" below. A failure exits `3`, same as a failed `verify:` hook — CI built something, but it is not shippable.

## Target environment

- Runs on the same machine the agent runs on (Linux/macOS), inside a repo checkout.
- `gh` CLI is installed and authenticated — **delegate all GitHub API access to `gh api`/`gh run`; do not reimplement auth or REST.**
- Output must be agent-consumable: `--json` flag on every command; human output is terse (no spinners, no colors when non-TTY).
- Single Go module, standard library first. History storage: append-only JSONL at `${XDG_DATA_HOME:-~/.local/share}/ciwatch/history.jsonl`. No sqlite, no external deps unless strictly necessary.

## Commands

### `ciwatch wait [run-id | --ref <ref>]`

The core loop. Resolves a workflow run (explicit id, or newest run for `--ref` defaulting to current HEAD sha/branch), then polls `gh api repos/{owner}/{repo}/actions/runs/{id}/jobs` with backoff: 10s → 30s → 60s (cap). Emits nothing between state changes.

It exits early and only when actionable. Build the job dependency map from the workflow file's `needs:` clauses so you can report whether a failed job sits on the deploy path:

- **Any job fails/cancelled/times-out** → don't wait for siblings. Fetch the failed job's log via `gh api repos/{owner}/{repo}/actions/jobs/{job_id}/logs` (logs only exist for completed jobs — if the run is mid-flight, keep polling), slice the relevant error window, name the failing *step* from the `steps[]` array, print result, exit `2`.
- **All jobs complete** → print a compact per-job table (name, conclusion, duration), plus captured job outputs if any, exit `0`.
- **Run verdict ≠ job verdict.** If a non-gating job fails but the deploy-path jobs succeeded, report exactly that — `deploy ✓ · mobile ✗ (non-blocking)` — and let exit code reflect the gating outcome, not the worst job.
- **Flags:** `--job <name>` to wait on a single gate only; `--timeout <dur>` (default 45m); `--json` for machine output; `--rerun-flaky` (one-shot retry on transient signatures); `--notify` (desktop notification on exit).
- During the wait, if a job exceeds its historical p90 duration (from history.jsonl), emit a `longer than usual` marker on that job — a status line, not an exit.

Human output example on failure:

```
✗ mobile — build signed AAB + APK — 5m32s
  Execution failed for task ':app:createBundleReleaseJsAndAssets'.
  > Process 'command 'node'' finished with non-zero exit value 1
  Error: Cannot find module '…/react-native/rn-get-polyfills'
  signature: metro/module-not-found
  previous occurrence: run 37453993282 (v0.7.1), commit f4b16f6
```

### `ciwatch push`

`git push` the current branch (or `--tag <tag>`), then locate the run it triggered — match on `head_sha`, not `--branch` (tag pushes don't surface there). Optionally `--cancel-superseded` to `gh run cancel` earlier pending/in-progress runs on the same ref — but first check whether the workflow file already declares a `concurrency:` group that handles this; if so, skip the flag silently. Then chain into `wait`.

### `ciwatch status [--ref <ref>]`

One-shot snapshot of the newest run for a ref: per-job state, elapsed vs historical p90, failed steps if any. Non-blocking — for agents that prefer to poll themselves. `--json` first-class.

### `ciwatch preflight`

Reads `.ciwatch.yml` from the repo root:

```yaml
checks:
  - name: api
    paths: ["apps/api/**"]
    run: ["cd apps/api && go build ./... && go test ./..."]
  - name: web
    paths: ["apps/web/**"]
    run: ["cd apps/web && npx tsc -b && npx oxlint"]
  - name: mobile-gradle-config
    paths: ["apps/mobile/**"]
    run: ["cd apps/mobile && npx tsc --noEmit"]
    optional: true   # warn, don't block, if the tool/env is missing

# optional post-run hook: executed by `ciwatch wait` after a successful run
# (CI green ≠ deployed-and-healthy — verify the thing the CI shipped)
verify: "curl -sf https://app.invico.org/api/health"

# Optional. After these jobs succeed, run the built-in release artifact gate.
# Default: any job named "mobile".
artifact_jobs: ["mobile"]
artifact:
  api_url_must_not_match: ["localhost", "127.0.0.1", "10.0.2.2"]
  forbid_deps: ["expo-dev-client"]
  require_gradle_tasks: ["bundleRelease", "assembleRelease"]
  reject_gradle_tasks: ["assembleDebug", "bundleDebug"]
```

Diffs `git diff --name-only origin/<default>...HEAD` (override via `--base`, `--files`), selects checks whose path globs match, runs them sequentially (fail-fast by default, `--all` to run all matches regardless of first failure), prints a compact result table. Exit non-zero if any required check fails. This is the feature that would have caught the Expo/RN version skew locally: an `apps/mobile` change triggered a local Gradle configuration check instead of four doomed release tags.

### `ciwatch history`

Reads `history.jsonl`, prints per-workflow/per-job stats: count, p50/p90 duration, last conclusion. `ciwatch history --job mobile --json` for programmatic use.

## Log slicing (the reason this exists)

From a failed job's log, produce a *small actionable slice* — not the whole log:

1. Parse per-step boundaries from timestamped lines.
2. Score lines by failure patterns: `FAILURE:`, `FAILED`, `* What went wrong`, `^e: file://`, `error TS\d+`, `Error:`, `exit code|exit value \d+`, `panic:`, `##[error]`.
3. Emit: the failing step name, the first matching diagnostic block, ±5 lines of context, deduped.
4. Normalize a **signature**: strip absolute paths, timestamps, hashes, and version numbers from the primary error line; store `{signature, job, run_id, head_sha, ts}` in history. On a repeat signature, print the previous occurrence line as shown above.

Keep the slicer honest: if no pattern matches, fall back to the last ~40 lines of the log. Never dump the full log.

## Non-goals and constraints

- No speculative abstractions: five commands (`wait`, `push`, `status`, `preflight`, `history`), one file per concern, stdlib arg parsing (`flag` or minimal hand-rolled subcommand dispatch — no cobra unless already justified).
- No goroutine fanfare needed; polling is sequential and slow by design.
- Respect rate limits: exponential backoff; honor `X-RateLimit-Remaining` if trivially readable from `gh api --include`.
- `--json` output is the contract; keep keys stable, snake_case.
- Unit-test the log slicer against saved fixture logs (include `testdata/failed-gradle.log` and `testdata/failed-metro.log` — synthetic is fine, ~100 lines each).
- `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt` must pass before any commit.

## Acceptance criteria

In a repo with a GitHub Actions workflow:

1. `ciwatch wait <failing-run-id>` exits 2 within ~15s of the job failure and prints ≤30 lines including the actual error text and a signature.
2. `ciwatch wait <passing-run-id>` exits 0 with the job table.
3. `ciwatch preflight` on a diff touching only `apps/mobile/**` runs only the mobile-mapped checks and blocks on failure.
4. A second failure with the same signature prints the `previous occurrence` line.
5. `--json` on `wait` produces parseable output with keys: `run_id`, `conclusion`, `failed_job`, `failed_step`, `gating` (bool — was the failure on the deploy path), `log_slice`, `signature`, `previous_occurrence`, `job_outputs`.
6. On a run where a non-gating job fails but deploy-path jobs pass, `wait` reports the mixed verdict and exits `1` (warnings only). The output distinguishes `gating: true/false` per job regardless.
7. `ciwatch push --tag v0.0.1` resolves the resulting workflow run via `head_sha` even though the ref is a tag.
8. A `verify:` hook in `.ciwatch.yml` runs after a green run; a failing verify is reported distinctly from a CI failure (exit `3`).
9. A mobile job whose workflow runs `assembleDebug`, or an app config whose API URL is `localhost`, fails the artifact gate with exit `3` and `artifact.production: false`. A debug-signed `.aab` passed via `--artifact` fails the same way.

## Project structure

```
ciwatch/
  cmd/ciwatch/main.go          # entrypoint, subcommand dispatch
  internal/
    gh.go                  # gh CLI wrapper: run, list, jobs, logs, cancel, rerun
    wait.go                # polling loop, state machine, exit logic
    push.go                # git push + run resolution + cancel-superseded
    status.go              # one-shot snapshot
    preflight.go           # .ciwatch.yml parsing, diff-based check selection, execution
    history.go             # history.jsonl append + read + p50/p90 stats
    workflow.go            # parse .github/workflows/*.yml for needs: dependency map
    logslice.go            # log fetching, step boundary parsing, error extraction
    signature.go           # signature normalization + transient pattern matching
    config.go              # .ciwatch.yml struct + parsing
    artifact.go            # release artifact gate (production app, not debug)
    output.go              # --json formatting, human table formatting
  testdata/
    failed-gradle.log      # synthetic Gradle failure log (~100 lines)
    failed-metro.log       # synthetic Metro bundle failure log (~100 lines)
    failed-gotest.log      # synthetic Go test failure log (~80 lines)
    workflow.yml           # sample workflow with needs: for dependency map tests
  .ciwatch.yml                 # example config (for testing preflight)
  go.mod
```

One file per concern. No package nesting beyond `internal/`. `main.go` is dispatch only — each subcommand delegates to its `internal/` module.

## `gh` CLI command reference

All GitHub access goes through `gh`. Never import a Go GitHub client. Exact commands and response shapes the agent needs:

### List runs for a ref or sha

```bash
gh api repos/{owner}/{repo}/actions/runs --jq '.workflow_runs[]' \
  -H "Accept: application/vnd.github+json"
```

Response shape (per run):
```json
{
  "id": 37475963388,
  "name": "release",
  "head_sha": "a1b2c3d4e5f6...",
  "head_branch": "master",
  "event": "push",
  "status": "in_progress",
  "conclusion": null,
  "created_at": "2025-01-15T10:30:00Z",
  "html_url": "https://github.com/..."
}
```

To find a run by sha: `gh api repos/{owner}/{repo}/actions/runs?head_sha={sha}`. Tag pushes set `head_branch` to the tag name and `event` to `push` or `create` — match on `head_sha` first, it's the reliable field.

### Get jobs for a run

```bash
gh api repos/{owner}/{repo}/actions/runs/{run_id}/jobs
```

Response shape (per job):
```json
{
  "id": 12345678901,
  "name": "mobile",
  "status": "completed",
  "conclusion": "failure",
  "started_at": "2025-01-15T10:30:00Z",
  "completed_at": "2025-01-15T10:43:32Z",
  "steps": [
    {
      "name": "Set up job",
      "conclusion": "success",
      "number": 1
    },
    {
      "name": "build signed AAB + APK",
      "conclusion": "failure",
      "number": 7
    }
  ]
}
```

`status` values: `queued`, `in_progress`, `completed`. `conclusion` values: `success`, `failure`, `cancelled`, `skipped`, `neutral`, `action_required`, `stale`, `null` (still running).

### Get job logs

```bash
gh api repos/{owner}/{repo}/actions/jobs/{job_id}/logs
```

Returns the raw log text. Only available when the job is `completed`. Returns 410 if the job is still running or logs expired (90 days). The response is a redirect to a temporary URL — `gh api` follows redirects automatically.

### Cancel a run

```bash
gh api -X POST repos/{owner}/{repo}/actions/runs/{run_id}/cancel
```

### Rerun failed jobs

```bash
gh api -X POST repos/{owner}/{repo}/actions/runs/{run_id}/rerun-failed-jobs
```

### Get run outputs

Job outputs are not in the jobs API. They're in the run's workflow run details:
```bash
gh api repos/{owner}/{repo}/actions/runs/{run_id} --jq '.run_number'
```

For job-level outputs (`needs.<job>.outputs.*`), parse the workflow YAML to know which outputs exist, then check the run's check_runs annotations or the job's step outputs. In practice, the most reliable path is to parse the workflow YAML for `outputs:` declarations and report which jobs declare outputs. The actual values are only available via the workflow run API's `jobs_url` → job → `steps` → no direct output field. For v1, surface *which outputs are declared* in the workflow, not their resolved values — the agent can fetch those from the release page or deploy logs if needed.

## Workflow YAML parsing

Parse `.github/workflows/*.yml` to build the job dependency map. Use `gopkg.in/yaml.v3` (justified dependency — the only YAML parser worth using in Go).

Workflow structure relevant to `ciwatch`:

```yaml
jobs:
  release:
    needs: [test, lint]        # string or list
    outputs:
      version: ${{ steps.meta.outputs.version }}
      versionCode: ${{ steps.meta.outputs.version_code }}
  mobile:
    needs: release              # depends on release
  deploy:
    needs: [release, docker]    # deploy waits for both
  desktop:
    needs: release
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
```

Parsing rules:
- `needs` can be a string (`needs: release`) or a list (`needs: [release, docker]`). Handle both.
- `strategy.matrix` creates N jobs from one definition. The jobs API returns them as separate jobs with names like `desktop (ubuntu-latest)`. Strip the matrix suffix for dependency-map matching.
- `outputs` is a map of string keys to `${{ ... }}` expressions. Report the key names only.

### Building the deploy path

The "deploy path" is the set of jobs that must succeed for the user's goal (typically deployment). Algorithm:

1. Parse all jobs and their `needs:` into a directed graph (job → its dependencies).
2. Identify terminal jobs (no other job needs them) — these are leaf nodes.
3. The "deploy path" is all jobs reachable by walking backwards from the `deploy` job (or a configurable `--gate <job>` flag, default: any job named `deploy` or `release`).
4. If no `deploy`/`release` job exists, all jobs are on the critical path (conservative default).

A job failure is `gating: true` if it's on the deploy path, `gating: false` otherwise. The exit code reflects the deploy-path outcome, not the worst job.

## JSON output schemas

### `ciwatch wait --json`

```json
{
  "run_id": 37475963388,
  "conclusion": "failure",
  "jobs": [
    {
      "name": "release",
      "conclusion": "success",
      "duration_seconds": 42,
      "gating": true,
      "on_deploy_path": true
    },
    {
      "name": "mobile",
      "conclusion": "failure",
      "duration_seconds": 332,
      "gating": false,
      "on_deploy_path": false,
      "failed_step": "build signed AAB + APK",
      "log_slice": "Execution failed for task ':app:createBundleReleaseJsAndAssets'.\n> Process 'command 'node'' finished with non-zero exit value 1\nError: Cannot find module 'react-native/rn-get-polyfills'",
      "signature": "metro/module-not-found",
      "previous_occurrence": {
        "run_id": 37453993282,
        "head_sha": "f4b16f6",
        "ts": "2025-01-14T09:12:00Z"
      }
    }
  ],
  "failed_job": "mobile",
  "failed_step": "build signed AAB + APK",
  "gating": false,
  "log_slice": "Execution failed for task ...",
  "signature": "metro/module-not-found",
  "previous_occurrence": null,
  "job_outputs": {
    "release": ["version", "versionCode"]
  }
}
```

On success, `conclusion` is `"success"`, `failed_job`/`failed_step`/`log_slice`/`signature`/`previous_occurrence` are `null`, `jobs` array has all jobs with conclusions.

### `ciwatch status --json`

```json
{
  "run_id": 37475963388,
  "status": "in_progress",
  "conclusion": null,
  "jobs": [
    {
      "name": "lint",
      "status": "completed",
      "conclusion": "success",
      "duration_seconds": 42,
      "p90_seconds": 55,
      "overage": false
    },
    {
      "name": "mobile",
      "status": "in_progress",
      "conclusion": null,
      "elapsed_seconds": 612,
      "p90_seconds": 842,
      "overage": false
    }
  ]
}
```

### `ciwatch preflight --json`

```json
{
  "checks": [
    {
      "name": "api",
      "matched": true,
      "command": "cd apps/api && go build ./... && go test ./...",
      "passed": true,
      "duration_seconds": 12,
      "output": ""
    },
    {
      "name": "mobile-gradle-config",
      "matched": true,
      "command": "cd apps/mobile && npx tsc --noEmit",
      "passed": false,
      "duration_seconds": 8,
      "output": "src/types.ts(42,3): error TS2322: Type 'string' is not assignable to type 'number'.",
      "optional": true
    }
  ],
  "blocked": false,
  "failed_required": 0,
  "failed_optional": 1
}
```

### `ciwatch push --json`

```json
{
  "ref": "v0.7.6",
  "sha": "a1b2c3d4e5f6...",
  "pushed": true,
  "run_id": 37475963388,
  "cancelled_superseded": ["37474000000"],
  "skipped_cancel": false
}
```

### `ciwatch history --json`

```json
{
  "stats": [
    {
      "workflow": "release",
      "job": "mobile",
      "count": 6,
      "p50_seconds": 780,
      "p90_seconds": 842,
      "last_conclusion": "success",
      "last_run_id": 37475963388,
      "last_ts": "2025-01-15T10:30:00Z"
    }
  ]
}
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success — all gating jobs passed, verify hook passed (if configured) |
| 1 | Warnings only — non-gating jobs failed but deploy path succeeded |
| 2 | Critical failure — a gating job failed, or preflight blocked the push |
| 3 | Verify hook failed — CI green but post-run health check failed |
| 4 | Timeout — `--timeout` exceeded before all gating jobs completed |
| 5 | Error — `gh` not found, auth failed, run not found, network error |

Document the exit code for partial failures explicitly: **exit 1** when non-gating jobs fail but deploy-path jobs pass. The output must distinguish `gating: true/false` per job regardless.

## history.jsonl record schema

Append-only, one JSON object per line:

```json
{"run_id":37475963388,"workflow":"release","job":"mobile","conclusion":"failure","duration_seconds":332,"head_sha":"a1b2c3d4","ts":"2025-01-15T10:30:00Z","signature":"metro/module-not-found","failed_step":"build signed AAB + APK"}
```

Fields:
- `run_id` (int) — GitHub Actions run ID
- `workflow` (string) — workflow name
- `job` (string) — job name
- `conclusion` (string) — `success`/`failure`/`cancelled`/`skipped`
- `duration_seconds` (int) — job wall-clock duration
- `head_sha` (string) — commit SHA (first 8 chars for readability)
- `ts` (string) — ISO 8601 timestamp of run creation
- `signature` (string, optional) — normalized failure signature (only on failures)
- `failed_step` (string, optional) — name of the failed step (only on failures)

## `.ciwatch.yml` full schema

```yaml
# .ciwatch.yml — optional. ciwatch works without it using defaults.

# Preflight checks — path-globbed local commands run before push
checks:
  - name: <string>           # required, display name
    paths: [<glob>]           # required, doublestar globs matched against git diff
    run: [<string>]          # required, shell commands to execute sequentially
    optional: <bool>          # optional, default false — if true, failure warns but doesn't block

# Post-run verify hook — run after a successful `ciwatch wait`
# Exit non-zero = exit code 3 (distinct from CI failure)
verify: <string>             # optional, shell command

# Release artifact gate. Default artifact_jobs: ["mobile"] when an
# apps/mobile/app.json or app.json with an expo key exists.
artifact_jobs: [<string>]
artifact:
  api_url_must_not_match: [<string>]   # default: localhost, 127.0.0.1, 10.0.2.2
  forbid_deps: [<string>]              # default: ["expo-dev-client"]
  require_gradle_tasks: [<string>]     # default: ["bundleRelease"]
  reject_gradle_tasks: [<string>]      # default: ["assembleDebug", "bundleDebug"]

# Which job to treat as the deployment gate
# Default: any job named "deploy" or "release"
gate: <string>               # optional, job name

# Default ref for `ciwatch status` and `ciwatch wait --ref`
# Default: current HEAD
default_ref: <string>        # optional, git ref
```

## Implementation details

### Polling backoff

```
poll 1: wait 10s
poll 2: wait 20s
poll 3: wait 30s
poll 4+: wait 60s (cap)
```

Add ±20% jitter to each interval to avoid thundering herd on multiple `ciwatch` instances. Use `time.Sleep` — no need for timers or channels. The poll is a simple loop:

```go
for {
    jobs := gh.GetJobs(runID)
    if actionable(jobs) { break }
    sleep(backoff.Next() + jitter)
}
```

### Signature normalization

Strip from the primary error line:
1. Absolute file paths: `/home/user/project/...`, `/Users/...`, `C:\...` → `<path>`
2. Timestamps: `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}` → `<ts>`
3. SHA hashes: `\b[0-9a-f]{7,40}\b` → `<sha>`
4. Version numbers: `\d+\.\d+\.\d+` → `<ver>`
5. Line numbers: `:\d+` → `:N`
6. Memory addresses: `0x[0-9a-f]+` → `<addr>`

Then take the first 200 chars of the normalized line as the signature. If the line is shorter than 40 chars after normalization, include the next line too.

### Transient failure signatures (for `--rerun-flaky`)

Regex patterns that indicate a transient failure safe to auto-retry once:

```
runner lost contact|The runner has been marked offline
503 Service Unavailable|502 Bad Gateway|504 Gateway Timeout
ETIMEDOUT|ECONNRESET|ECONNREFUSED (only in network-fetch steps, not compile steps)
Gradle daemon OOM|OutOfMemoryError|Java heap space
The operation was canceled|cancelled
```

Never retry if the failure signature matches a compile error, test assertion, or type error — those are deterministic.

### Step-level log parsing

GitHub Actions logs have a structured format. Each step's log is prefixed with a timestamp and the step is delimited by `##[group]` / `##[endgroup]` markers, or by `##[step:<n>]` headers. Parse:

1. Split the log on lines matching `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z ` (timestamp prefix).
2. Group lines by the step they belong to (the `steps[]` array from the jobs API gives step names and numbers; match by order).
3. For the failed step (from `steps[]` where `conclusion == "failure"`), extract its log block.
4. Within that block, apply the error scoring patterns.

If step boundaries can't be determined (some logs don't have clean markers), fall back to scanning the full log with error patterns and taking the first match.

### Error handling

| Situation | Behavior |
|---|---|
| `gh` not installed | Exit 5, message: "`gh` CLI not found. Install from https://cli.github.com/" |
| `gh` not authenticated | Exit 5, message: "`gh` not authenticated. Run `gh auth login`." |
| Run ID not found | Exit 5, message: "Run {id} not found." |
| No workflow run for ref | Exit 5, message: "No workflow run found for ref {ref}." |
| Network error during poll | Retry up to 3 times with 5s backoff, then exit 5 |
| `gh api` rate limited | Back off using `Retry-After` header or 60s default, retry |
| `.ciwatch.yml` malformed | Exit 5, message with parse error and file:line |
| Preflight command not found | If `optional: true`, warn and continue. If required, exit 2. |
| `--notify` but no `notify-send`/`osascript` | Silently skip notification, continue normally |

### Desktop notification

```go
// Linux
exec.Command("notify-send", "ciwatch", message).Run()
// macOS
exec.Command("osascript", "-e", fmt.Sprintf(`display notification "%s" with title "ciwatch"`, message)).Run()
```

Silently ignore failures — notification is best-effort.

### Release artifact gate

Run only after a job listed in `artifact_jobs` completes with `success`. Skip silently when the repo has no Expo app config. This is the check that answers "did CI ship a debug Expo client?" — observed as a manual audit of `release.yml`, `app.json`, and `plugins/withReleaseSigning.js` before v0.7.6 was trusted.

Checks, in order, all local, no extra network:

1. **Workflow tasks.** Read the workflow file that produced the run. The job's `run:` steps must contain every `require_gradle_tasks` token and none of `reject_gradle_tasks`. `assembleDebug` or `bundleDebug` is a failure even if the job is green.
2. **App config.** Read `apps/mobile/app.json`, then `apps/mobile/app.config.js` is not executed (it can hide a URL behind code). For `app.json`, reject `expo.extra.apiUrl` / `expo.extra.API_URL` / any string field named `apiUrl` if it contains a host in `api_url_must_not_match`. Production example that must pass: `https://app.invico.org`.
3. **Forbidden dependency.** `apps/mobile/package.json` `dependencies` must not contain `forbid_deps`. `devDependencies` is allowed — a dev client tool that is not bundled is not a shipped debug app. Also reject `expo-dev-client` if it appears in the app config `plugins` array.
4. **Optional binary.** `ciwatch wait --artifact path/to/app.aab` (repeatable) runs `jarsigner -verify -verbose` when `jarsigner` is on `PATH`. Failure if the output contains `CN=Android Debug` or the verify exit code is non-zero. If `jarsigner` is missing, warn and skip — do not fail the run for a missing local JDK. Do not download the artifact from GitHub; the caller passes a path if they have one.

Exit `3` if any check fails. JSON addition on `wait`:

```json
"artifact": {
  "production": false,
  "failed_checks": ["gradle_task:assembleDebug", "api_url:localhost"]
}
```

`production: true` and `failed_checks: []` when the gate passes. Omit `artifact` entirely when no artifact job was in the run.

## Test fixtures

### `testdata/failed-gradle.log`

Synthetic Gradle build failure. Must contain:
- Timestamp-prefixed lines
- A `##[group]` / `##[endgroup]` step boundary
- The error: `FAILURE: Build failed with an exception.` followed by `* What went wrong:` and `Execution failed for task ':app:compileReleaseKotlin'.`
- A `> Caused by:` chain
- ~100 lines total, with the error ~60% through the file

### `testdata/failed-metro.log`

Synthetic Metro bundler failure. Must contain:
- Timestamp-prefixed lines
- The error: `Error: Cannot find module 'react-native/rn-get-polyfills'`
- A stack trace
- ~80 lines total

### `testdata/failed-gotest.log`

Synthetic Go test failure. Must contain:
- `--- FAIL: TestFoo` line
- `expected: X, got: Y` assertion
- `FAIL` exit line
- ~60 lines total

### `testdata/workflow.yml`

A sample workflow with:
- 5 jobs: `lint`, `test`, `web`, `mobile`, `deploy`
- `needs:` dependencies: `deploy` needs `test`+`web`, `mobile` needs `test`, `web` needs `lint`
- `outputs:` on one job
- A `strategy.matrix` on one job

Tests should verify: the dependency map is built correctly, the deploy path from `deploy` includes `deploy`, `test`, `web`, `lint` but not `mobile`, and matrix job names are stripped correctly.

## Where it goes

Build in `/home/tdvorak/Desktop/PROG+HTML/ciwatch/` — fresh module `ciwatch`, `cmd/ciwatch/main.go` entrypoint. Initialize a git repo. Commit when green.
