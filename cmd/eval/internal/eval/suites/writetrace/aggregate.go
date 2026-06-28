package writetrace

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
)

type aggregateRow struct {
	Repo                       string
	System                     string
	Commits                    int
	FinalLivePayloadBytes      int64
	LogicalChangedPayloadBytes int64
	PhysicalPersistedBytes     uint64
	PhysicalPayloadBytes       uint64
	PhysicalMetadataBytes      uint64
	ArcTablePersistedBytes     uint64
	CASMetadataPersistedBytes  uint64
	RootHeadPersistedBytes     uint64
	CommitmentPersistedBytes   uint64
	CumulativeWriteAmp         float64
	MedianWriteAmp             float64
	P95WriteAmp                float64
	AddCount                   int
	ModifyCount                int
	DeleteCount                int
	RenameCount                int
}

type aggregateKey struct {
	repo   string
	system string
}

type aggregateAccumulator struct {
	row       aggregateRow
	waSamples []float64
	seen      map[string]struct{}
}

func aggregateRecords(records []replay.ResultRecord) []aggregateRow {
	groups := make(map[aggregateKey]*aggregateAccumulator)
	for _, record := range records {
		key := aggregateKey{repo: record.Repo, system: record.System}
		acc := groups[key]
		if acc == nil {
			acc = &aggregateAccumulator{
				row: aggregateRow{
					Repo:   record.Repo,
					System: record.System,
				},
				seen: make(map[string]struct{}),
			}
			groups[key] = acc
		}
		if _, ok := acc.seen[record.Commit]; !ok {
			acc.seen[record.Commit] = struct{}{}
			acc.row.Commits++
		}
		acc.row.FinalLivePayloadBytes = record.LiveStats.LivePayloadBytes
		acc.row.LogicalChangedPayloadBytes += record.LogicalChangedPayloadBytes
		acc.row.PhysicalPersistedBytes += record.PhysicalPersistedBytes
		acc.row.PhysicalPayloadBytes += record.PhysicalPayloadBytes
		acc.row.PhysicalMetadataBytes += record.PhysicalMetadataBytes
		acc.row.ArcTablePersistedBytes += persistedBytes(record, evalstore.CategoryArcTable)
		acc.row.CASMetadataPersistedBytes += persistedBytes(record, evalstore.CategoryCASMetadata)
		acc.row.RootHeadPersistedBytes += persistedBytes(record, evalstore.CategoryRootHead)
		acc.row.CommitmentPersistedBytes += persistedBytes(record, evalstore.CategoryCommitment)
		if record.WriteAmplification != nil {
			acc.waSamples = append(acc.waSamples, *record.WriteAmplification)
		}
		for _, mutation := range record.MutationSet {
			switch mutation.Kind {
			case replay.MutationAdd:
				acc.row.AddCount++
			case replay.MutationModify:
				acc.row.ModifyCount++
			case replay.MutationDelete:
				acc.row.DeleteCount++
			case replay.MutationRename:
				acc.row.RenameCount++
			}
		}
	}

	keys := make([]aggregateKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].repo != keys[j].repo {
			return keys[i].repo < keys[j].repo
		}
		return keys[i].system < keys[j].system
	})

	rows := make([]aggregateRow, 0, len(keys))
	for _, key := range keys {
		acc := groups[key]
		if acc.row.LogicalChangedPayloadBytes > 0 {
			acc.row.CumulativeWriteAmp = float64(acc.row.PhysicalPersistedBytes) / float64(acc.row.LogicalChangedPayloadBytes)
		}
		acc.row.MedianWriteAmp = percentile(acc.waSamples, 0.50)
		acc.row.P95WriteAmp = percentile(acc.waSamples, 0.95)
		rows = append(rows, acc.row)
	}
	return rows
}

func writeAggregateCSV(env framework.Env, rows []aggregateRow) error {
	path := aggregatePath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create aggregate dir: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create aggregate csv: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	header := []string{
		"repo",
		"system",
		"commits",
		"final_live_payload_bytes",
		"logical_changed_payload_bytes",
		"physical_persisted_bytes",
		"physical_payload_bytes",
		"physical_metadata_bytes",
		"arctable_persisted_bytes",
		"cas_metadata_persisted_bytes",
		"root_head_persisted_bytes",
		"commitment_persisted_bytes",
		"cumulative_write_amplification",
		"median_write_amplification",
		"p95_write_amplification",
		"add_count",
		"modify_count",
		"delete_count",
		"rename_count",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{
			row.Repo,
			row.System,
			strconv.Itoa(row.Commits),
			strconv.FormatInt(row.FinalLivePayloadBytes, 10),
			strconv.FormatInt(row.LogicalChangedPayloadBytes, 10),
			strconv.FormatUint(row.PhysicalPersistedBytes, 10),
			strconv.FormatUint(row.PhysicalPayloadBytes, 10),
			strconv.FormatUint(row.PhysicalMetadataBytes, 10),
			strconv.FormatUint(row.ArcTablePersistedBytes, 10),
			strconv.FormatUint(row.CASMetadataPersistedBytes, 10),
			strconv.FormatUint(row.RootHeadPersistedBytes, 10),
			strconv.FormatUint(row.CommitmentPersistedBytes, 10),
			formatFloat(row.CumulativeWriteAmp),
			formatFloat(row.MedianWriteAmp),
			formatFloat(row.P95WriteAmp),
			strconv.Itoa(row.AddCount),
			strconv.Itoa(row.ModifyCount),
			strconv.Itoa(row.DeleteCount),
			strconv.Itoa(row.RenameCount),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write aggregate csv: %w", err)
	}
	return nil
}

func aggregatePath(env framework.Env) string {
	return filepath.Join(env.ResultDir, "aggregate", SuiteName+".csv")
}

func persistedBytes(record replay.ResultRecord, category evalstore.Category) uint64 {
	return record.AccountingDelta.Categories[category].NewPersistedBytes
}

func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(q*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
