package metrics

import "github.com/dewebprotocol/malt/storage/cas"

// CASStats is the runtime metrics projection of storage CAS counters.
type CASStats = cas.Stats

// CASStatsRecorder records CAS counters with atomic updates.
type CASStatsRecorder = cas.StatsRecorder
