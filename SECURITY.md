# Security

## Reporting

Report vulnerabilities privately via GitHub Security Advisories
(“Report a vulnerability” on the repo's Security tab), or email
info@tdvorak.dev. Do not open a public issue for undisclosed
vulnerabilities.

## Threat model

ciwatch shells out to `git` and `gh`; your GitHub credentials stay inside
`gh`'s keyring — ciwatch never reads or transmits tokens. Local history is
append-only JSONL under `~/.local/share/ciwatch/` and contains run metadata
and failure signatures — no secrets by construction.

- `preflight` and `verify` execute commands from `.ciwatch.yml` — treat that
  file like a Makefile: only run ciwatch in repos whose config you trust.
- `--artifact` runs `jarsigner -verify` on supplied files.
- Notifications are best-effort `notify-send`/`osascript`.

If you find a path where ciwatch could expose credentials or execute
unexpected code, that's a reportable issue.
