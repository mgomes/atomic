// Package merkle builds deterministic BLAKE3 Merkle trees.
//
// The encoding and tree shape are versioned so roots remain stable across
// implementations. Applications are responsible for defining their own
// canonical leaf payloads.
package merkle
