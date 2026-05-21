package arcset

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	cid "github.com/ipfs/go-cid"
)

const (
	canonicalEncodingMagic   = "MARC"
	canonicalEncodingVersion = byte(1)
)

var (
	// ErrInvalidKind is returned when a canonical arc set kind is not supported.
	ErrInvalidKind = errors.New("invalid canonical arc set kind")

	// ErrInvalidTargetKind is returned when a canonical target kind is not supported.
	ErrInvalidTargetKind = errors.New("invalid canonical target kind")

	// ErrUndefinedTarget is returned when a canonical entry does not carry a defined CID.
	ErrUndefinedTarget = errors.New("canonical target CID is undefined")

	// ErrDuplicateCoordinate is returned when one canonical coordinate has conflicting targets.
	ErrDuplicateCoordinate = errors.New("duplicate canonical coordinate")

	// ErrMissingPayloadBinding is returned when a map canonical arc set lacks its mandatory payload binding.
	ErrMissingPayloadBinding = errors.New("mandatory @payload binding is missing")

	// ErrInvalidMapCoordinate is returned when map coordinates are not canonical path/key tokens.
	ErrInvalidMapCoordinate = errors.New("invalid canonical map coordinate")

	// ErrInvalidListCoordinate is returned when list coordinates are not canonical numeric indexes.
	ErrInvalidListCoordinate = errors.New("invalid canonical list coordinate")
)

// Kind identifies the semantic shape represented by a canonical arc set.
type Kind string

const (
	KindMap  Kind = "map"
	KindList Kind = "list"
)

// TargetKind identifies the semantic type of the entry target.
type TargetKind string

const (
	TargetKindUnknown TargetKind = "unknown"
	TargetKindCAS     TargetKind = "cas"
	TargetKindMap     TargetKind = "map"
	TargetKindList    TargetKind = "list"
)

var canonicalPayloadCoordinate = []byte("@payload")

// TargetRef is a typed CID reference stored by a canonical entry.
type TargetRef struct {
	kind   TargetKind
	target cid.Cid
}

// NewTargetRef creates a typed target reference.
func NewTargetRef(kind TargetKind, target cid.Cid) TargetRef {
	return TargetRef{kind: kind, target: target}
}

// NewCIDTarget creates a target reference whose semantic type is not known.
func NewCIDTarget(target cid.Cid) TargetRef {
	return NewTargetRef(TargetKindUnknown, target)
}

// NewUnknownTarget creates a target reference whose semantic type is not known.
func NewUnknownTarget(target cid.Cid) TargetRef {
	return NewCIDTarget(target)
}

// NewCASTarget creates a target reference to immutable CAS payload.
func NewCASTarget(target cid.Cid) TargetRef {
	return NewTargetRef(TargetKindCAS, target)
}

// NewMapTarget creates a target reference to a map semantic root.
func NewMapTarget(target cid.Cid) TargetRef {
	return NewTargetRef(TargetKindMap, target)
}

// NewListTarget creates a target reference to a list semantic root.
func NewListTarget(target cid.Cid) TargetRef {
	return NewTargetRef(TargetKindList, target)
}

// Kind returns the target semantic kind.
func (r TargetRef) Kind() TargetKind {
	return r.kind
}

// CID returns the target CID.
func (r TargetRef) CID() cid.Cid {
	return r.target
}

// CanonicalCoordinate stores the canonical coordinate bytes plus a debug form.
type CanonicalCoordinate struct {
	bytes []byte
	debug string
}

// NewMapCoordinate canonicalizes a map path/key token into coordinate bytes.
func NewMapCoordinate(raw string) (CanonicalCoordinate, error) {
	path, err := NewPath(raw)
	if err != nil {
		return CanonicalCoordinate{}, err
	}
	return newCanonicalCoordinate([]byte(path.String()), path.String()), nil
}

// NewListCoordinate encodes a non-negative list index as canonical coordinate bytes.
func NewListCoordinate(index int64) (CanonicalCoordinate, error) {
	if index < 0 {
		return CanonicalCoordinate{}, ErrInvalidListCoordinate
	}
	return newListCoordinate(uint64(index)), nil
}

func newListCoordinate(index uint64) CanonicalCoordinate {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], index)
	return newCanonicalCoordinate(buf[:], strconv.FormatUint(index, 10))
}

func newCanonicalCoordinate(raw []byte, debug string) CanonicalCoordinate {
	out := make([]byte, len(raw))
	copy(out, raw)
	return CanonicalCoordinate{bytes: out, debug: debug}
}

func (c CanonicalCoordinate) clone() CanonicalCoordinate {
	return newCanonicalCoordinate(c.bytes, c.debug)
}

// Bytes returns cloned canonical coordinate bytes.
func (c CanonicalCoordinate) Bytes() []byte {
	out := make([]byte, len(c.bytes))
	copy(out, c.bytes)
	return out
}

// String returns the coordinate debug form.
func (c CanonicalCoordinate) String() string {
	return c.debug
}

// DebugString returns the coordinate debug form.
func (c CanonicalCoordinate) DebugString() string {
	return c.debug
}

// ArcEntry is one canonical coordinate-to-target binding.
type ArcEntry struct {
	Coordinate CanonicalCoordinate
	Target     TargetRef
}

// ArcChange is one canonical coordinate transition in a semantic mutation.
// A nil Before means the coordinate is expected to be absent. A nil After means
// the coordinate is deleted.
type ArcChange struct {
	Coordinate CanonicalCoordinate
	Before     *TargetRef
	After      *TargetRef
}

// CanonicalArcSet is an immutable semantic representation of map or list entries.
type CanonicalArcSet struct {
	kind    Kind
	entries []ArcEntry
}

// CanonicalArcDelta is an immutable semantic representation of coordinate
// transitions for one map or list object.
type CanonicalArcDelta struct {
	kind    Kind
	changes []ArcChange
}

// NewCanonicalArcSet creates a validated canonical arc set from entries.
func NewCanonicalArcSet(kind Kind, entries []ArcEntry) (*CanonicalArcSet, error) {
	if err := validateKind(kind); err != nil {
		return nil, err
	}

	normalized := make([]ArcEntry, len(entries))
	for i, entry := range entries {
		if err := validateTarget(entry.Target); err != nil {
			return nil, err
		}
		coord, err := validateCoordinate(kind, entry.Coordinate)
		if err != nil {
			return nil, err
		}
		normalized[i] = ArcEntry{
			Coordinate: coord,
			Target:     entry.Target,
		}
	}

	normalized = sortAndCollapseEntries(normalized)
	if err := rejectConflictingDuplicates(normalized); err != nil {
		return nil, err
	}
	normalized = collapseEquivalentDuplicates(normalized)

	if kind == KindMap && !hasPayloadBinding(normalized) {
		return nil, ErrMissingPayloadBinding
	}

	return &CanonicalArcSet{kind: kind, entries: cloneEntries(normalized)}, nil
}

// NewCanonicalMapArcSet converts a string-keyed ArcSet map into canonical map entries.
func NewCanonicalMapArcSet(arcs map[string]cid.Cid) (*CanonicalArcSet, error) {
	entries := make([]ArcEntry, 0, len(arcs))
	for raw, target := range arcs {
		coord, err := NewMapCoordinate(raw)
		if err != nil {
			return nil, err
		}
		entries = append(entries, ArcEntry{
			Coordinate: coord,
			Target:     NewCIDTarget(target),
		})
	}
	return NewCanonicalArcSet(KindMap, entries)
}

// NewCanonicalMapArcSetFromPaths converts canonical ArcSet paths into canonical map entries.
func NewCanonicalMapArcSetFromPaths(arcs map[Path]cid.Cid) (*CanonicalArcSet, error) {
	entries := make([]ArcEntry, 0, len(arcs))
	for path, target := range arcs {
		if path.IsEmpty() {
			return nil, &PathError{Err: ErrEmptyPath}
		}
		entries = append(entries, ArcEntry{
			Coordinate: newCanonicalCoordinate([]byte(path.String()), path.String()),
			Target:     NewCIDTarget(target),
		})
	}
	return NewCanonicalArcSet(KindMap, entries)
}

// CanonicalMapFromArcSet converts the current ArcSet API into a canonical map ArcSet.
func CanonicalMapFromArcSet(arcs ArcSet) (*CanonicalArcSet, error) {
	pathMap, err := ToPathMap(arcs)
	if err != nil {
		return nil, err
	}
	return NewCanonicalMapArcSetFromPaths(pathMap)
}

// NewCanonicalListArcSet builds canonical list entries from slice position.
func NewCanonicalListArcSet(targets []cid.Cid) (*CanonicalArcSet, error) {
	entries := make([]ArcEntry, 0, len(targets))
	for i, target := range targets {
		entries = append(entries, ArcEntry{
			Coordinate: newListCoordinate(uint64(i)),
			Target:     NewCIDTarget(target),
		})
	}
	return NewCanonicalArcSet(KindList, entries)
}

// NewCanonicalListArcSetFromIndexed builds canonical list entries from explicit indexes.
func NewCanonicalListArcSetFromIndexed(arcs map[uint64]cid.Cid) (*CanonicalArcSet, error) {
	entries := make([]ArcEntry, 0, len(arcs))
	for index, target := range arcs {
		entries = append(entries, ArcEntry{
			Coordinate: newListCoordinate(index),
			Target:     NewCIDTarget(target),
		})
	}
	return NewCanonicalArcSet(KindList, entries)
}

// NewCanonicalArcDelta creates a validated canonical delta from coordinate
// transitions.
func NewCanonicalArcDelta(kind Kind, changes []ArcChange) (*CanonicalArcDelta, error) {
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("canonical arc delta is empty")
	}

	normalized := make([]ArcChange, len(changes))
	for i, change := range changes {
		coord, err := validateCoordinate(kind, change.Coordinate)
		if err != nil {
			return nil, err
		}
		normalized[i] = ArcChange{Coordinate: coord}
		if change.Before != nil {
			before := *change.Before
			if err := validateTarget(before); err != nil {
				return nil, fmt.Errorf("before target: %w", err)
			}
			normalized[i].Before = &before
		}
		if change.After != nil {
			after := *change.After
			if err := validateTarget(after); err != nil {
				return nil, fmt.Errorf("after target: %w", err)
			}
			normalized[i].After = &after
		}
		if normalized[i].Before == nil && normalized[i].After == nil {
			return nil, fmt.Errorf("canonical arc delta change %s is empty", coord.String())
		}
		if normalized[i].Before != nil && normalized[i].After != nil && targetRefEqual(*normalized[i].Before, *normalized[i].After) {
			return nil, fmt.Errorf("canonical arc delta change %s is a no-op", coord.String())
		}
	}

	normalized = sortChanges(normalized)
	if err := rejectDuplicateChanges(normalized); err != nil {
		return nil, err
	}
	return &CanonicalArcDelta{kind: kind, changes: cloneChanges(normalized)}, nil
}

// Kind returns the semantic kind for this canonical arc set.
func (s *CanonicalArcSet) Kind() Kind {
	if s == nil {
		return ""
	}
	return s.kind
}

// Kind returns the semantic kind for this canonical delta.
func (d *CanonicalArcDelta) Kind() Kind {
	if d == nil {
		return ""
	}
	return d.kind
}

// Changes returns cloned canonical changes in deterministic coordinate order.
func (d *CanonicalArcDelta) Changes() []ArcChange {
	if d == nil {
		return nil
	}
	return cloneChanges(d.changes)
}

// Len returns the number of canonical changes.
func (d *CanonicalArcDelta) Len() int {
	if d == nil {
		return 0
	}
	return len(d.changes)
}

// Entries returns cloned canonical entries in deterministic coordinate order.
func (s *CanonicalArcSet) Entries() []ArcEntry {
	if s == nil {
		return nil
	}
	return cloneEntries(s.entries)
}

// Len returns the number of canonical entries.
func (s *CanonicalArcSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// MarshalBinary encodes a canonical arc set deterministically.
func (s *CanonicalArcSet) MarshalBinary() ([]byte, error) {
	if s == nil {
		return nil, errors.New("canonical arc set is nil")
	}
	if err := validateKind(s.kind); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(canonicalEncodingMagic)
	buf.WriteByte(canonicalEncodingVersion)
	writeBytes(&buf, []byte(s.kind))
	writeUint32(&buf, uint32(len(s.entries)))
	for _, entry := range s.entries {
		writeBytes(&buf, entry.Coordinate.bytes)
		writeBytes(&buf, []byte(entry.Target.kind))
		writeBytes(&buf, entry.Target.target.Bytes())
	}
	return buf.Bytes(), nil
}

// UnmarshalCanonicalArcSet decodes a deterministic canonical arc set encoding.
func UnmarshalCanonicalArcSet(data []byte) (*CanonicalArcSet, error) {
	decoder := canonicalDecoder{data: data}
	if string(decoder.readFixed(len(canonicalEncodingMagic))) != canonicalEncodingMagic {
		return nil, errors.New("invalid canonical arc set encoding")
	}
	version := decoder.readByte()
	if version != canonicalEncodingVersion {
		return nil, fmt.Errorf("unsupported canonical arc set encoding version %d", version)
	}

	kind := Kind(decoder.readBytesAsString())
	count := decoder.readUint32()
	entries := make([]ArcEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		coordBytes := decoder.readBytes()
		targetKind := TargetKind(decoder.readBytesAsString())
		cidBytes := decoder.readBytes()
		target := cid.Undef
		if len(cidBytes) > 0 {
			c, err := cid.Cast(cidBytes)
			if err != nil {
				return nil, err
			}
			target = c
		}
		entries = append(entries, ArcEntry{
			Coordinate: coordinateFromEncodedBytes(kind, coordBytes),
			Target:     NewTargetRef(targetKind, target),
		})
	}
	if decoder.err != nil {
		return nil, decoder.err
	}
	if decoder.pos != len(decoder.data) {
		return nil, errors.New("canonical arc set encoding has trailing bytes")
	}
	return NewCanonicalArcSet(kind, entries)
}

func validateKind(kind Kind) error {
	switch kind {
	case KindMap, KindList:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
}

func validateTargetKind(kind TargetKind) error {
	switch kind {
	case TargetKindUnknown, TargetKindCAS, TargetKindMap, TargetKindList:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTargetKind, kind)
	}
}

func validateTarget(target TargetRef) error {
	if err := validateTargetKind(target.kind); err != nil {
		return err
	}
	if !target.target.Defined() {
		return ErrUndefinedTarget
	}
	return nil
}

func validateCoordinate(kind Kind, coord CanonicalCoordinate) (CanonicalCoordinate, error) {
	switch kind {
	case KindMap:
		return validateMapCoordinate(coord)
	case KindList:
		return validateListCoordinate(coord)
	default:
		return CanonicalCoordinate{}, fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
}

func validateMapCoordinate(coord CanonicalCoordinate) (CanonicalCoordinate, error) {
	if len(coord.bytes) == 0 {
		return CanonicalCoordinate{}, ErrInvalidMapCoordinate
	}
	raw := string(coord.bytes)
	canonical := CanonicalizePath(raw)
	if canonical.IsEmpty() || canonical.String() != raw {
		return CanonicalCoordinate{}, fmt.Errorf("%w: %q", ErrInvalidMapCoordinate, raw)
	}
	return newCanonicalCoordinate(coord.bytes, raw), nil
}

func validateListCoordinate(coord CanonicalCoordinate) (CanonicalCoordinate, error) {
	if len(coord.bytes) != 8 {
		return CanonicalCoordinate{}, ErrInvalidListCoordinate
	}
	index := binary.BigEndian.Uint64(coord.bytes)
	return newCanonicalCoordinate(coord.bytes, strconv.FormatUint(index, 10)), nil
}

func coordinateFromEncodedBytes(kind Kind, raw []byte) CanonicalCoordinate {
	switch kind {
	case KindList:
		if len(raw) == 8 {
			return newCanonicalCoordinate(raw, strconv.FormatUint(binary.BigEndian.Uint64(raw), 10))
		}
	case KindMap:
		return newCanonicalCoordinate(raw, string(raw))
	}
	return newCanonicalCoordinate(raw, string(raw))
}

func sortAndCollapseEntries(entries []ArcEntry) []ArcEntry {
	out := cloneEntries(entries)
	less := func(i, j int) bool {
		return bytes.Compare(out[i].Coordinate.bytes, out[j].Coordinate.bytes) < 0
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortChanges(changes []ArcChange) []ArcChange {
	out := cloneChanges(changes)
	less := func(i, j int) bool {
		return bytes.Compare(out[i].Coordinate.bytes, out[j].Coordinate.bytes) < 0
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func rejectDuplicateChanges(changes []ArcChange) error {
	for i := 1; i < len(changes); i++ {
		if bytes.Equal(changes[i-1].Coordinate.bytes, changes[i].Coordinate.bytes) {
			return fmt.Errorf("%w: %s", ErrDuplicateCoordinate, changes[i].Coordinate.String())
		}
	}
	return nil
}

func rejectConflictingDuplicates(entries []ArcEntry) error {
	for i := 1; i < len(entries); i++ {
		if bytes.Equal(entries[i-1].Coordinate.bytes, entries[i].Coordinate.bytes) &&
			!targetRefEqual(entries[i-1].Target, entries[i].Target) {
			return fmt.Errorf("%w: %s", ErrDuplicateCoordinate, entries[i].Coordinate.String())
		}
	}
	return nil
}

func collapseEquivalentDuplicates(entries []ArcEntry) []ArcEntry {
	if len(entries) <= 1 {
		return cloneEntries(entries)
	}

	out := make([]ArcEntry, 0, len(entries))
	for _, entry := range entries {
		if len(out) > 0 &&
			bytes.Equal(out[len(out)-1].Coordinate.bytes, entry.Coordinate.bytes) &&
			targetRefEqual(out[len(out)-1].Target, entry.Target) {
			continue
		}
		out = append(out, cloneEntry(entry))
	}
	return out
}

func hasPayloadBinding(entries []ArcEntry) bool {
	count := 0
	for _, entry := range entries {
		if bytes.Equal(entry.Coordinate.bytes, canonicalPayloadCoordinate) && entry.Target.target.Defined() {
			count++
		}
	}
	return count == 1
}

func cloneEntries(entries []ArcEntry) []ArcEntry {
	if entries == nil {
		return nil
	}
	out := make([]ArcEntry, len(entries))
	for i, entry := range entries {
		out[i] = cloneEntry(entry)
	}
	return out
}

func cloneEntry(entry ArcEntry) ArcEntry {
	return ArcEntry{
		Coordinate: entry.Coordinate.clone(),
		Target:     entry.Target,
	}
}

func cloneChanges(changes []ArcChange) []ArcChange {
	if changes == nil {
		return nil
	}
	out := make([]ArcChange, len(changes))
	for i, change := range changes {
		out[i] = cloneChange(change)
	}
	return out
}

func cloneChange(change ArcChange) ArcChange {
	out := ArcChange{Coordinate: change.Coordinate.clone()}
	if change.Before != nil {
		before := *change.Before
		out.Before = &before
	}
	if change.After != nil {
		after := *change.After
		out.After = &after
	}
	return out
}

func targetRefEqual(a, b TargetRef) bool {
	return a.kind == b.kind && cidEqual(a.target, b.target)
}

func writeUint32(buf *bytes.Buffer, value uint32) {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], value)
	buf.Write(tmp[:])
}

func writeBytes(buf *bytes.Buffer, value []byte) {
	writeUint32(buf, uint32(len(value)))
	buf.Write(value)
}

type canonicalDecoder struct {
	data []byte
	pos  int
	err  error
}

func (d *canonicalDecoder) readFixed(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || len(d.data)-d.pos < n {
		d.err = errors.New("truncated canonical arc set encoding")
		return nil
	}
	out := d.data[d.pos : d.pos+n]
	d.pos += n
	return out
}

func (d *canonicalDecoder) readByte() byte {
	raw := d.readFixed(1)
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}

func (d *canonicalDecoder) readUint32() uint32 {
	raw := d.readFixed(4)
	if len(raw) == 0 {
		return 0
	}
	return binary.BigEndian.Uint32(raw)
}

func (d *canonicalDecoder) readBytes() []byte {
	length := d.readUint32()
	raw := d.readFixed(int(length))
	if raw == nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

func (d *canonicalDecoder) readBytesAsString() string {
	return string(d.readBytes())
}
