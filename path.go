package malt

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PathSeparator is the canonical textual separator for MALT segment paths.
// Transports may carry segments without using this textual projection.
const PathSeparator = "/"

// SegmentPath is an immutable application-neutral sequence of MALT path
// segments. Applications and transports decide how their own path syntax maps
// to segments; MALT owns the canonical segment-to-arc projection.
type SegmentPath struct {
	segments []string
}

// NewSegmentPath validates and clones a segment sequence. Segments are
// non-empty UTF-8 strings and must not contain the canonical separator.
func NewSegmentPath(segments []string) (SegmentPath, error) {
	cloned := make([]string, len(segments))
	for i, segment := range segments {
		if segment == "" {
			return SegmentPath{}, fmt.Errorf("MALT path segment %d is empty", i)
		}
		if !utf8.ValidString(segment) {
			return SegmentPath{}, fmt.Errorf("MALT path segment %d is not valid UTF-8", i)
		}
		if strings.Contains(segment, PathSeparator) {
			return SegmentPath{}, fmt.Errorf("MALT path segment %d contains separator %q", i, PathSeparator)
		}
		cloned[i] = segment
	}
	return SegmentPath{segments: cloned}, nil
}

// ParseSegmentPath parses the canonical textual projection used by the
// reference slash-path adapters. The empty string denotes the root path.
func ParseSegmentPath(raw string) (SegmentPath, error) {
	if raw == "" {
		return SegmentPath{}, nil
	}
	if strings.HasPrefix(raw, PathSeparator) || strings.HasSuffix(raw, PathSeparator) {
		return SegmentPath{}, fmt.Errorf("MALT path must not start or end with %q", PathSeparator)
	}
	return NewSegmentPath(strings.Split(raw, PathSeparator))
}

// Segments returns a cloned segment slice.
func (p SegmentPath) Segments() []string {
	return append([]string(nil), p.segments...)
}

// String returns the canonical textual projection.
func (p SegmentPath) String() string {
	return strings.Join(p.segments, PathSeparator)
}

// Empty reports whether the path denotes the caller-supplied root.
func (p SegmentPath) Empty() bool {
	return len(p.segments) == 0
}

// HasPrefix reports whether prefix is a segment-aligned prefix of p.
func (p SegmentPath) HasPrefix(prefix SegmentPath) bool {
	if len(prefix.segments) > len(p.segments) {
		return false
	}
	for i := range prefix.segments {
		if p.segments[i] != prefix.segments[i] {
			return false
		}
	}
	return true
}

// Consume removes a segment-aligned prefix.
func (p SegmentPath) Consume(prefix SegmentPath) (SegmentPath, bool) {
	if !p.HasPrefix(prefix) {
		return SegmentPath{}, false
	}
	return SegmentPath{segments: append([]string(nil), p.segments[len(prefix.segments):]...)}, true
}
