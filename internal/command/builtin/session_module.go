package builtin

type SessionModule struct{}

func (SessionModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewNew,
		NewStatus,
		NewSessions,
		NewArchives,
		NewArchive,
		NewUnarchive,
		NewPin,
		NewUnpin,
		NewRename,
		NewDelete,
		NewClean,
		NewResume,
		NewMessages,
		NewFork,
		NewWork,
		NewChat,
	)
}
