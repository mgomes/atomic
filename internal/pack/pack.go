package pack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/zeebo/blake3"

	"github.com/mgomes/ressik/internal/object"
)

const (
	// TargetSize is the maximum number of sealed block bytes in one pack.
	TargetSize = 4 << 20
	// MaxSize is the largest pack accepted by this format version.
	MaxSize = 64 << 20
	// CurrentVersion is the pack-index encoding emitted by this build.
	CurrentVersion = 1
)

var (
	// ErrFull reports that a sealed frame does not fit in the current pack.
	ErrFull = errors.New("pack is full")
	// ErrClosed reports an append attempted after a pack was finalized.
	ErrClosed = errors.New("pack writer is closed")
	// ErrDuplicateBlock reports that a pack already contains a logical block.
	ErrDuplicateBlock = errors.New("pack contains duplicate block")
)

// Writer appends complete sealed block frames to one immutable pack.
type Writer struct {
	output  io.Writer
	codec   *object.Codec
	hasher  *blake3.Hasher
	members []Member
	blocks  map[object.ID]struct{}
	length  uint64
	closed  bool
	err     error
}

// NewWriter returns a pack writer that authenticates frames with codec and
// writes them directly to output.
func NewWriter(output io.Writer, codec *object.Codec) (*Writer, error) {
	if output == nil {
		return nil, errors.New("pack output is nil")
	}
	if codec == nil {
		return nil, errors.New("pack codec is nil")
	}
	hasher := blake3.New()
	return &Writer{
		output: io.MultiWriter(output, hasher),
		codec:  codec,
		hasher: hasher,
		blocks: make(map[object.ID]struct{}),
	}, nil
}

// Append authenticates and adds one complete sealed block frame. It returns
// ErrFull without modifying the pack when the frame would exceed TargetSize.
func (w *Writer) Append(blockID object.ID, sealed []byte) error {
	if w.closed {
		return ErrClosed
	}
	if w.err != nil {
		return w.err
	}
	if blockID.IsZero() {
		return errors.New("pack block ID is zero")
	}
	if len(sealed) == 0 {
		return errors.New("sealed block frame is empty")
	}
	if _, exists := w.blocks[blockID]; exists {
		return fmt.Errorf("block %s: %w", blockID, ErrDuplicateBlock)
	}
	sealedLength := uint64(len(sealed))
	if sealedLength > TargetSize || sealedLength > TargetSize-w.length {
		return fmt.Errorf("block %s with %d sealed bytes: %w", blockID, sealedLength, ErrFull)
	}
	plaintext, err := w.codec.Open(object.Block, blockID, sealed)
	if err != nil {
		return fmt.Errorf("authenticate block %s before packing: %w", blockID, err)
	}
	clear(plaintext)

	written, err := w.output.Write(sealed)
	if err != nil {
		w.err = fmt.Errorf("write block %s to pack: %w", blockID, err)
		return w.err
	}
	if written != len(sealed) {
		w.err = fmt.Errorf("write block %s to pack: %w", blockID, io.ErrShortWrite)
		return w.err
	}

	w.members = append(w.members, Member{
		BlockID:      blockID,
		Offset:       w.length,
		SealedLength: uint32(len(sealed)),
	})
	w.blocks[blockID] = struct{}{}
	w.length += sealedLength
	return nil
}

// Finalize closes the writer and returns the canonical index for packID. It
// does not flush, sync, or close the output. The caller must complete those
// operations successfully before publishing the returned index and must discard
// the index if they fail.
func (w *Writer) Finalize(packID object.ID) (Index, error) {
	if w.closed {
		return Index{}, ErrClosed
	}
	if w.err != nil {
		return Index{}, w.err
	}
	if packID.IsZero() {
		return Index{}, errors.New("pack ID is zero")
	}
	if len(w.members) == 0 {
		return Index{}, errors.New("pack contains no blocks")
	}

	members := slices.Clone(w.members)
	slices.SortFunc(members, func(a, b Member) int {
		return bytes.Compare(a.BlockID[:], b.BlockID[:])
	})
	var digest Digest
	copy(digest[:], w.hasher.Sum(nil))
	index := Index{
		Version: CurrentVersion,
		PackID:  packID,
		Digest:  digest,
		Length:  w.length,
		Members: members,
	}
	if err := index.Validate(); err != nil {
		return Index{}, fmt.Errorf("finalize pack index: %w", err)
	}
	w.closed = true
	w.output = nil
	w.codec = nil
	w.hasher = nil
	w.members = nil
	w.blocks = nil
	return index, nil
}
