// Package graph provides graph lifecycle management for MALT.
// A graph represents a scoped collection of arcs authenticated by structure commitments.
// This file contains the sentinel errors and GraphState type.
package graph

import "errors"

// GraphState represents the lifecycle state of a graph.
type GraphState string

// Graph state constants.
const (
	StateActive  GraphState = "active"
	StateFrozen  GraphState = "frozen"
	StateDeleted GraphState = "deleted"
)

// Sentinel errors.
var (
	ErrNotFound      = errors.New("graph not found")
	ErrAlreadyExists = errors.New("graph already exists")
	ErrDeleted       = errors.New("graph is deleted")
	ErrFrozen        = errors.New("graph is frozen")
	ErrInvalidState  = errors.New("invalid graph state")
)
