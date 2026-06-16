package launcher

import (
	"fmt"
	"io"
	"os"
	"strings"

	"elbot/internal/app"
)

type Command string

const (
	CommandRun        Command = "run"
	CommandCompletion Command = "completion"
)

type Options struct {
	ConfigPath string
	Mode       app.RunMode
	Command    Command
	Completion string
	Help       bool
	Version    bool
}

func ParseArgs(args []string) (Options, error) {
	opts := Options{Mode: app.RunModeAuto, Command: CommandRun}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			continue
		case arg == "--help" || arg == "-h":
			opts.Help = true
		case arg == "--version":
			opts.Version = true
		case arg == "--config" || arg == "-config":
			if i+1 >= len(args) {
				return Options{}, fmt.Errorf("%s requires a value", arg)
			}
			i++
			opts.ConfigPath = args[i]
		case strings.HasPrefix(arg, "--config="):
			opts.ConfigPath = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-"):
			return Options{}, fmt.Errorf("unknown option: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if opts.Help || opts.Version {
		return opts, nil
	}
	if len(positionals) == 0 {
		return opts, nil
	}

	switch positionals[0] {
	case "run":
		if len(positionals) != 1 {
			return Options{}, fmt.Errorf("usage: elbot run [--config path]")
		}
		opts.Mode = app.RunModeFull
	case "cli":
		if len(positionals) != 1 {
			return Options{}, fmt.Errorf("usage: elbot cli [--config path]")
		}
		opts.Mode = app.RunModeCLIOnly
	case "service":
		if len(positionals) != 2 || positionals[1] != "run" {
			return Options{}, fmt.Errorf("usage: elbot service run [--config path]")
		}
		opts.Mode = app.RunModeService
	case "completion":
		if len(positionals) != 2 || !SupportedCompletionShell(positionals[1]) {
			return Options{}, fmt.Errorf("usage: elbot completion [auto|bash|zsh|fish|powershell]")
		}
		opts.Command = CommandCompletion
		opts.Completion = positionals[1]
	default:
		return Options{}, fmt.Errorf("unknown command: %s", positionals[0])
	}
	return opts, nil
}

func SupportedCompletionShell(shell string) bool {
	switch shell {
	case "auto", "bash", "zsh", "fish", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

func WriteUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  elbot [--config path]
  elbot run [--config path]
  elbot cli [--config path]
  elbot service run [--config path]
  elbot completion [auto|bash|zsh|fish|powershell]

Commands:
  run          Run full foreground mode: CLI plus enabled platforms and cron.
  cli          Run local CLI-only mode: CLI without background platforms or cron.
  service run  Run headless service mode for Linux service managers.
  completion   Generate shell completion scripts.

Options:
  --config path  Path to TOML config file.
  --help         Show this help.
  --version      Show version.
`)
}

func WriteCompletion(w io.Writer, shell string) error {
	if shell == "auto" {
		shell = detectCompletionShell()
	}
	switch shell {
	case "bash":
		writeBashCompletion(w)
	case "zsh":
		writeZshCompletion(w)
	case "fish":
		writeFishCompletion(w)
	case "powershell", "pwsh":
		writePowerShellCompletion(w)
	default:
		return fmt.Errorf("cannot detect shell; specify bash, zsh, fish, or powershell")
	}
	return nil
}

func detectCompletionShell() string {
	shell := strings.ToLower(os.Getenv("SHELL"))
	switch {
	case strings.Contains(shell, "fish"):
		return "fish"
	case strings.Contains(shell, "zsh"):
		return "zsh"
	case strings.Contains(shell, "bash"):
		return "bash"
	}
	if strings.Contains(strings.ToLower(os.Getenv("PSModulePath")), "powershell") {
		return "powershell"
	}
	return ""
}

func writeBashCompletion(w io.Writer) {
	fmt.Fprint(w, `# bash completion for elbot
_elbot_completion() {
  local cur prev
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"

  case "$prev" in
    --config)
      COMPREPLY=( $(compgen -f -- "$cur") )
      return 0
      ;;
    completion)
      COMPREPLY=( $(compgen -W "auto bash zsh fish powershell" -- "$cur") )
      return 0
      ;;
    service)
      COMPREPLY=( $(compgen -W "run" -- "$cur") )
      return 0
      ;;
  esac

  COMPREPLY=( $(compgen -W "run cli service completion --config --help --version" -- "$cur") )
}
complete -F _elbot_completion elbot
`)
}

func writeZshCompletion(w io.Writer) {
	fmt.Fprint(w, `#compdef elbot
_arguments -C \
  '--config[Path to TOML config file]:config file:_files' \
  '--help[Show help]' \
  '--version[Show version]' \
  '1:command:(run cli service completion)' \
  '2:subcommand:->subcmd'
case $words[2] in
  service)
    _values 'service command' run
    ;;
  completion)
    _values 'shell' auto bash zsh fish powershell
    ;;
esac
`)
}

func writeFishCompletion(w io.Writer) {
	fmt.Fprint(w, `complete -c elbot -f
complete -c elbot -n "__fish_use_subcommand" -a "run" -d "Run full foreground mode"
complete -c elbot -n "__fish_use_subcommand" -a "cli" -d "Run local CLI-only mode"
complete -c elbot -n "__fish_use_subcommand" -a "service" -d "Service commands"
complete -c elbot -n "__fish_use_subcommand" -a "completion" -d "Generate shell completions"
complete -c elbot -n "__fish_seen_subcommand_from service" -a "run" -d "Run headless service mode"
complete -c elbot -n "__fish_seen_subcommand_from completion" -a "auto bash zsh fish powershell"
complete -c elbot -l config -r -d "Path to TOML config file"
complete -c elbot -s h -l help -d "Show help"
complete -c elbot -l version -d "Show version"
`)
}

func writePowerShellCompletion(w io.Writer) {
	fmt.Fprint(w, `Register-ArgumentCompleter -Native -CommandName elbot -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = $commandAst.CommandElements | ForEach-Object { $_.ToString() }
  $candidates = @('run', 'cli', 'service', 'completion', '--config', '--help', '--version')
  if ($words -contains 'service') { $candidates = @('run') }
  if ($words -contains 'completion') { $candidates = @('auto', 'bash', 'zsh', 'fish', 'powershell') }
  $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`)
}
