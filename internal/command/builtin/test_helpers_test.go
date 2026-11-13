package builtin

import command "elbot/internal/command/share"

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }
