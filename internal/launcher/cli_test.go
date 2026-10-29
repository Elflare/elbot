package launcher

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCmd    Command
		wantConfig string
		wantClient string
		wantErr    bool
	}{
		{name: "default auto", wantCmd: CommandAuto},
		{name: "run", args: []string{"run"}, wantCmd: CommandRun},
		{name: "client", args: []string{"client"}, wantCmd: CommandClient},
		{name: "service run", args: []string{"service", "run"}, wantCmd: CommandService},
		{name: "completion fish", args: []string{"completion", "fish"}, wantCmd: CommandCompletion},
		{name: "completion bash", args: []string{"completion", "bash"}, wantCmd: CommandCompletion},
		{name: "completion nushell", args: []string{"completion", "nushell"}, wantCmd: CommandCompletion},
		{name: "go run separator", args: []string{"--", "completion", "fish"}, wantCmd: CommandCompletion},
		{name: "config before command", args: []string{"--config", "custom/app.toml", "run"}, wantCmd: CommandRun, wantConfig: "custom/app.toml"},
		{name: "config after command", args: []string{"client", "--config=custom/app.toml"}, wantCmd: CommandClient, wantConfig: "custom/app.toml"},
		{name: "client before command", args: []string{"-c", "local", "client"}, wantCmd: CommandClient, wantClient: "local"},
		{name: "client long option", args: []string{"client", "--client", "local"}, wantCmd: CommandClient, wantClient: "local"},
		{name: "client equals", args: []string{"--client=windows"}, wantCmd: CommandAuto, wantClient: "windows"},
		{name: "old cli rejected", args: []string{"cli"}, wantErr: true},
		{name: "extra client argument", args: []string{"client", "local"}, wantErr: true},
		{name: "missing client", args: []string{"--client"}, wantErr: true},
		{name: "name unsupported", args: []string{"--name", "local"}, wantErr: true},
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
			if got.Command != tt.wantCmd {
				t.Fatalf("Command = %q, want %q", got.Command, tt.wantCmd)
			}
			if got.ConfigPath != tt.wantConfig {
				t.Fatalf("ConfigPath = %q, want %q", got.ConfigPath, tt.wantConfig)
			}
			if got.ClientName != tt.wantClient {
				t.Fatalf("ClientName = %q, want %q", got.ClientName, tt.wantClient)
			}
		})
	}
}

func TestWriteCompletionShells(t *testing.T) {
	for shell, option := range map[string]string{
		"bash": "--client", "zsh": "--client", "fish": "-s c -l client",
		"nu": "--client(-c)", "nushell": "--client(-c)", "powershell": "--client", "pwsh": "--client",
	} {
		t.Run(shell, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteCompletion(&buf, shell); err != nil {
				t.Fatalf("WriteCompletion() error = %v", err)
			}
			if !strings.Contains(buf.String(), "elbot") {
				t.Fatalf("completion output does not mention elbot: %q", buf.String())
			}
			for _, old := range []string{"run cli ", "\"cli\"", "'cli'"} {
				if strings.Contains(buf.String(), old) {
					t.Fatalf("completion advertises removed cli command: %q", buf.String())
				}
			}
			for _, want := range []string{"client", option} {
				if !strings.Contains(buf.String(), want) {
					t.Fatalf("completion missing %q: %q", want, buf.String())
				}
			}
		})
	}
}

func TestUsageAdvertisesClient(t *testing.T) {
	var buf bytes.Buffer
	WriteUsage(&buf)
	if !strings.Contains(buf.String(), "elbot client ") || strings.Contains(buf.String(), "elbot cli ") {
		t.Fatal(buf.String())
	}
}
