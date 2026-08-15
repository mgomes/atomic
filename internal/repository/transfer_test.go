package repository_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
)

func TestRepositoryIDIsStableAndKeyScoped(t *testing.T) {
	t.Parallel()

	first := openRepositoryWithKey(t, 0x42)
	if got, want := first.ID().String(), "7711c4c018c81512957511a88853cd90"; got != want {
		t.Errorf("Repository.ID() = %q, want %q", got, want)
	}
	if got, want := len(first.ID().String()), 32; got != want {
		t.Errorf("len(Repository.ID().String()) = %d, want %d", got, want)
	}
	if _, err := hex.DecodeString(first.ID().String()); err != nil {
		t.Errorf("DecodeString(Repository.ID()) returned error: %v", err)
	}

	reopened, err := repository.Open(first.Root())
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	if got, want := reopened.ID(), first.ID(); got != want {
		t.Errorf("Open().ID() = %s, want %s", got, want)
	}

	second := openRepositoryWithKey(t, 0x24)
	if second.ID() == first.ID() {
		t.Errorf("repositories with different keys have the same ID %s", first.ID())
	}
}

func TestRepositoryObjectKeyUsesCanonicalLayout(t *testing.T) {
	t.Parallel()

	repo := openRepositoryWithKey(t, 0x42)
	var id object.ID
	for i := range id {
		id[i] = 0xab
	}
	base := "atomic/v1/" + repo.ID().String()
	tests := []struct {
		name    string
		kind    object.Kind
		want    string
		wantErr bool
	}{
		{name: "block", kind: object.Block, want: base + "/blocks/ab/" + id.String() + ".block"},
		{name: "manifest", kind: object.Manifest, want: base + "/manifests/" + id.String() + ".manifest"},
		{name: "commit", kind: object.Commit, want: base + "/commits/" + id.String() + ".commit"},
		{name: "unknown", kind: object.Kind(255), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := repo.ObjectKey(repository.ObjectRef{Kind: test.kind, ID: id})
			if gotErr := err != nil; gotErr != test.wantErr {
				t.Fatalf("ObjectKey(kind %d) error = %v, want error presence = %t", test.kind, err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("ObjectKey(kind %d) = %q, want %q", test.kind, got, test.want)
			}
			if strings.ContainsRune(got, '\\') {
				t.Errorf("ObjectKey(kind %d) = %q, want slash separators", test.kind, got)
			}
		})
	}
}

func TestRepositoryReadSealedReturnsStoredCiphertext(t *testing.T) {
	t.Parallel()

	repo, block, snapshotID := committedRepository(t)
	refs := []repository.ObjectRef{
		{Kind: object.Block, ID: block.ID},
		{Kind: object.Manifest, ID: snapshotID},
		{Kind: object.Commit, ID: snapshotID},
	}
	for _, ref := range refs {
		want, err := os.ReadFile(localObjectPath(repo.Root(), ref))
		if err != nil {
			t.Fatalf("ReadFile(kind %d) returned error: %v", ref.Kind, err)
		}
		got, err := repo.ReadSealed(context.Background(), ref)
		if err != nil {
			t.Fatalf("ReadSealed(kind %d) returned error: %v", ref.Kind, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadSealed(kind %d) did not return the stored ciphertext", ref.Kind)
		}
	}
}

func TestRepositoryReadSealedRejectsTampering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  func(repository.BlockRef, object.ID) repository.ObjectRef
	}{
		{
			name: "block",
			ref: func(block repository.BlockRef, _ object.ID) repository.ObjectRef {
				return repository.ObjectRef{Kind: object.Block, ID: block.ID}
			},
		},
		{
			name: "manifest",
			ref: func(_ repository.BlockRef, id object.ID) repository.ObjectRef {
				return repository.ObjectRef{Kind: object.Manifest, ID: id}
			},
		},
		{
			name: "commit",
			ref: func(_ repository.BlockRef, id object.ID) repository.ObjectRef {
				return repository.ObjectRef{Kind: object.Commit, ID: id}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repo, block, snapshotID := committedRepository(t)
			ref := test.ref(block, snapshotID)
			path := localObjectPath(repo.Root(), ref)
			sealed, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s) returned error: %v", test.name, err)
			}
			sealed[len(sealed)-1] ^= 0xff
			if err := os.WriteFile(path, sealed, 0o600); err != nil {
				t.Fatalf("WriteFile(%s) returned error: %v", test.name, err)
			}
			if _, err := repo.ReadSealed(context.Background(), ref); err == nil {
				t.Errorf("ReadSealed(tampered %s) error = nil, want authentication error", test.name)
			}
		})
	}
}

func TestRepositoryReadSealedRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	repo, block, _ := committedRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.ReadSealed(ctx, repository.ObjectRef{Kind: object.Block, ID: block.ID}); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadSealed(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := repo.ReadSealed(context.Background(), repository.ObjectRef{Kind: object.Kind(255), ID: block.ID}); err == nil {
		t.Error("ReadSealed(unknown kind) error = nil, want validation error")
	}
}

func TestRepositoryReadSealedRejectsMismatchedAndOversizedObjects(t *testing.T) {
	t.Parallel()

	t.Run("mismatched ID", func(t *testing.T) {
		t.Parallel()

		repo, block, _ := committedRepository(t)
		ref := repository.ObjectRef{Kind: object.Block, ID: block.ID}
		sealed, err := os.ReadFile(localObjectPath(repo.Root(), ref))
		if err != nil {
			t.Fatalf("ReadFile(block) returned error: %v", err)
		}
		ref.ID[0] ^= 0xff
		if err := os.WriteFile(localObjectPath(repo.Root(), ref), sealed, 0o600); err != nil {
			t.Fatalf("WriteFile(mismatched block) returned error: %v", err)
		}
		if _, err := repo.ReadSealed(context.Background(), ref); err == nil {
			t.Error("ReadSealed(mismatched ID) error = nil, want authentication error")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		t.Parallel()

		repo, block, _ := committedRepository(t)
		ref := repository.ObjectRef{Kind: object.Block, ID: block.ID}
		if err := os.WriteFile(localObjectPath(repo.Root(), ref), make([]byte, (4<<20)+1025), 0o600); err != nil {
			t.Fatalf("WriteFile(oversized block) returned error: %v", err)
		}
		if _, err := repo.ReadSealed(context.Background(), ref); err == nil {
			t.Error("ReadSealed(oversized block) error = nil, want size error")
		}
	})
}

func openRepositoryWithKey(t *testing.T, value byte) *repository.Repository {
	t.Helper()
	root := t.TempDir()
	if _, err := repository.Initialize(root); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "repository.key"), bytes.Repeat([]byte{value}, 32), 0o600); err != nil {
		t.Fatalf("WriteFile(repository.key) returned error: %v", err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	return repo
}

func committedRepository(t *testing.T) (*repository.Repository, repository.BlockRef, object.ID) {
	t.Helper()
	repo, err := repository.Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	id, err := object.RandomID()
	if err != nil {
		t.Fatalf("RandomID() returned error: %v", err)
	}
	var ref repository.BlockRef
	err = repo.Exclusive(context.Background(), func() error {
		var err error
		ref, _, err = repo.PutBlock(context.Background(), []byte("sealed transfer block"))
		if err != nil {
			return err
		}
		return repo.Commit(
			context.Background(),
			manifestWithBlock(id, "files", ref, time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)),
		)
	})
	if err != nil {
		t.Fatalf("Repository.Exclusive() returned error: %v", err)
	}
	return repo, ref, id
}

func localObjectPath(root string, ref repository.ObjectRef) string {
	name := ref.ID.String()
	switch ref.Kind {
	case object.Block:
		return filepath.Join(root, "blocks", name[:2], name+".block")
	case object.Manifest:
		return filepath.Join(root, "manifests", name+".manifest")
	case object.Commit:
		return filepath.Join(root, "commits", name+".commit")
	default:
		return ""
	}
}
