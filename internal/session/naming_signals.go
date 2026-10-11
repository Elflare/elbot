package session

import "elbot/internal/signal"

type NamingSignals struct {
	Scheduled *signal.Signal[NamingScheduledEvent, signal.Empty]
	Completed *signal.Signal[NamingCompletedEvent, signal.Empty]
}

func newNamingSignals() NamingSignals {
	return NamingSignals{
		Scheduled: signal.New[NamingScheduledEvent, signal.Empty]("session.naming_scheduled"),
		Completed: signal.New[NamingCompletedEvent, signal.Empty]("session.naming_completed"),
	}
}

func (s *Service) NamingSignals() NamingSignals { return s.namingSignals }
