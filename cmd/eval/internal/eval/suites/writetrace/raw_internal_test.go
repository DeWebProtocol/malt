package writetrace

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
)

func TestRepairRawTailFileReturnsCloseError(t *testing.T) {
	envelope := framework.RecordEnvelope{
		SchemaVersion: framework.SchemaVersion,
		RunID:         "run",
		Suite:         SuiteName,
		EmittedAt:     "2026-07-05T00:00:00Z",
		Record:        json.RawMessage(`{"repo":"repo","system":"maltflat","index":0}`),
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	line = append(line, '\n')
	closeErr := errors.New("close failed")

	err = repairRawTailFile("raw.jsonl", &closeErrorRawFile{
		Reader:   bytes.NewReader(line),
		closeErr: closeErr,
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("repairRawTailFile error = %v, want close error", err)
	}
}

type closeErrorRawFile struct {
	*bytes.Reader
	closeErr error
}

func (f *closeErrorRawFile) Write(p []byte) (int, error) {
	return len(p), nil
}

func (f *closeErrorRawFile) Truncate(int64) error {
	return nil
}

func (f *closeErrorRawFile) Close() error {
	return f.closeErr
}

var _ rawRepairFile = (*closeErrorRawFile)(nil)

func TestRepairRawTailFileTruncatesOnlyFinalPartialLine(t *testing.T) {
	valid := `{"schema_version":"` + framework.SchemaVersion + `","suite":"` + SuiteName + `","record":{"repo":"repo","system":"maltflat","index":0}}` + "\n"
	file := &trackingRawFile{Reader: bytes.NewReader([]byte(valid + `{"schema_version":"`))}

	if err := repairRawTailFile("raw.jsonl", file); err != nil {
		t.Fatalf("repairRawTailFile: %v", err)
	}
	if file.truncateAt == nil {
		t.Fatal("repairRawTailFile did not truncate partial tail")
	}
	if got := *file.truncateAt; got != int64(len(valid)) {
		t.Fatalf("truncate offset = %d, want %d", got, len(valid))
	}
}

type trackingRawFile struct {
	*bytes.Reader
	truncateAt *int64
	writes     strings.Builder
}

func (f *trackingRawFile) Write(p []byte) (int, error) {
	return f.writes.Write(p)
}

func (f *trackingRawFile) Truncate(size int64) error {
	f.truncateAt = &size
	return nil
}

func (f *trackingRawFile) Close() error {
	return nil
}

var _ rawRepairFile = (*trackingRawFile)(nil)
