package session

import "elbot/internal/signal"

type NamingSignals struct {
	Scheduled *signal.Signal[NamingScheduledEvent]
	Completed *signal.Signal[NamingCompletedEvent]
	Failed    *signal.Signal[NamingFailedEvent]
}

func newNamingSignals() NamingSignals {
	return NamingSignals{
		Scheduled: signal.New[NamingScheduledEvent]("session.naming_scheduled", nil),
		Completed: signal.New[NamingCompletedEvent]("session.naming_completed", nil),
		Failed:    signal.New[NamingFailedEvent]("session.naming_failed", nil),
	}
}

func (s *Service) NamingSignals() NamingSignals { return s.namingSignals }
