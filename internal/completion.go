package internal

import "fmt"

// Completion prints a shell completion script for the given shell.
func Completion(shell string) (string, error) {
	switch shell {
	case "bash":
		return `_cix() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W "wait push status preflight history version completion" -- "$cur"))
    return 0
  fi
  case "$prev" in
    --run|--pr|--job|--timeout|--base|--tag) COMPREPLY=(); return 0 ;;
    --files|--artifact) COMPREPLY=($(compgen -f -- "$cur")); return 0 ;;
    completion) COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur")); return 0 ;;
  esac
  COMPREPLY=($(compgen -W "--run --ref --pr --job --timeout --rerun-flaky --notify --artifact --tag --cancel-superseded --base --files --all --failures --gate --json" -- "$cur"))
}
complete -F _cix ciwatch
`, nil
	case "zsh":
		return `#compdef ciwatch
_cix() {
  local -a cmds=(wait push status preflight history version completion)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
    return
  fi
  _arguments \
    '--run[run id]:run:' \
    '--ref[branch or tag]:ref:' \
    '--pr[pull request]:pr:' \
    '--job[single job]:job:' \
    '--timeout[max wait]:timeout:' \
    '--rerun-flaky[rerun transient]' \
    '--notify[desktop notification]' \
    '--artifact[artifact path]:file:_files' \
    '--tag[tag or HEAD]:tag:' \
    '--cancel-superseded[cancel older runs]' \
    '--base[base ref]:ref:' \
    '--files[changed files]:files:' \
    '--all[all checks]' \
    '--failures[failures only]' \
    '--gate[gate job]:job:' \
    '--json[structured output]'
}
_cix "$@"
`, nil
	case "fish":
		return `complete -c ciwatch -n '__fish_use_subcommand' -a 'wait push status preflight history version completion'
complete -c ciwatch -l run -r -d 'run id'
complete -c ciwatch -l ref -r -d 'branch or tag'
complete -c ciwatch -l pr -r -d 'pull request number'
complete -c ciwatch -l job -r -d 'single job'
complete -c ciwatch -l timeout -r -d 'max wait'
complete -c ciwatch -l rerun-flaky -d 'rerun transient failure'
complete -c ciwatch -l notify -d 'desktop notification'
complete -c ciwatch -l artifact -r -F -d 'artifact path'
complete -c ciwatch -l tag -r -d 'tag or HEAD'
complete -c ciwatch -l cancel-superseded -d 'cancel older runs'
complete -c ciwatch -l base -r -d 'base ref'
complete -c ciwatch -l files -r -d 'changed files'
complete -c ciwatch -l all -d 'all checks'
complete -c ciwatch -l failures -d 'failures only'
complete -c ciwatch -l gate -r -d 'gate job'
complete -c ciwatch -l json -d 'structured output'
`, nil
	}
	return "", fmt.Errorf("unknown shell %q — use bash, zsh, or fish", shell)
}
