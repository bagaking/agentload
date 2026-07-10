package main

type SessionMetricFacts struct {
	Role             string
	KnownSession     bool
	RecentMovement   bool
	RecentSession    bool
	StaleSession     bool
	ProcessPressure  int
	ProcessIDs       []int
	ProcessResources ProcessResourceTotals
}

type ProcessResourceTotals struct {
	CPUPercent  float64
	MemoryBytes int64
}

type ProcessEvidenceMetricFacts struct {
	Role           string
	RecentMovement bool
	KnownSession   bool
}

// sessionNeedsReviewObservation is the backend source of truth for the
// "needs human review" cue: a main session whose local log shows no recent
// movement and whose freshness observation is idle or stale. It describes
// observed evidence only; it is not a judgment that the session is stuck.
func sessionNeedsReviewObservation(role string, observation liveSessionObservation) bool {
	if normalizedRole(role) != "main" || observation.ActiveBurst {
		return false
	}
	return observation.Freshness == "idle" || observation.Freshness == "stale"
}

func metricFactsForLiveSession(session LiveSession, observation liveSessionObservation) SessionMetricFacts {
	role := observeSessionRole(session)
	processIDs := make([]int, 0, len(session.Processes))
	for pid := range session.Processes {
		processIDs = append(processIDs, pid)
	}
	return SessionMetricFacts{
		Role:            role.Role,
		KnownSession:    true,
		RecentMovement:  observation.ActiveBurst,
		RecentSession:   observation.Recent,
		StaleSession:    observation.Stale,
		ProcessPressure: len(session.Processes),
		ProcessIDs:      processIDs,
	}
}

func metricFactsForSessionSnapshot(session LiveSessionSnapshot) SessionMetricFacts {
	return SessionMetricFacts{
		Role:            session.SessionRole,
		KnownSession:    true,
		RecentMovement:  session.ActiveBurst,
		ProcessPressure: session.ProcessCount,
		ProcessResources: ProcessResourceTotals{
			CPUPercent:  session.ProcessCPUPercent,
			MemoryBytes: session.ProcessMemoryBytes,
		},
	}
}

func metricFactsForProcessSessionEvidence(evidence ProcessSessionEvidence) ProcessEvidenceMetricFacts {
	return ProcessEvidenceMetricFacts{
		Role:           normalizedRole(evidence.Role),
		RecentMovement: evidence.ActiveBurst,
		KnownSession:   evidence.SessionID != "",
	}
}
