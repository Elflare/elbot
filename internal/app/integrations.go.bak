package app

import "context"

// connectModules binds cross-line callbacks only, after every owner has constructed its children.
func connectModules(c *WiringComponents) error {
	c.Hook.SetWakeup(c.Dialogue.Handler().HookWakeup)
	c.Hook.SetObserver(c.Dialogue.ObserveHookRun())
	c.Platform.SetCompleter(c.Dialogue.Completion())
	c.Platform.SetCommandCatalog(c.Dialogue.Commands())
	return c.Platform.ConnectUI(c.Dialogue.StatusChanged(), c.Dialogue.Sessions().BindingChanged())
}

// Start publishes only the fully connected graph. Platform loops launch before background scheduling.
func (c *WiringComponents) Start(stop context.CancelFunc) error {
	if err := c.Tool.Start(); err != nil {
		return err
	}
	if err := c.Dialogue.Start(); err != nil {
		return err
	}
	if err := c.Hook.Start(); err != nil {
		return err
	}
	if err := c.Platform.Start(c.Dialogue.Handler(), stop); err != nil {
		return err
	}
	return c.Background.Start()
}
