package readmatrix

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
)

type aggregateRow struct {
	System                          readbench.SystemName
	Workload                        readbench.WorkloadKind
	PathDepth                       int
	CASLatencyMS                    int
	Samples                         int
	MedianElapsedNS                 int64
	P95ElapsedNS                    int64
	MedianCASGetCount               uint64
	P95CASGetCount                  uint64
	MedianEvidenceItemCount         uint64
	P95EvidenceItemCount            uint64
	MedianProofListStepCount        uint64
	P95ProofListStepCount           uint64
	MedianArcTableBatchGetCount     uint64
	P95ArcTableBatchGetCount        uint64
	MedianArcTableBatchGetPathCount uint64
	P95ArcTableBatchGetPathCount    uint64
}

type aggregateKey struct {
	system       readbench.SystemName
	workload     readbench.WorkloadKind
	pathDepth    int
	casLatencyMS int
}

func aggregateResults(results []readbench.Result) []aggregateRow {
	groups := make(map[aggregateKey][]readbench.Result)
	for _, result := range results {
		key := aggregateKey{
			system:       result.System,
			workload:     result.Workload,
			pathDepth:    result.PathDepth,
			casLatencyMS: result.CASLatencyMS,
		}
		groups[key] = append(groups[key], result)
	}

	keys := make([]aggregateKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.system != b.system {
			return a.system < b.system
		}
		if a.workload != b.workload {
			return a.workload < b.workload
		}
		if a.pathDepth != b.pathDepth {
			return a.pathDepth < b.pathDepth
		}
		return a.casLatencyMS < b.casLatencyMS
	})

	rows := make([]aggregateRow, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		rows = append(rows, aggregateRow{
			System:                          key.system,
			Workload:                        key.workload,
			PathDepth:                       key.pathDepth,
			CASLatencyMS:                    key.casLatencyMS,
			Samples:                         len(group),
			MedianElapsedNS:                 medianInt64(mapElapsedNS(group)),
			P95ElapsedNS:                    p95Int64(mapElapsedNS(group)),
			MedianCASGetCount:               medianUint64(mapCASGetCount(group)),
			P95CASGetCount:                  p95Uint64(mapCASGetCount(group)),
			MedianEvidenceItemCount:         medianUint64(mapEvidenceItemCount(group)),
			P95EvidenceItemCount:            p95Uint64(mapEvidenceItemCount(group)),
			MedianProofListStepCount:        medianUint64(mapProofListStepCount(group)),
			P95ProofListStepCount:           p95Uint64(mapProofListStepCount(group)),
			MedianArcTableBatchGetCount:     medianUint64(mapArcTableBatchGetCount(group)),
			P95ArcTableBatchGetCount:        p95Uint64(mapArcTableBatchGetCount(group)),
			MedianArcTableBatchGetPathCount: medianUint64(mapArcTableBatchGetPathCount(group)),
			P95ArcTableBatchGetPathCount:    p95Uint64(mapArcTableBatchGetPathCount(group)),
		})
	}
	return rows
}

func writeAggregateCSV(env framework.Env, suite string, rows []aggregateRow) error {
	path := aggregatePath(env, suite)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	header := []string{
		"system",
		"workload",
		"path_depth",
		"cas_latency_ms",
		"samples",
		"median_elapsed_ns",
		"p95_elapsed_ns",
		"median_cas_get_count",
		"p95_cas_get_count",
		"median_evidence_item_count",
		"p95_evidence_item_count",
		"median_prooflist_step_count",
		"p95_prooflist_step_count",
		"median_arctable_batch_get_count",
		"p95_arctable_batch_get_count",
		"median_arctable_batch_get_path_count",
		"p95_arctable_batch_get_path_count",
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{
			string(row.System),
			string(row.Workload),
			strconv.Itoa(row.PathDepth),
			strconv.Itoa(row.CASLatencyMS),
			strconv.Itoa(row.Samples),
			strconv.FormatInt(row.MedianElapsedNS, 10),
			strconv.FormatInt(row.P95ElapsedNS, 10),
			strconv.FormatUint(row.MedianCASGetCount, 10),
			strconv.FormatUint(row.P95CASGetCount, 10),
			strconv.FormatUint(row.MedianEvidenceItemCount, 10),
			strconv.FormatUint(row.P95EvidenceItemCount, 10),
			strconv.FormatUint(row.MedianProofListStepCount, 10),
			strconv.FormatUint(row.P95ProofListStepCount, 10),
			strconv.FormatUint(row.MedianArcTableBatchGetCount, 10),
			strconv.FormatUint(row.P95ArcTableBatchGetCount, 10),
			strconv.FormatUint(row.MedianArcTableBatchGetPathCount, 10),
			strconv.FormatUint(row.P95ArcTableBatchGetPathCount, 10),
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return nil
}

func aggregatePath(env framework.Env, suite string) string {
	base := env.ResultDir
	if base == "" {
		base = env.OutputDir
	}
	return filepath.Join(base, "aggregate", suite+".csv")
}

func mapElapsedNS(results []readbench.Result) []int64 {
	out := make([]int64, len(results))
	for i, result := range results {
		out[i] = result.ElapsedNS
	}
	return out
}

func mapCASGetCount(results []readbench.Result) []uint64 {
	out := make([]uint64, len(results))
	for i, result := range results {
		out[i] = result.CAS.GetCount
	}
	return out
}

func mapEvidenceItemCount(results []readbench.Result) []uint64 {
	out := make([]uint64, len(results))
	for i, result := range results {
		out[i] = uint64(result.EvidenceItemCount)
	}
	return out
}

func mapProofListStepCount(results []readbench.Result) []uint64 {
	out := make([]uint64, len(results))
	for i, result := range results {
		out[i] = uint64(result.ProofListStepCount)
	}
	return out
}

func mapArcTableBatchGetCount(results []readbench.Result) []uint64 {
	out := make([]uint64, len(results))
	for i, result := range results {
		out[i] = result.ArcTable.BatchGetCount
	}
	return out
}

func mapArcTableBatchGetPathCount(results []readbench.Result) []uint64 {
	out := make([]uint64, len(results))
	for i, result := range results {
		out[i] = result.ArcTable.BatchGetPathCount
	}
	return out
}

func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func p95Int64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[percentileIndex(len(sorted), 95)]
}

func medianUint64(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]uint64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func p95Uint64(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]uint64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[percentileIndex(len(sorted), 95)]
}

func percentileIndex(length int, percentile int) int {
	if length <= 0 {
		return 0
	}
	index := ((percentile * length) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > length {
		index = length
	}
	return index - 1
}
