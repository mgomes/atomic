package s3

import (
	"bytes"
	"context"
	"testing"

	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/pack"
)

func TestClientRangeRoundTripsEncryptedPackMember(t *testing.T) {
	t.Parallel()

	codec, err := object.NewCodec(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatalf("object.NewCodec() returned error: %v", err)
	}
	plaintextBlocks := [][]byte{
		[]byte("first block"),
		[]byte("middle block selected for restore"),
		[]byte("last block"),
	}
	var packed bytes.Buffer
	writer, err := pack.NewWriter(&packed, codec)
	if err != nil {
		t.Fatalf("pack.NewWriter() returned error: %v", err)
	}
	blockIDs := make([]object.ID, 0, len(plaintextBlocks))
	for _, plaintext := range plaintextBlocks {
		blockID := codec.BlockID(plaintext)
		sealed, err := codec.Seal(object.Block, blockID, plaintext)
		if err != nil {
			t.Fatalf("Codec.Seal(Block, %s) returned error: %v", blockID, err)
		}
		if err := writer.Append(blockID, sealed); err != nil {
			t.Fatalf("pack.Writer.Append(%s) returned error: %v", blockID, err)
		}
		blockIDs = append(blockIDs, blockID)
	}
	packID := filledObjectID(0x42)
	index, err := writer.Finalize(packID)
	if err != nil {
		t.Fatalf("pack.Writer.Finalize(%s) returned error: %v", packID, err)
	}
	sealedIndex, err := pack.SealIndex(codec, index)
	if err != nil {
		t.Fatalf("pack.SealIndex() returned error: %v", err)
	}

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	packKey := "opaque/test-pack"
	indexKey := "opaque/test-index"
	if err := client.Put(context.Background(), packKey, packed.Bytes()); err != nil {
		t.Fatalf("Put(pack) returned error: %v", err)
	}
	if err := client.Put(context.Background(), indexKey, sealedIndex); err != nil {
		t.Fatalf("Put(pack index) returned error: %v", err)
	}
	downloadedIndex, _, err := client.Get(context.Background(), indexKey, int64(len(sealedIndex)))
	if err != nil {
		t.Fatalf("Get(pack index) returned error: %v", err)
	}
	openedIndex, err := pack.OpenIndex(codec, packID, downloadedIndex)
	if err != nil {
		t.Fatalf("pack.OpenIndex() returned error: %v", err)
	}
	member, found := openedIndex.Find(blockIDs[1])
	if !found {
		t.Fatalf("Index.Find(%s) found = false, want true", blockIDs[1])
	}
	frame, packObject, err := client.GetRange(
		context.Background(),
		packKey,
		int64(member.Offset),
		int64(member.SealedLength),
	)
	if err != nil {
		t.Fatalf("GetRange(packed block %s) returned error: %v", blockIDs[1], err)
	}
	if got, want := packObject.Size, int64(openedIndex.Length); got != want {
		t.Fatalf("GetRange(packed block %s).Size = %d, want indexed pack length %d", blockIDs[1], got, want)
	}
	if len(frame) >= packed.Len() {
		t.Errorf("GetRange(packed block %s) returned %d bytes, want less than complete pack size %d", blockIDs[1], len(frame), packed.Len())
	}
	restored, err := codec.Open(object.Block, member.BlockID, frame)
	if err != nil {
		t.Fatalf("Codec.Open(packed block %s) returned error: %v", member.BlockID, err)
	}
	if !bytes.Equal(restored, plaintextBlocks[1]) {
		t.Errorf("restored packed block = %q, want %q", restored, plaintextBlocks[1])
	}
}

func filledObjectID(value byte) object.ID {
	var id object.ID
	for index := range id {
		id[index] = value
	}
	return id
}
