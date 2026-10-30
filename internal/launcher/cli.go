package launcher

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Command 表达命令意图，由进程入口映射到 App 的运行能力。
type Command string

const (
	CommandAuto       Command = "auto" // 未指定子命令；先尝试客户端，收到回退信号才本地启动。
	CommandRun        Command = "run"
	CommandClient     Command = "client"
	CommandService    Command = "service"
	CommandCompletion Command = "completion"
)

type Options struct {
	ConfigPath string
	ClientName string
	Command    Command
	Completion string
	Help       bool
	Version    bool
}

func ParseArgs(args []string) (Options, error) {
	opts := Options{Command: CommandAuto}
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
		case arg == "--client" || arg == "-c":
			if i+1 >= len(args) {
				return Options{}, fmt.Errorf("%s requires a value", arg)
			}
			i++
			opts.ClientName = args[i]
		case strings.HasPrefix(arg, "--client="):
			opts.ClientName = strings.TrimPrefix(arg, "--client=")
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
		opts.Command = CommandRun
	case "client":
		if len(positionals) != 1 {
			return Options{}, fmt.Errorf("usage: elbot client [--config path] [-c name]")
		}
		opts.Command = CommandClient
	case "service":
		if len(positionals) != 2 || positionals[1] != "run" {
			return Options{}, fmt.Errorf("usage: elbot service run [--config path]")
		}
		opts.Command = CommandService
	case "completion":
		if len(positionals) != 2 || !SupportedCompletionShell(positionals[1]) {
			return Options{}, fmt.Errorf("usage: elbot completion [auto|bash|zsh|fish|nushell|powershell]")
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
	case "auto", "bash", "zsh", "fish", "nu", "nushell", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

func WriteUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  elbot [--config path] [-c name]
  elbot run [--config path]
  elbot client [--config path] [-c name]
  elbot service run [--config path]
  elbot completion [auto|bash|zsh|fish|nushell|powershell]

Default: CLI client → eligible local fallback.

Commands:
  run          Local TUI + platforms + background tasks.
  client       Connect to CLI server.
  service run  Headless service.
  completion   Shell completion.

Options:
  -c, --client name  Connection profile.
  --config path     TOML config.
  --help            Help.
  --version         Version.
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
	case "nu", "nushell":
		writeNushellCompletion(w)
	case "powershell", "pwsh":
		writePowerShellCompletion(w)
	default:
		return fmt.Errorf("cannot detect shell; specify bash, zsh, fish, nushell, or powershell")
	}
	return nil
}

func detectCompletionShell() string {
	shell := strings.ToLower(os.Getenv("SHELL"))
	switch {
	case strings.Contains(shell, "fish"):
		return "fish"
	case strings.Contains(shell, "nu"):
		return "nushell"
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
    -c|--client)
      return 0
      ;;
    completion)
      COMPREPLY=( $(compgen -W "auto bash zsh fish nushell powershell" -- "$cur") )
      return 0
      ;;
    service)
      COMPREPLY=( $(compgen -W "run" -- "$cur") )
      return 0
      ;;
  esac

  COMPREPLY=( $(compgen -W "run client service completion -c --client --config --help --version" -- "$cur") )
}
complete -F _elbot_completion elbot
`)
}

func writeZshCompletion(w io.Writer) {
	fmt.Fprint(w, `#compdef elbot
_arguments -C \
  '--config[Path to TOML config file]:config file:_files' \
  '(-c --client)'{-c,--client}'[CLI connection profile name]:profile:' \
  '--help[Show help]' \
  '--version[Show version]' \
  '1:command:(run client service completion)' \
  '2:subcommand:->subcmd'
case $words[2] in
  service)
    _values 'service command' run
    ;;
  completion)
    _values 'shell' auto bash zsh fish nushell powershell
    ;;
esac
`)
}

func writeFishCompletion(w io.Writer) {
	fmt.Fprint(w, `complete -c elbot -f
complete -c elbot -n "__fish_use_subcommand" -a "run" -d "Run local TUI with enabled platforms and background tasks"
complete -c elbot -n "__fish_use_subcommand" -a "client" -d "Run the CLI client and connect to a server"
complete -c elbot -n "__fish_use_subcommand" -a "service" -d "Service commands"
complete -c elbot -n "__fish_use_subcommand" -a "completion" -d "Generate shell completions"
complete -c elbot -n "__fish_seen_subcommand_from service" -a "run" -d "Run headless service mode"
complete -c elbot -n "__fish_seen_subcommand_from completion" -a "auto bash zsh fish nushell powershell"
complete -c elbot -l config -r -d "Path to TOML config file"
complete -c elbot -s c -l client -r -d "CLI connection profile name"
complete -c elbot -s h -l help -d "Show help"
complete -c elbot -l version -d "Show version"
`)
}

func writeNushellCompletion(w io.Writer) {
	fmt.Fprint(w, `def "nu-complete elbot commands" [] {
  [
    {value: "run", description: "Run local TUI with enabled platforms and background tasks"}
    {value: "client", description: "Run the CLI client and connect to a server"}
    {value: "service", description: "Service commands"}
    {value: "completion", description: "Generate shell completions"}
  ]
}

def "nu-complete elbot service" [] {
  [{value: "run", description: "Run headless service mode"}]
}

def "nu-complete elbot completion" [] {
  [auto bash zsh fish nushell powershell]
}

export extern "elbot" [
  command?: string@"nu-complete elbot commands"
  subcommand?: string
  --config: path
  --client(-c): string
  --help(-h)
  --version
]

export extern "elbot service" [
  command?: string@"nu-complete elbot service"
  --config: path
  --help(-h)
  --version
]

export extern "elbot completion" [
  shell?: string@"nu-complete elbot completion"
]
`)
}

func writePowerShellCompletion(w io.Writer) {
	fmt.Fprint(w, `Register-ArgumentCompleter -Native -CommandName elbot -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = $commandAst.CommandElements | ForEach-Object { $_.ToString() }
  $candidates = @('run', 'client', 'service', 'completion', '-c', '--client', '--config', '--help', '--version')
  if ($words -contains 'service') { $candidates = @('run') }
  if ($words -contains 'completion') { $candidates = @('auto', 'bash', 'zsh', 'fish', 'powershell') }
  $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`)
}
