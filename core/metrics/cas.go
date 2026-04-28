package metrics

import "sync/atomic"

// CASStats is a point-in-time snapshot of CAS operation counters.
type CASStats struct {
	PutCount uint64
	GetCount uint64
	HasCount uint64
	BytesPut uint64
	BytesGet uint64
}

// CASStatsRecorder records CAS counters with atomic updates.
type CASStatsRecorder struct {
	putCount atomic.Uint64
	getCount atomic.Uint64
	hasCount atomic.Uint64
	bytesPut atomic.Uint64
	bytesGet atomic.Uint64
}

// RecordPut records a Put call and the bytes accepted by storage.
func (r *CASStatsRecorder) RecordPut(bytes int) {
	r.putCount.Add(1)
	r.RecordPutBytes(bytes)
}

// RecordPutCall records a Put call without accepted bytes.
func (r *CASStatsRecorder) RecordPutCall() {
	r.putCount.Add(1)
}

// RecordPutBytes records bytes accepted by storage.
func (r *CASStatsRecorder) RecordPutBytes(bytes int) {
	if bytes > 0 {
		r.bytesPut.Add(uint64(bytes))
	}
}

// RecordGet records a Get call and the bytes returned by storage.
func (r *CASStatsRecorder) RecordGet(bytes int) {
	r.getCount.Add(1)
	r.RecordGetBytes(bytes)
}

// RecordGetBytes records bytes returned by storage.
func (r *CASStatsRecorder) RecordGetBytes(bytes int) {
	if bytes > 0 {
		r.bytesGet.Add(uint64(bytes))
	}
}

// RecordGetCall records a Get call without returned bytes.
func (r *CASStatsRecorder) RecordGetCall() {
	r.getCount.Add(1)
}

// RecordHasCall records a Has call.
func (r *CASStatsRecorder) RecordHasCall() {
	r.hasCount.Add(1)
}

// Snapshot returns the current CAS counters.
func (r *CASStatsRecorder) Snapshot() CASStats {
	return CASStats{
		PutCount: r.putCount.Load(),
		GetCount: r.getCount.Load(),
		HasCount: r.hasCount.Load(),
		BytesPut: r.bytesPut.Load(),
		BytesGet: r.bytesGet.Load(),
	}
}

// Reset clears all CAS counters.
func (r *CASStatsRecorder) Reset() {
	r.putCount.Store(0)
	r.getCount.Store(0)
	r.hasCount.Store(0)
	r.bytesPut.Store(0)
	r.bytesGet.Store(0)
}
