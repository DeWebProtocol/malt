package writetrace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
)

type rawRecordKey struct {
	repo   string
	system string
	index  int
}

func loadRawProgress(path string) (map[taskKey]taskProgress, error) {
	progress := make(map[taskKey]taskProgress)
	err := readRawRecords(path, func(record replay.ResultRecord) {
		key := taskKey{repo: record.Repo, system: record.System}
		current, ok := progress[key]
		if !ok || record.Index > current.Index {
			progress[key] = taskProgress{
				Repo:   record.Repo,
				System: record.System,
				Index:  record.Index,
				Commit: record.Commit,
				Root:   record.Result.Root,
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return progress, nil
}

func aggregateRawRecords(path string) ([]aggregateRow, error) {
	records := make(map[rawRecordKey]replay.ResultRecord)
	err := readRawRecords(path, func(record replay.ResultRecord) {
		records[rawRecordKey{repo: record.Repo, system: record.System, index: record.Index}] = record
	})
	if err != nil {
		return nil, err
	}
	keys := make([]rawRecordKey, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].repo != keys[j].repo {
			return keys[i].repo < keys[j].repo
		}
		if keys[i].system != keys[j].system {
			return keys[i].system < keys[j].system
		}
		return keys[i].index < keys[j].index
	})
	collector := newAggregateCollector()
	for _, key := range keys {
		collector.Observe(records[key])
	}
	return collector.Rows(), nil
}

func readRawRecords(path string, visit func(replay.ResultRecord)) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	for {
		var envelope framework.RecordEnvelope
		if err := dec.Decode(&envelope); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode raw envelope %s: %w", path, err)
		}
		if envelope.Suite != SuiteName {
			continue
		}
		var record replay.ResultRecord
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			return fmt.Errorf("decode write_trace record %s: %w", path, err)
		}
		visit(record)
	}
}
