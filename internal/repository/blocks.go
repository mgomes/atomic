package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mgomes/ressik/internal/chunk"
	"github.com/mgomes/ressik/internal/object"
)

const maxBlockObjectSize = chunk.DefaultSize + 1024

// HasBlock reports whether a referenced block object is present without
// opening or authenticating its contents.
func (r *Repository) HasBlock(ctx context.Context, ref BlockRef) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Stat(r.blockPath(ref.ID))
	if err == nil {
		return info.Mode().IsRegular() && info.Size() == int64(object.SealedSize(int(ref.Length))), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect block %s: %w", ref.ID, err)
}

// PutBlock encrypts and stores one deduplicated block. Callers must hold the
// exclusive repository lock.
func (r *Repository) PutBlock(ctx context.Context, plaintext []byte) (BlockRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return BlockRef{}, false, err
	}
	if len(plaintext) > chunk.DefaultSize {
		return BlockRef{}, false, fmt.Errorf("block has %d bytes, maximum is %d", len(plaintext), chunk.DefaultSize)
	}
	id := r.codec.BlockID(plaintext)
	ref := BlockRef{ID: id, Length: uint32(len(plaintext))}
	path := r.blockPath(id)
	if _, err := os.Stat(path); err == nil {
		if _, err := r.ReadBlock(ctx, ref); err != nil {
			return BlockRef{}, false, fmt.Errorf("validate reused block %s: %w", id, err)
		}
		return ref, false, nil
	} else if !os.IsNotExist(err) {
		return BlockRef{}, false, fmt.Errorf("inspect block %s: %w", id, err)
	}
	sealed, err := r.codec.Seal(object.Block, id, plaintext)
	if err != nil {
		return BlockRef{}, false, fmt.Errorf("encrypt block %s: %w", id, err)
	}
	created, err := putFile(path, sealed)
	if err != nil {
		return BlockRef{}, false, fmt.Errorf("store block %s: %w", id, err)
	}
	return ref, created, nil
}

// ReadBlock authenticates and decrypts one block reference.
func (r *Repository) ReadBlock(ctx context.Context, ref BlockRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readFile(r.blockPath(ref.ID), maxBlockObjectSize)
	if err != nil {
		return nil, fmt.Errorf("read block %s: %w", ref.ID, err)
	}
	plaintext, err := r.codec.Open(object.Block, ref.ID, data)
	if err != nil {
		return nil, fmt.Errorf("decrypt block %s: %w", ref.ID, err)
	}
	if len(plaintext) != int(ref.Length) {
		return nil, fmt.Errorf("block %s has %d bytes, manifest requires %d", ref.ID, len(plaintext), ref.Length)
	}
	return plaintext, nil
}

func (r *Repository) blockPath(id object.ID) string {
	name := id.String()
	return filepath.Join(r.root, "blocks", name[:2], name+".block")
}
