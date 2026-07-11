package object_test

import (
	"bytes"
	"testing"

	"github.com/mgomes/ressik/internal/object"
)

func TestCodecRoundTrip(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, 0x42)
	plaintext := []byte("the same block can appear in many snapshots")
	id := codec.BlockID(plaintext)
	sealed, err := codec.Seal(object.Block, id, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal() returned error: %v", err)
	}
	if got, want := len(sealed), object.SealedSize(len(plaintext)); got != want {
		t.Errorf("len(Codec.Seal()) = %d, want %d", got, want)
	}
	got, err := codec.Open(object.Block, id, sealed)
	if err != nil {
		t.Fatalf("Codec.Open() returned error: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Codec.Open() = %q, want %q", got, plaintext)
	}
}

func TestCodecUsesRandomCiphertextWithStableBlockID(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, 0x42)
	plaintext := []byte("deduplicate me")
	id := codec.BlockID(plaintext)
	first, err := codec.Seal(object.Block, id, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal(first) returned error: %v", err)
	}
	second, err := codec.Seal(object.Block, id, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal(second) returned error: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("Codec.Seal() produced identical ciphertexts, want random nonces")
	}
	if got := codec.BlockID(plaintext); got != id {
		t.Errorf("Codec.BlockID() = %s, want stable ID %s", got, id)
	}
}

func TestCodecRejectsTampering(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, 0x42)
	plaintext := []byte("authenticate me")
	id := codec.BlockID(plaintext)
	sealed, err := codec.Seal(object.Block, id, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal() returned error: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := codec.Open(object.Block, id, sealed); err == nil {
		t.Error("Codec.Open(tampered object) error = nil, want authentication error")
	}
}

func TestCodecRejectsWrongRepositoryKey(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, 0x42)
	other := newCodec(t, 0x24)
	plaintext := []byte("repository scoped")
	id := codec.BlockID(plaintext)
	sealed, err := codec.Seal(object.Block, id, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal() returned error: %v", err)
	}
	if _, err := other.Open(object.Block, id, sealed); err == nil {
		t.Error("Codec.Open(wrong key) error = nil, want authentication error")
	}
}

func newCodec(t *testing.T, value byte) *object.Codec {
	t.Helper()
	key := bytes.Repeat([]byte{value}, 32)
	codec, err := object.NewCodec(key)
	if err != nil {
		t.Fatalf("NewCodec() returned error: %v", err)
	}
	return codec
}
