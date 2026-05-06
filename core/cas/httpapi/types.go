package httpapi

type HasBatchRequest struct {
	CIDs []string `json:"cids"`
}

type HasBatchResult struct {
	CID     string `json:"cid"`
	Present bool   `json:"present"`
}

type HasBatchResponse struct {
	Results []HasBatchResult `json:"results"`
}

type PutBatchRequest struct {
	Blocks []PutBatchBlock `json:"blocks"`
}

type PutBatchBlock struct {
	Codec uint64 `json:"codec,omitempty"`
	Data  string `json:"data"`
}

type PutBatchResult struct {
	CID    string `json:"cid"`
	Status string `json:"status"`
}

type PutBatchResponse struct {
	Results []PutBatchResult `json:"results"`
}
