// Package api defines the wire protocol between the toolcop CLI and daemon.
//
// All messages are length-prefixed JSON frames on a unix socket:
//
//	[4-byte big-endian length][JSON payload]
//
// The schema lives here so any client (Go, Rust, C, etc.) has a single
// reference for the contract.
package api
