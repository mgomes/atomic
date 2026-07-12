package repository

import (
	"context"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"

	"github.com/zeebo/blake3"

	"github.com/mgomes/ressik/internal/object"
)

const repositoryIDContext = "github.com/mgomes/ressik 2026-07-11 repository ids v1"

// ID is a stable, opaque repository identity derived from repository.key.
// Repository copies opened with the same key have the same ID.
type ID [16]byte

// ObjectRef identifies one immutable encrypted repository object.
type ObjectRef struct {
	// Kind identifies the object's repository format kind.
	Kind object.Kind
	// ID is the repository-scoped object identifier.
	ID object.ID
}

// ID returns the stable identity shared by repository copies using the same
// repository key.
func (r *Repository) ID() ID {
	return r.id
}

// String returns the lowercase hexadecimal repository identity.
func (id ID) String() string {
	return hex.EncodeToString(id[:])
}

// ObjectKey returns the canonical provider-independent storage key for ref.
func (r *Repository) ObjectKey(ref ObjectRef) (string, error) {
	directory, suffix, _, err := transferKind(ref.Kind)
	if err != nil {
		return "", err
	}
	name := ref.ID.String()
	parts := []string{"ressik", "v1", r.id.String(), directory}
	if ref.Kind == object.Block {
		parts = append(parts, name[:2])
	}
	return path.Join(append(parts, name+suffix)...), nil
}

// ReadSealed returns the exact stored ciphertext for ref after authenticating
// its kind, ID, header, and contents. It does not determine whether the object
// belongs to a committed snapshot.
func (r *Repository) ReadSealed(ctx context.Context, ref ObjectRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	localPath, limit, err := r.sealedPath(ref)
	if err != nil {
		return nil, err
	}
	sealed, err := readFile(localPath, limit)
	if err != nil {
		return nil, fmt.Errorf("read sealed %s %s: %w", kindName(ref.Kind), ref.ID, err)
	}
	plaintext, err := r.codec.Open(ref.Kind, ref.ID, sealed)
	if err != nil {
		return nil, fmt.Errorf("authenticate sealed %s %s: %w", kindName(ref.Kind), ref.ID, err)
	}
	clear(plaintext)
	return sealed, nil
}

func deriveID(master []byte) ID {
	var id ID
	blake3.DeriveKey(repositoryIDContext, master, id[:])
	return id
}

func (r *Repository) sealedPath(ref ObjectRef) (string, int64, error) {
	directory, suffix, limit, err := transferKind(ref.Kind)
	if err != nil {
		return "", 0, err
	}
	name := ref.ID.String()
	root := filepath.Join(r.root, directory)
	if ref.Kind == object.Block {
		root = filepath.Join(root, name[:2])
	}
	return filepath.Join(root, name+suffix), limit, nil
}

func transferKind(kind object.Kind) (string, string, int64, error) {
	switch kind {
	case object.Block:
		return "blocks", ".block", maxBlockObjectSize, nil
	case object.Manifest:
		return "manifests", ".manifest", maxManifestObject, nil
	case object.Commit:
		return "commits", ".commit", maxCommitObjectSize, nil
	default:
		return "", "", 0, fmt.Errorf("unknown repository object kind %d", kind)
	}
}

func kindName(kind object.Kind) string {
	switch kind {
	case object.Block:
		return "block"
	case object.Manifest:
		return "manifest"
	case object.Commit:
		return "commit"
	default:
		return "object"
	}
}
