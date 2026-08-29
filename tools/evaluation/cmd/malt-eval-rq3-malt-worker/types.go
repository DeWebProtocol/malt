package main

import "github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"

const (
	workerRequestSchema          = "malt-rq3-malt-worker-request/v2"
	workerResponseSchema         = "malt-rq3-malt-worker-response/v2"
	capabilitySchema             = "malt-rq3-malt-boundary-capability/v2"
	runResultSchema              = "malt-rq3-malt-run-result/v2"
	capabilityID                 = "rq3.malt-flat-kzg-fskv-arcset.v3"
	systemMALTFlat               = "malt-flat"
	operationCapabilities        = "capabilities"
	operationRun                 = "run"
	operationStreamStart         = "stream-start"
	operationStreamChunk         = "stream-chunk"
	operationStreamFinish        = "stream-finish"
	resultScopeComplete          = "complete-run"
	resultScopeStreamStart       = "stream-start"
	resultScopeStreamChunk       = "stream-chunk"
	maxWorkerLineBytes           = rq3baseline.MaximumJSONLRecordBytes
	maxWorkerRequests            = 4_096
	maximumMALTWholeFileBytes    = 64 << 20
	maximumGatewayFlatMapChanges = 65_536
	maximumMALTSnapshotFiles     = (maximumGatewayFlatMapChanges - 1) / 2
	maximumMALTChunkMutations    = maximumGatewayFlatMapChanges / 2
	maximumMALTCommitManifest    = 100_000
)

type workerRequest struct {
	SchemaVersion string   `json:"schema_version"`
	RequestID     string   `json:"request_id"`
	Operation     string   `json:"operation"`
	Run           *runSpec `json:"run,omitempty"`
}

type runSpec struct {
	PassMode    string               `json:"pass_mode"`
	RunPhase    string               `json:"run_phase"`
	ClusterID   string               `json:"cluster_id"`
	RunIndex    int                  `json:"run_index"`
	Workload    workloadIdentity     `json:"workload"`
	InitialRoot string               `json:"initial_root,omitempty"`
	Snapshot    rq3baseline.Snapshot `json:"snapshot"`
	Commits     []rq3baseline.Commit `json:"commits"`
	// CommitManifest is required only at stream-start. It binds the complete
	// ordered history before the first Gateway mutation; chunks must then match
	// it exactly and may not replace it.
	CommitManifest []string `json:"commit_manifest,omitempty"`
}

type workloadIdentity struct {
	ID                   string                `json:"id"`
	Kind                 string                `json:"kind"`
	ArtifactSHA256       string                `json:"artifact_sha256"`
	SemanticSHA256       string                `json:"semantic_sha256"`
	ChunkBytes           uint64                `json:"chunk_bytes"`
	CommitListSHA256     string                `json:"commit_list_sha256"`
	HistoryRetention     string                `json:"history_retention"`
	ControlledStructure  string                `json:"controlled_structure,omitempty"`
	ControlledCoordinate *controlledCoordinate `json:"controlled_coordinate,omitempty"`
}

type controlledCoordinate struct {
	Operation       string `json:"operation"`
	PathDepth       int    `json:"path_depth"`
	DirectoryWidth  int    `json:"directory_width"`
	FileChunks      int    `json:"file_chunks"`
	BatchSize       int    `json:"batch_size"`
	RenamedBindings int    `json:"renamed_bindings"`
}

type workerResponse struct {
	SchemaVersion string        `json:"schema_version"`
	RequestID     string        `json:"request_id"`
	OK            bool          `json:"ok"`
	Capability    *capability   `json:"capability,omitempty"`
	Result        *runResult    `json:"result,omitempty"`
	Error         *workerError  `json:"error,omitempty"`
	Stream        *streamStatus `json:"stream,omitempty"`
}

type streamStatus struct {
	Root           string `json:"root"`
	CommitsApplied uint32 `json:"commits_applied"`
	Complete       bool   `json:"complete"`
}

type workerError struct {
	Code              string   `json:"code"`
	Message           string   `json:"message"`
	MissingCategories []string `json:"missing_categories,omitempty"`
	MissingMetrics    []string `json:"missing_metrics,omitempty"`
}

type capability struct {
	SchemaVersion            string   `json:"schema_version"`
	CapabilityID             string   `json:"capability_id"`
	LayoutProfile            string   `json:"layout_profile"`
	CommitmentBackend        string   `json:"commitment_backend"`
	KVBackend                string   `json:"kv_backend"`
	ArcTablePersistence      string   `json:"arctable_persistence"`
	CheckpointEnabled        bool     `json:"checkpoint_enabled"`
	MaterializationCache     string   `json:"materialization_cache"`
	System                   string   `json:"system"`
	Boundary                 []string `json:"boundary"`
	Supported                bool     `json:"supported"`
	ExactCategories          []string `json:"exact_categories"`
	AttemptedVsPersisted     bool     `json:"attempted_vs_persisted"`
	ReplacementByteFlow      bool     `json:"replacement_byte_flow"`
	SameValueAttempts        bool     `json:"same_value_attempts"`
	OneRootPerCommit         bool     `json:"one_root_per_commit"`
	HistoryRetention         string   `json:"history_retention"`
	GatewayAccountingProfile string   `json:"gateway_accounting_profile"`
	GatewayByteMethod        string   `json:"gateway_byte_method"`
	AggregateKeyBinding      string   `json:"aggregate_key_binding"`
	DeleteLifecycle          bool     `json:"delete_lifecycle"`
	MissingCategories        []string `json:"missing_categories"`
	MissingMetrics           []string `json:"missing_metrics"`
	GapsFailClosed           bool     `json:"gaps_fail_closed"`
	MaximumWholeFileBytes    int      `json:"maximum_whole_file_bytes"`
	MaximumSnapshotFiles     int      `json:"maximum_snapshot_files"`
	MaximumMutationsPerChunk int      `json:"maximum_mutations_per_chunk"`
	MaximumFlatMapChanges    int      `json:"maximum_flat_map_changes"`
}

type runResult struct {
	SchemaVersion string           `json:"schema_version"`
	CapabilityID  string           `json:"capability_id"`
	System        string           `json:"system"`
	ResultScope   string           `json:"result_scope"`
	PassMode      string           `json:"pass_mode"`
	RunPhase      string           `json:"run_phase"`
	ClusterID     string           `json:"cluster_id"`
	RunIndex      int              `json:"run_index"`
	Workload      workloadIdentity `json:"workload"`
	Commits       []commitRecord   `json:"commits"`
	WriteEvents   []writeEvent     `json:"write_events"`
}

type commitRecord struct {
	Order      uint32 `json:"order"`
	CommitID   string `json:"commit_id"`
	ParentRoot string `json:"parent_root,omitempty"`
	Root       string `json:"root"`
	// HistoryRootsRetained counts workload roots only. The direct flat path has
	// no non-workload setup root; the compatibility field is therefore zero.
	HistoryRootsRetained          uint32 `json:"history_roots_retained"`
	NonWorkloadSetupRootsRetained uint32 `json:"non_workload_setup_roots_retained"`
	LogicalObjectsChanged         int    `json:"logical_objects_changed"`
	LogicalBindingsChanged        int    `json:"logical_bindings_changed"`
	LogicalPayloadBytes           int64  `json:"logical_payload_bytes"`
	AdapterPayloadInputBytes      int64  `json:"adapter_payload_input_bytes"`
	// ClientComputeWallNS is retained for evaluator wire compatibility. It is
	// the inclusive client-observed source-to-durable operation wall time,
	// including payload upload and submission. Gateway replay/persist are
	// nested diagnostics and must not be added to this total.
	ClientComputeWallNS  int64 `json:"client_compute_wall_ns"`
	GatewayReplayWallNS  int64 `json:"gateway_replay_wall_ns"`
	GatewayPersistWallNS int64 `json:"gateway_persist_wall_ns"`
	OracleUnmeasured     bool  `json:"oracle_unmeasured"`
}

type writeEvent struct {
	Sequence          uint64 `json:"sequence"`
	CommitID          string `json:"commit_id"`
	Stage             string `json:"stage"`
	Category          string `json:"category"`
	Cause             string `json:"cause"`
	Disposition       string `json:"disposition"`
	ObjectKey         string `json:"object_key"`
	Count             uint64 `json:"count"`
	Bytes             uint64 `json:"bytes"`
	GrossNewBytes     uint64 `json:"gross_new_bytes"`
	ReclaimedBytes    uint64 `json:"reclaimed_bytes"`
	NetBytes          int64  `json:"net_bytes"`
	CASClassification string `json:"cas_classification"`
}

func supportedCapability() capability {
	return capability{
		SchemaVersion: capabilitySchema, CapabilityID: capabilityID, System: systemMALTFlat,
		LayoutProfile: "malt-flat-canonical-path-map/v1", CommitmentBackend: "kzg", KVBackend: "fs",
		ArcTablePersistence: "fskv-versioned-delta-only-append/v1", CheckpointEnabled: false, MaterializationCache: "none",
		Boundary: []string{
			"MALT-flat one-map canonical-path to whole-file-CID layout using current Core radix Map with fixed KZG",
			"Gateway embedded CAS exact batch dispositions",
			"Gateway FSKV exact delta-only ArcTable accounting of canonical ArcSet key-plus-value bytes; checkpoints disabled; no materialization cache",
		},
		Supported: true,
		ExactCategories: []string{
			"arctable-arcset-records", "arctable-lineage-metadata", "cas-structural-metadata", "logical-changed-payload", "root-version-metadata",
		},
		AttemptedVsPersisted: true, ReplacementByteFlow: true, SameValueAttempts: true,
		OneRootPerCommit: true, HistoryRetention: "all-roots",
		GatewayAccountingProfile: "gateway.client-root-write-accounting/v2",
		GatewayByteMethod:        "durable-kv-key-plus-value-bytes/v2",
		AggregateKeyBinding:      "object-ledger-sha256/category/disposition/v1", DeleteLifecycle: true,
		MissingCategories: []string{}, MissingMetrics: []string{}, GapsFailClosed: true,
		MaximumWholeFileBytes: maximumMALTWholeFileBytes, MaximumSnapshotFiles: maximumMALTSnapshotFiles,
		MaximumMutationsPerChunk: maximumMALTChunkMutations, MaximumFlatMapChanges: maximumGatewayFlatMapChanges,
	}
}
