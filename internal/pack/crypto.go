package pack

import (
	"errors"
	"fmt"

	"github.com/mgomes/atomic/internal/object"
)

// SealIndex validates and encrypts an index with its pack ID.
func SealIndex(codec *object.Codec, index Index) ([]byte, error) {
	if codec == nil {
		return nil, errors.New("pack codec is nil")
	}
	encoded, err := index.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encode pack index: %w", err)
	}
	defer clear(encoded)
	sealed, err := codec.Seal(object.PackIndex, index.PackID, encoded)
	if err != nil {
		return nil, fmt.Errorf("seal pack index: %w", err)
	}
	return sealed, nil
}

// OpenIndex authenticates, decrypts, and validates an index for packID.
func OpenIndex(codec *object.Codec, packID object.ID, sealed []byte) (Index, error) {
	if codec == nil {
		return Index{}, errors.New("pack codec is nil")
	}
	if packID.IsZero() {
		return Index{}, errors.New("pack ID is zero")
	}
	encoded, err := codec.Open(object.PackIndex, packID, sealed)
	if err != nil {
		return Index{}, fmt.Errorf("open pack index: %w", err)
	}
	defer clear(encoded)
	var index Index
	if err := index.UnmarshalBinary(encoded); err != nil {
		return Index{}, fmt.Errorf("decode pack index: %w", err)
	}
	if index.PackID != packID {
		return Index{}, fmt.Errorf("pack index contains pack ID %s, envelope requires %s", index.PackID, packID)
	}
	return index, nil
}
