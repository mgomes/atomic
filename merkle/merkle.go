package merkle

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/zeebo/blake3"
)

const maxLevels = 64

var domain = []byte("ressik-merkle-v1\x00")

// Digest is a 256-bit BLAKE3 Merkle digest.
type Digest [32]byte

// String returns the lowercase hexadecimal representation of the digest.
func (d Digest) String() string {
	return hex.EncodeToString(d[:])
}

// MarshalText encodes a digest for JSON and other text-based formats.
func (d Digest) MarshalText() ([]byte, error) {
	encoded := make([]byte, hex.EncodedLen(len(d)))
	hex.Encode(encoded, d[:])
	return encoded, nil
}

// UnmarshalText decodes a digest from a text-based format.
func (d *Digest) UnmarshalText(text []byte) error {
	data, err := hex.DecodeString(string(text))
	if err != nil {
		return err
	}
	if len(data) != len(d) {
		return fmt.Errorf("Merkle digest has %d bytes, want %d", len(data), len(d))
	}
	copy(d[:], data)
	return nil
}

// Leaf hashes a length-delimited application payload.
func Leaf(payload []byte) Digest {
	h := blake3.New()
	_, _ = h.Write(domain)
	_, _ = h.Write([]byte{0x01})

	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(payload)

	var digest Digest
	copy(digest[:], h.Sum(nil))
	return digest
}

// Parent hashes two ordered child digests.
func Parent(left, right Digest) Digest {
	h := blake3.New()
	_, _ = h.Write(domain)
	_, _ = h.Write([]byte{0x02})
	_, _ = h.Write(left[:])
	_, _ = h.Write(right[:])

	var digest Digest
	copy(digest[:], h.Sum(nil))
	return digest
}

// Root builds a left-balanced root from ordered leaf digests.
func Root(leaves []Digest) Digest {
	var b Builder
	for _, digest := range leaves {
		b.Add(digest)
	}
	return b.Digest()
}

// Builder incrementally builds a left-balanced Merkle tree using O(log n)
// memory. A Builder is not safe for concurrent use.
type Builder struct {
	levels [maxLevels]Digest
	used   [maxLevels]bool
	count  uint64
}

// Add appends one already-hashed leaf to the tree.
func (b *Builder) Add(digest Digest) {
	level := 0
	for b.used[level] {
		digest = Parent(b.levels[level], digest)
		b.used[level] = false
		level++
	}
	b.levels[level] = digest
	b.used[level] = true
	b.count++
}

// Digest returns the current root without changing the builder.
func (b *Builder) Digest() Digest {
	if b.count == 0 {
		return empty()
	}

	var (
		root Digest
		have bool
	)
	for level := range maxLevels {
		if !b.used[level] {
			continue
		}
		if !have {
			root = b.levels[level]
			have = true
			continue
		}
		root = Parent(b.levels[level], root)
	}
	return root
}

func empty() Digest {
	h := blake3.New()
	_, _ = h.Write(domain)
	_, _ = h.Write([]byte{0x00})

	var digest Digest
	copy(digest[:], h.Sum(nil))
	return digest
}
