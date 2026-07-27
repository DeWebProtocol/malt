package manifest

// MarshalDirectoryEntries returns the canonical V2 JSON payload for a
// directory manifest with the supplied typed entries.
func MarshalDirectoryEntries(entries []DirectoryEntry) ([]byte, error) {
	return MarshalV2DirectoryEntries(entries)
}
