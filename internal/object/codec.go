package object

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zeebo/blake3"
)

const (
	headerSize          = 8 + 1 + 32 + 8
	randomNonceOverhead = 12 + 16
	keySize             = 32
)

var magic = [8]byte{'R', 'E', 'S', 'S', 'I', 'K', 0, 1}

const (
	blockIDContext   = "github.com/mgomes/ressik 2026-07-10 repository block ids v1"
	objectKeyContext = "github.com/mgomes/ressik 2026-07-10 repository object keys v1"
)

// Kind identifies a repository object type.
type Kind byte

const (
	// Block contains one deduplicated plaintext block.
	Block Kind = 1
	// Manifest contains one encrypted snapshot manifest.
	Manifest Kind = 2
	// Commit marks a manifest as a complete snapshot.
	Commit Kind = 3
	// PackIndex maps logical block IDs to sealed frames in one immutable pack.
	PackIndex Kind = 4
)

// ID is an opaque, repository-scoped object identifier.
type ID [32]byte

// SealedSize returns the encoded object size for a plaintext length.
func SealedSize(plaintextSize int) int {
	return headerSize + randomNonceOverhead + plaintextSize
}

// IsZero reports whether id is the all-zero sentinel value.
func (id ID) IsZero() bool {
	return id == ID{}
}

// String returns the lowercase hexadecimal identifier.
func (id ID) String() string {
	return hex.EncodeToString(id[:])
}

// MarshalText encodes an ID for JSON and other text-based formats.
func (id ID) MarshalText() ([]byte, error) {
	encoded := make([]byte, hex.EncodedLen(len(id)))
	hex.Encode(encoded, id[:])
	return encoded, nil
}

// UnmarshalText decodes an ID from a text-based format.
func (id *ID) UnmarshalText(text []byte) error {
	parsed, err := ParseID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// ParseID parses a lowercase or uppercase hexadecimal identifier.
func ParseID(value string) (ID, error) {
	var id ID
	data, err := hex.DecodeString(value)
	if err != nil {
		return id, fmt.Errorf("decode object ID: %w", err)
	}
	if len(data) != len(id) {
		return id, fmt.Errorf("object ID has %d bytes, want %d", len(data), len(id))
	}
	copy(id[:], data)
	return id, nil
}

// RandomID returns a cryptographically random object identifier.
func RandomID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("generate object ID: %w", err)
	}
	return id, nil
}

// Codec derives repository-scoped identifiers and encrypts repository objects.
type Codec struct {
	idHasher  *blake3.Hasher
	objectKey [keySize]byte
}

// NewCodec derives independent identifier and encryption keys from a random
// 256-bit repository key.
func NewCodec(master []byte) (*Codec, error) {
	if len(master) != keySize {
		return nil, fmt.Errorf("repository key has %d bytes, want %d", len(master), keySize)
	}

	var (
		codec Codec
		idKey [keySize]byte
	)
	blake3.DeriveKey(blockIDContext, master, idKey[:])
	blake3.DeriveKey(objectKeyContext, master, codec.objectKey[:])
	hasher, err := blake3.NewKeyed(idKey[:])
	for i := range idKey {
		idKey[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("initialize block identifier: %w", err)
	}
	codec.idHasher = hasher
	return &codec, nil
}

// BlockID returns the keyed BLAKE3 identifier for plaintext.
func (c *Codec) BlockID(plaintext []byte) ID {
	h := c.idHasher.Clone()
	_, _ = h.Write([]byte{byte(Block), 0x01})
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(plaintext)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(plaintext)

	var id ID
	copy(id[:], h.Sum(nil))
	return id
}

// Seal encrypts plaintext with AES-256-GCM and authenticates its clear object
// header. The random nonce is stored with the ciphertext.
func (c *Codec) Seal(kind Kind, id ID, plaintext []byte) ([]byte, error) {
	if !kind.valid() {
		return nil, fmt.Errorf("unknown object kind %d", kind)
	}
	header := makeHeader(kind, id, uint64(len(plaintext)))
	aead, err := c.aead(kind, id)
	if err != nil {
		return nil, err
	}
	if aead.Overhead() != randomNonceOverhead {
		return nil, fmt.Errorf("AES-GCM overhead is %d, want %d", aead.Overhead(), randomNonceOverhead)
	}
	sealed := aead.Seal(nil, nil, plaintext, header)
	return append(header, sealed...), nil
}

// Open authenticates and decrypts one object. For blocks, it also recomputes
// the keyed content identifier.
func (c *Codec) Open(wantKind Kind, wantID ID, object []byte) ([]byte, error) {
	if len(object) < headerSize {
		return nil, errors.New("repository object is truncated")
	}
	header := object[:headerSize]
	if subtle.ConstantTimeCompare(header[:len(magic)], magic[:]) != 1 {
		return nil, errors.New("repository object has invalid magic")
	}
	kind := Kind(header[len(magic)])
	if kind != wantKind || !kind.valid() {
		return nil, fmt.Errorf("repository object kind is %d, want %d", kind, wantKind)
	}
	var id ID
	copy(id[:], header[len(magic)+1:len(magic)+1+len(id)])
	if subtle.ConstantTimeCompare(id[:], wantID[:]) != 1 {
		return nil, errors.New("repository object ID does not match its name")
	}
	wantLength := binary.BigEndian.Uint64(header[headerSize-8:])
	if wantLength > uint64(maxInt()) {
		return nil, errors.New("repository object plaintext is too large")
	}

	aead, err := c.aead(kind, id)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, object[headerSize:], header)
	if err != nil {
		return nil, fmt.Errorf("authenticate repository object: %w", err)
	}
	if uint64(len(plaintext)) != wantLength {
		return nil, errors.New("repository object plaintext length does not match header")
	}
	if kind == Block {
		gotID := c.BlockID(plaintext)
		if subtle.ConstantTimeCompare(gotID[:], id[:]) != 1 {
			return nil, errors.New("repository block content does not match its ID")
		}
	}
	return plaintext, nil
}

func (c *Codec) aead(kind Kind, id ID) (cipher.AEAD, error) {
	h, err := blake3.NewKeyed(c.objectKey[:])
	if err != nil {
		return nil, fmt.Errorf("initialize object key derivation: %w", err)
	}
	_, _ = h.Write([]byte{byte(kind), 0x01})
	_, _ = h.Write(id[:])
	key := h.Sum(nil)
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-256: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-GCM: %w", err)
	}
	return aead, nil
}

func makeHeader(kind Kind, id ID, length uint64) []byte {
	header := make([]byte, headerSize)
	copy(header, magic[:])
	header[len(magic)] = byte(kind)
	copy(header[len(magic)+1:], id[:])
	binary.BigEndian.PutUint64(header[headerSize-8:], length)
	return header
}

func (k Kind) valid() bool {
	return k >= Block && k <= PackIndex
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
