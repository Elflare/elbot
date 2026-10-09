package session

import "elbot/internal/signal"

type NamingSignals struct {
	Scheduled *signal.Signal[NamingScheduledEvent]
	Completed *signal.Signal[NamingCompletedEvent]
}

func newNamingSignals() NamingSignals {
	return NamingSignals{
		Scheduled: signal.New[NamingScheduledEvent]("session.naming_scheduled"),
		Completed: signal.New[NamingCompletedEvent]("session.naming_completed"),
	}
}

func (s *Service) NamingSignals() NamingSignals { return s.namingSignals }
