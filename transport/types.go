package transport

// HealthResponse is returned by the managed gateway health endpoint.
type HealthResponse struct {
	Status             string `json:"status"`
	KVBackend          string `json:"kv_backend,omitempty"`
	BlobBackend        string `json:"blob_backend,omitempty"`
	ArcTableMode       string `json:"arctable_mode,omitempty"`
	CommitmentProfile  string `json:"default_commitment_backend,omitempty"`
	CommitmentBackends string `json:"commitment_backends,omitempty"`
}

type errorResponse struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e errorResponse) messageText() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Error
}
