package cas

import "testing"

func TestStatsRecorderSnapshotsAllCASCounterFields(t *testing.T) {
	var recorder StatsRecorder

	recorder.RecordPut(11)
	recorder.RecordPutCall()
	recorder.RecordPutBytes(13)
	recorder.RecordGet(17)
	recorder.RecordGetCall()
	recorder.RecordGetBytes(19)
	recorder.RecordHasCall()

	got := recorder.Snapshot()
	want := Stats{
		PutCount: 2,
		GetCount: 2,
		HasCount: 1,
		BytesPut: 24,
		BytesGet: 36,
	}
	if got != want {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}

	recorder.Reset()
	if got := recorder.Snapshot(); got != (Stats{}) {
		t.Fatalf("Snapshot() after Reset = %+v, want zero", got)
	}
}
