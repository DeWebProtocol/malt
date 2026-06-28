// Package store provides per-system evaluation storage and write accounting.
package store

import "sync"

// Category identifies the persisted-data class charged by the evaluator.
type Category string

const (
	CategoryCASPayload  Category = "cas_payload"
	CategoryCASMetadata Category = "cas_metadata"
	CategoryArcTable    Category = "arctable"
	CategoryCommitment  Category = "commitment"
	CategoryRootHead    Category = "root_head"
)

// Counter captures attempted writes and newly persisted writes for one class.
type Counter struct {
	AttemptedPutCount  uint64 `json:"attempted_put_count"`
	AttemptedPutBytes  uint64 `json:"attempted_put_bytes"`
	NewObjectCount     uint64 `json:"new_object_count"`
	NewPersistedBytes  uint64 `json:"new_persisted_bytes"`
	ChangedRecordCount uint64 `json:"changed_record_count,omitempty"`
}

// Snapshot is a stable copy of all accounting counters.
type Snapshot struct {
	Total      Counter              `json:"total"`
	Categories map[Category]Counter `json:"categories"`
}

// Meter records write-accounting counters for one evaluated system.
type Meter struct {
	mu         sync.Mutex
	categories map[Category]Counter
}

// NewMeter creates an empty write-accounting meter.
func NewMeter() *Meter {
	return &Meter{categories: make(map[Category]Counter)}
}

// RecordCASPut records a CAS put attempt. Only previously absent CAS blocks are
// charged as newly persisted objects.
func (m *Meter) RecordCASPut(category Category, bytes int, isNew bool) {
	if m == nil {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counter := m.categories[category]
	counter.AttemptedPutCount++
	counter.AttemptedPutBytes += uint64(bytes)
	if isNew {
		counter.NewObjectCount++
		counter.NewPersistedBytes += uint64(bytes)
	}
	m.categories[category] = counter
}

// RecordChangedRecord records a KV-style changed record. KV writes are charged
// on every successful put because overwrite and delta stores still persist a
// changed record for the evaluated commit.
func (m *Meter) RecordChangedRecord(category Category, keyBytes, valueBytes int) {
	if m == nil {
		return
	}
	if keyBytes < 0 {
		keyBytes = 0
	}
	if valueBytes < 0 {
		valueBytes = 0
	}
	bytes := uint64(keyBytes + valueBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	counter := m.categories[category]
	counter.AttemptedPutCount++
	counter.AttemptedPutBytes += bytes
	counter.NewObjectCount++
	counter.NewPersistedBytes += bytes
	counter.ChangedRecordCount++
	m.categories[category] = counter
}

// RecordLogicalBytes records non-CAS/KV metadata such as root publication bytes.
func (m *Meter) RecordLogicalBytes(category Category, bytes int) {
	if m == nil {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counter := m.categories[category]
	counter.AttemptedPutCount++
	counter.AttemptedPutBytes += uint64(bytes)
	counter.NewObjectCount++
	counter.NewPersistedBytes += uint64(bytes)
	counter.ChangedRecordCount++
	m.categories[category] = counter
}

// Snapshot returns a stable copy of the current counters.
func (m *Meter) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{Categories: map[Category]Counter{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Snapshot{Categories: make(map[Category]Counter, len(m.categories))}
	for category, counter := range m.categories {
		out.Categories[category] = counter
		out.Total.AttemptedPutCount += counter.AttemptedPutCount
		out.Total.AttemptedPutBytes += counter.AttemptedPutBytes
		out.Total.NewObjectCount += counter.NewObjectCount
		out.Total.NewPersistedBytes += counter.NewPersistedBytes
		out.Total.ChangedRecordCount += counter.ChangedRecordCount
	}
	return out
}

// Delta returns the non-negative counter difference between after and before.
// Missing categories are treated as zero-valued counters.
func Delta(after, before Snapshot) Snapshot {
	out := Snapshot{
		Total:      counterDelta(after.Total, before.Total),
		Categories: make(map[Category]Counter),
	}
	for category, afterCounter := range after.Categories {
		out.Categories[category] = counterDelta(afterCounter, before.Categories[category])
	}
	for category := range before.Categories {
		if _, ok := out.Categories[category]; !ok {
			out.Categories[category] = Counter{}
		}
	}
	return out
}

func counterDelta(after, before Counter) Counter {
	return Counter{
		AttemptedPutCount:  saturatedSub(after.AttemptedPutCount, before.AttemptedPutCount),
		AttemptedPutBytes:  saturatedSub(after.AttemptedPutBytes, before.AttemptedPutBytes),
		NewObjectCount:     saturatedSub(after.NewObjectCount, before.NewObjectCount),
		NewPersistedBytes:  saturatedSub(after.NewPersistedBytes, before.NewPersistedBytes),
		ChangedRecordCount: saturatedSub(after.ChangedRecordCount, before.ChangedRecordCount),
	}
}

func saturatedSub(after, before uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}
