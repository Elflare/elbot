package launcher

import (
	"bytes"
	"strings"
	"testing"

	"elbot/internal/app"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMode   app.RunMode
		wantCmd    Command
		wantConfig string
		wantErr    bool
	}{
		{name: "default auto", wantMode: app.RunModeAuto, wantCmd: CommandRun},
		{name: "run", args: []string{"run"}, wantMode: app.RunModeFull, wantCmd: CommandRun},
		{name: "cli", args: []string{"cli"}, wantMode: app.RunModeCLIOnly, wantCmd: CommandRun},
		{name: "service run", args: []string{"service", "run"}, wantMode: app.RunModeService, wantCmd: CommandRun},
		{name: "completion fish", args: []string{"completion", "fish"}, wantMode: app.RunModeAuto, wantCmd: CommandCompletion},
		{name: "completion bash", args: []string{"completion", "bash"}, wantMode: app.RunModeAuto, wantCmd: CommandCompletion},
		{name: "completion nushell", args: []string{"completion", "nushell"}, wantMode: app.RunModeAuto, wantCmd: CommandCompletion},
		{name: "go run separator", args: []string{"--", "completion", "fish"}, wantMode: app.RunModeAuto, wantCmd: CommandCompletion},
		{name: "config before command", args: []string{"--config", "config/app.toml", "run"}, wantMode: app.RunModeFull, wantCmd: CommandRun, wantConfig: "config/app.toml"},
		{name: "config after command", args: []string{"cli", "--config=config/app.toml"}, wantMode: app.RunModeCLIOnly, wantCmd: CommandRun, wantConfig: "config/app.toml"},
		{name: "unknown command", args: []string{"wat"}, wantErr: true},
		{name: "unknown service action", args: []string{"service", "stop"}, wantErr: true},
		{name: "unknown completion shell", args: []string{"completion", "csh"}, wantErr: true},
		{name: "missing config", args: []string{"--config"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseArgs() error = %v", err)
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.Command != tt.wantCmd {
				t.Fatalf("Command = %q, want %q", got.Command, tt.wantCmd)
			}
			if got.ConfigPath != tt.wantConfig {
				t.Fatalf("ConfigPath = %q, want %q", got.ConfigPath, tt.wantConfig)
			}
		})
	}
}

func TestWriteCompletionShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "nu", "nushell", "powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteCompletion(&buf, shell); err != nil {
				t.Fatalf("WriteCompletion() error = %v", err)
			}
			if !strings.Contains(buf.String(), "elbot") {
				t.Fatalf("completion output does not mention elbot: %q", buf.String())
			}
		})
	}
}
