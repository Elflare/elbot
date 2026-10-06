package agent

import (
	"fmt"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
)

// Only composition chooses concrete protocols. Runtime consumers query the
// sealed provider bindings or the saved source-material identity.
func bindProviderRoutes(registry *routes.Registry, models *modelmgr.Service, chat, response dialogue.Loop, compactor contextmgr.Compactor) error {
	if err := registry.RegisterCompactor(llm.ProtocolChat, compactor); err != nil {
		return err
	}
	for _, origin := range models.ProviderOrigins() {
		client := models.ClientForProvider(origin.Provider)
		binding := routes.Binding{Origin: origin, Client: client}
		switch origin.Protocol {
		case llm.ProtocolChat:
			if _, ok := client.(chatcompletions.Streamer); !ok {
				return fmt.Errorf("provider %q api_mode %q requires the Chat streaming capability", origin.Provider, origin.Protocol)
			}
			binding.Loop, binding.Compactor = chat, compactor
		case llm.ProtocolResponse:
			if _, ok := client.(responses.Streamer); !ok {
				return fmt.Errorf("provider %q api_mode %q requires the Responses streaming capability", origin.Provider, origin.Protocol)
			}
			binding.Loop = response
			// TODO Step16.4: install the native source compactor.
		default:
			return fmt.Errorf("provider %q has unsupported api_mode %q", origin.Provider, origin.Protocol)
		}
		if err := registry.Register(binding); err != nil {
			return err
		}
	}
	return registry.Seal()
}
