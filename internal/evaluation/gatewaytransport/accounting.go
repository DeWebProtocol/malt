package gatewaytransport

import "math"

// WriteAccounting is untrusted Gateway diagnostic accounting for
// the atomic staging boundary. It excludes payload CAS and physical backend
// bytes, which evaluation workers must account for separately.
type WriteAccounting struct {
	Profile            string                    `json:"profile"`
	Available          bool                      `json:"available"`
	UnavailableReason  string                    `json:"unavailable_reason,omitempty"`
	ByteMethod         string                    `json:"byte_method"`
	ObjectLedgerSHA256 string                    `json:"object_ledger_sha256,omitempty"`
	Categories         []WriteCategoryAccounting `json:"categories"`
}

type WriteCategoryAccounting struct {
	Category                   string `json:"category"`
	AttemptedWrites            uint64 `json:"attempted_writes"`
	AttemptedBytes             uint64 `json:"attempted_bytes"`
	AttemptedNewWrites         uint64 `json:"attempted_new_writes"`
	AttemptedNewBytes          uint64 `json:"attempted_new_bytes"`
	AttemptedReplacementWrites uint64 `json:"attempted_replacement_writes"`
	AttemptedReplacementBytes  uint64 `json:"attempted_replacement_bytes"`
	AttemptedSameValueWrites   uint64 `json:"attempted_same_value_writes"`
	AttemptedSameValueBytes    uint64 `json:"attempted_same_value_bytes"`
	AttemptedDeleteWrites      uint64 `json:"attempted_delete_writes"`
	AttemptedDeleteBytes       uint64 `json:"attempted_delete_bytes"`
	NewlyPersistedWrites       uint64 `json:"newly_persisted_writes"`
	GrossNewBytes              uint64 `json:"gross_new_bytes"`
	NewWrites                  uint64 `json:"new_writes"`
	NewBytes                   uint64 `json:"new_bytes"`
	ReplacedWrites             uint64 `json:"replaced_writes"`
	ReplacementNewBytes        uint64 `json:"replacement_new_bytes"`
	ReplacementReclaimedBytes  uint64 `json:"replacement_reclaimed_bytes"`
	SameValueWrites            uint64 `json:"same_value_writes"`
	DeletedWrites              uint64 `json:"deleted_writes"`
	DeletedReclaimedBytes      uint64 `json:"deleted_reclaimed_bytes"`
	ReclaimedBytes             uint64 `json:"reclaimed_bytes"`
	NetBytes                   int64  `json:"net_bytes"`
}

func sumAmounts(want uint64, values ...uint64) bool {
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return false
		}
		total += value
	}
	return want == total
}
