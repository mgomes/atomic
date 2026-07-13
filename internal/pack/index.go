package pack

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/zeebo/blake3"

	"github.com/mgomes/ressik/internal/object"
)

const (
	indexHeaderSize = len(indexMagic) + 1 + 32 + 32 + 8 + 4
	memberSize      = 32 + 8 + 4
)

var indexMagic = [8]byte{'R', 'E', 'S', 'S', 'I', 'K', 'P', 'I'}

// Digest is the BLAKE3 digest of one complete immutable pack.
type Digest [32]byte

// Member locates one sealed logical block frame within a pack.
type Member struct {
	// BlockID is the repository-scoped logical block identifier.
	BlockID object.ID
	// Offset is the frame's zero-based byte offset within the pack.
	Offset uint64
	// SealedLength is the complete encrypted frame length.
	SealedLength uint32
}

// Index authenticates the contents and member locations of one pack.
type Index struct {
	// Version identifies the pack-index encoding.
	Version uint8
	// PackID names the immutable pack and its sealed index object.
	PackID object.ID
	// Digest authenticates the complete pack bytes.
	Digest Digest
	// Length is the complete pack length in bytes.
	Length uint64
	// Members is sorted strictly by BlockID for canonical lookup.
	Members []Member
}

// Validate checks the complete pack-index structure and canonical ordering.
func (i Index) Validate() error {
	if i.Version != CurrentVersion {
		return fmt.Errorf("pack index version is %d, want %d", i.Version, CurrentVersion)
	}
	if i.PackID.IsZero() {
		return errors.New("pack index has a zero pack ID")
	}
	if i.Digest == (Digest{}) {
		return errors.New("pack index has a zero digest")
	}
	if i.Length == 0 || i.Length > MaxSize {
		return fmt.Errorf("pack index length is %d, want 1 through %d", i.Length, MaxSize)
	}
	maxMembers := MaxSize / uint64(object.SealedSize(0))
	if len(i.Members) == 0 || uint64(len(i.Members)) > maxMembers {
		return fmt.Errorf("pack index has %d members, want 1 through %d", len(i.Members), maxMembers)
	}

	minimumFrameSize := uint32(object.SealedSize(0))
	for memberIndex, member := range i.Members {
		if member.BlockID.IsZero() {
			return fmt.Errorf("pack index member %d has a zero block ID", memberIndex)
		}
		if member.SealedLength < minimumFrameSize {
			return fmt.Errorf("pack index member %d sealed length is %d, minimum is %d", memberIndex, member.SealedLength, minimumFrameSize)
		}
		if member.Offset > i.Length || uint64(member.SealedLength) > i.Length-member.Offset {
			return fmt.Errorf("pack index member %d range exceeds pack length %d", memberIndex, i.Length)
		}
		if memberIndex > 0 {
			order := bytes.Compare(i.Members[memberIndex-1].BlockID[:], member.BlockID[:])
			switch {
			case order == 0:
				return fmt.Errorf("pack index contains duplicate block %s", member.BlockID)
			case order > 0:
				return errors.New("pack index members are not sorted by block ID")
			}
		}
	}

	byOffset := slices.Clone(i.Members)
	slices.SortFunc(byOffset, func(a, b Member) int {
		if a.Offset < b.Offset {
			return -1
		}
		if a.Offset > b.Offset {
			return 1
		}
		return 0
	})
	var nextOffset uint64
	for memberIndex, member := range byOffset {
		if member.Offset != nextOffset {
			return fmt.Errorf("pack index member %d starts at %d, want %d", memberIndex, member.Offset, nextOffset)
		}
		nextOffset += uint64(member.SealedLength)
	}
	if nextOffset != i.Length {
		return fmt.Errorf("pack index members end at %d, pack length is %d", nextOffset, i.Length)
	}
	return nil
}

// Find returns the member for blockID when the index contains it.
func (i Index) Find(blockID object.ID) (Member, bool) {
	memberIndex := sort.Search(len(i.Members), func(memberIndex int) bool {
		return bytes.Compare(i.Members[memberIndex].BlockID[:], blockID[:]) >= 0
	})
	if memberIndex == len(i.Members) || i.Members[memberIndex].BlockID != blockID {
		return Member{}, false
	}
	return i.Members[memberIndex], true
}

// Verify checks the complete pack length and BLAKE3 digest.
func (i Index) Verify(pack []byte) error {
	if err := i.Validate(); err != nil {
		return fmt.Errorf("validate pack index: %w", err)
	}
	if uint64(len(pack)) != i.Length {
		return fmt.Errorf("pack has %d bytes, index requires %d", len(pack), i.Length)
	}
	digest := blake3.Sum256(pack)
	if subtle.ConstantTimeCompare(digest[:], i.Digest[:]) != 1 {
		return errors.New("pack digest does not match index")
	}
	return nil
}

// MarshalBinary returns the canonical versioned binary index encoding.
func (i Index) MarshalBinary() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	encoded := make([]byte, indexHeaderSize+len(i.Members)*memberSize)
	copy(encoded, indexMagic[:])
	offset := len(indexMagic)
	encoded[offset] = i.Version
	offset++
	copy(encoded[offset:], i.PackID[:])
	offset += len(i.PackID)
	copy(encoded[offset:], i.Digest[:])
	offset += len(i.Digest)
	binary.BigEndian.PutUint64(encoded[offset:], i.Length)
	offset += 8
	binary.BigEndian.PutUint32(encoded[offset:], uint32(len(i.Members)))
	offset += 4
	for _, member := range i.Members {
		copy(encoded[offset:], member.BlockID[:])
		offset += len(member.BlockID)
		binary.BigEndian.PutUint64(encoded[offset:], member.Offset)
		offset += 8
		binary.BigEndian.PutUint32(encoded[offset:], member.SealedLength)
		offset += 4
	}
	return encoded, nil
}

// UnmarshalBinary parses and validates one canonical binary index encoding.
func (i *Index) UnmarshalBinary(encoded []byte) error {
	if len(encoded) < indexHeaderSize {
		return fmt.Errorf("pack index has %d bytes, minimum is %d", len(encoded), indexHeaderSize)
	}
	if !bytes.Equal(encoded[:len(indexMagic)], indexMagic[:]) {
		return errors.New("pack index has invalid magic")
	}
	offset := len(indexMagic)
	decoded := Index{Version: encoded[offset]}
	offset++
	copy(decoded.PackID[:], encoded[offset:offset+len(decoded.PackID)])
	offset += len(decoded.PackID)
	copy(decoded.Digest[:], encoded[offset:offset+len(decoded.Digest)])
	offset += len(decoded.Digest)
	decoded.Length = binary.BigEndian.Uint64(encoded[offset:])
	offset += 8
	memberCount := binary.BigEndian.Uint32(encoded[offset:])
	offset += 4
	maxMembers := MaxSize / uint64(object.SealedSize(0))
	if uint64(memberCount) > maxMembers {
		return fmt.Errorf("pack index declares %d members, maximum is %d", memberCount, maxMembers)
	}
	wantSize := indexHeaderSize + int(memberCount)*memberSize
	if len(encoded) != wantSize {
		return fmt.Errorf("pack index has %d bytes, declared members require %d", len(encoded), wantSize)
	}
	decoded.Members = make([]Member, memberCount)
	for memberIndex := range decoded.Members {
		member := &decoded.Members[memberIndex]
		copy(member.BlockID[:], encoded[offset:offset+len(member.BlockID)])
		offset += len(member.BlockID)
		member.Offset = binary.BigEndian.Uint64(encoded[offset:])
		offset += 8
		member.SealedLength = binary.BigEndian.Uint32(encoded[offset:])
		offset += 4
	}
	if err := decoded.Validate(); err != nil {
		return fmt.Errorf("validate decoded pack index: %w", err)
	}
	*i = decoded
	return nil
}
