package pack_test

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/pack"
)

//go:embed testdata/index-v1.hex
var goldenIndexHex string

func TestWriterRoundTrip(t *testing.T) {
	t.Parallel()

	codec := newCodec(t)
	plaintextBlocks := [][]byte{
		[]byte("alpha"),
		[]byte("a somewhat longer middle block"),
		[]byte("omega"),
	}
	var packed bytes.Buffer
	writer, err := pack.NewWriter(&packed, codec)
	if err != nil {
		t.Fatalf("NewWriter() returned error: %v", err)
	}
	blockIDs := make([]object.ID, 0, len(plaintextBlocks))
	for _, plaintext := range plaintextBlocks {
		blockID := codec.BlockID(plaintext)
		sealed, err := codec.Seal(object.Block, blockID, plaintext)
		if err != nil {
			t.Fatalf("Codec.Seal(Block, %s) returned error: %v", blockID, err)
		}
		if err := writer.Append(blockID, sealed); err != nil {
			t.Fatalf("Writer.Append(%s) returned error: %v", blockID, err)
		}
		blockIDs = append(blockIDs, blockID)
	}

	packID := repeatedID(0x42)
	index, err := writer.Finalize(packID)
	if err != nil {
		t.Fatalf("Writer.Finalize(%s) returned error: %v", packID, err)
	}
	if err := index.Verify(packed.Bytes()); err != nil {
		t.Fatalf("Index.Verify() returned error: %v", err)
	}
	sealedIndex, err := pack.SealIndex(codec, index)
	if err != nil {
		t.Fatalf("SealIndex() returned error: %v", err)
	}
	tamperedIndex := bytes.Clone(sealedIndex)
	tamperedIndex[len(tamperedIndex)-1] ^= 0xff
	if _, err := pack.OpenIndex(codec, packID, tamperedIndex); err == nil {
		t.Error("OpenIndex(tampered index) error = nil, want authentication error")
	}
	if _, err := pack.OpenIndex(codec, repeatedID(0x43), sealedIndex); err == nil {
		t.Error("OpenIndex(wrong pack ID) error = nil, want identity error")
	}
	decoded, err := pack.OpenIndex(codec, packID, sealedIndex)
	if err != nil {
		t.Fatalf("OpenIndex() returned error: %v", err)
	}
	if diff := cmp.Diff(index, decoded); diff != "" {
		t.Errorf("decoded Index mismatch (-want +got):\n%s", diff)
	}

	for blockIndex, blockID := range blockIDs {
		member, found := decoded.Find(blockID)
		if !found {
			t.Errorf("Index.Find(%s) found = false, want true", blockID)
			continue
		}
		end := member.Offset + uint64(member.SealedLength)
		sealed := packed.Bytes()[member.Offset:end]
		plaintext, err := codec.Open(object.Block, blockID, sealed)
		if err != nil {
			t.Errorf("Codec.Open(Block, %s) returned error: %v", blockID, err)
			continue
		}
		if !bytes.Equal(plaintext, plaintextBlocks[blockIndex]) {
			t.Errorf("Codec.Open(Block, %s) = %q, want %q", blockID, plaintext, plaintextBlocks[blockIndex])
		}
	}

	tampered := bytes.Clone(packed.Bytes())
	tampered[len(tampered)-1] ^= 0xff
	if err := decoded.Verify(tampered); err == nil {
		t.Error("Index.Verify(tampered pack) error = nil, want digest error")
	}
	first, _ := decoded.Find(blockIDs[0])
	firstFrame := packed.Bytes()[first.Offset : first.Offset+uint64(first.SealedLength)]
	if _, err := codec.Open(object.Block, repeatedID(0x99), firstFrame); err == nil {
		t.Error("Codec.Open(wrong block ID) error = nil, want identity error")
	}
	tamperedFrame := bytes.Clone(firstFrame)
	tamperedFrame[len(tamperedFrame)-1] ^= 0xff
	if _, err := codec.Open(object.Block, blockIDs[0], tamperedFrame); err == nil {
		t.Error("Codec.Open(tampered packed frame) error = nil, want authentication error")
	}
}

func TestIndexGoldenEncoding(t *testing.T) {
	t.Parallel()

	want := pack.Index{
		Version: pack.CurrentVersion,
		PackID:  repeatedID(0x11),
		Digest:  repeatedDigest(0x22),
		Length:  170,
		Members: []pack.Member{
			{BlockID: repeatedID(0x01), Offset: 0, SealedLength: 85},
			{BlockID: repeatedID(0x02), Offset: 85, SealedLength: 85},
		},
	}
	encoded, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("Index.MarshalBinary() returned error: %v", err)
	}
	golden, err := hex.DecodeString(strings.Join(strings.Fields(goldenIndexHex), ""))
	if err != nil {
		t.Fatalf("DecodeString(golden index) returned error: %v", err)
	}
	if !bytes.Equal(encoded, golden) {
		t.Errorf("Index.MarshalBinary() = %x, want golden %x", encoded, golden)
	}

	var got pack.Index
	if err := got.UnmarshalBinary(golden); err != nil {
		t.Fatalf("Index.UnmarshalBinary(golden) returned error: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Index.UnmarshalBinary(golden) mismatch (-want +got):\n%s", diff)
	}
}

func TestIndexValidateRejectsInvalidIndexes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*pack.Index)
	}{
		{name: "version", mutate: func(index *pack.Index) { index.Version++ }},
		{name: "pack_id", mutate: func(index *pack.Index) { index.PackID = object.ID{} }},
		{name: "digest", mutate: func(index *pack.Index) { index.Digest = pack.Digest{} }},
		{name: "empty", mutate: func(index *pack.Index) { index.Members = nil }},
		{name: "duplicate", mutate: func(index *pack.Index) { index.Members[1].BlockID = index.Members[0].BlockID }},
		{name: "unsorted", mutate: func(index *pack.Index) {
			index.Members[0].BlockID, index.Members[1].BlockID = index.Members[1].BlockID, index.Members[0].BlockID
		}},
		{name: "short_frame", mutate: func(index *pack.Index) { index.Members[0].SealedLength = 1 }},
		{name: "gap", mutate: func(index *pack.Index) { index.Members[1].Offset++; index.Length++ }},
		{name: "overlap", mutate: func(index *pack.Index) { index.Members[1].Offset-- }},
		{name: "out_of_bounds", mutate: func(index *pack.Index) { index.Length-- }},
		{name: "too_large", mutate: func(index *pack.Index) { index.Length = pack.MaxSize + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := validIndex()
			tt.mutate(&index)
			if err := index.Validate(); err == nil {
				t.Errorf("Index.Validate(%s) error = nil, want validation error", tt.name)
			}
		})
	}
}

func TestIndexUnmarshalRejectsMalformedEncoding(t *testing.T) {
	t.Parallel()

	valid, err := validIndex().MarshalBinary()
	if err != nil {
		t.Fatalf("Index.MarshalBinary() returned error: %v", err)
	}
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "truncated", mutate: func(encoded []byte) []byte { return encoded[:len(encoded)-1] }},
		{name: "trailing", mutate: func(encoded []byte) []byte { return append(encoded, 0) }},
		{name: "magic", mutate: func(encoded []byte) []byte { encoded[0] ^= 0xff; return encoded }},
		{name: "member_count", mutate: func(encoded []byte) []byte { binary.BigEndian.PutUint32(encoded[81:], ^uint32(0)); return encoded }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.mutate(bytes.Clone(valid))
			var index pack.Index
			if err := index.UnmarshalBinary(encoded); err == nil {
				t.Errorf("Index.UnmarshalBinary(%s) error = nil, want decoding error", tt.name)
			}
		})
	}
}

func TestWriterErrors(t *testing.T) {
	t.Parallel()

	codec := newCodec(t)
	if _, err := pack.NewWriter(nil, codec); err == nil {
		t.Error("NewWriter(nil) error = nil, want validation error")
	}
	if _, err := pack.NewWriter(io.Discard, nil); err == nil {
		t.Error("NewWriter(nil codec) error = nil, want validation error")
	}
	empty, err := pack.NewWriter(io.Discard, codec)
	if err != nil {
		t.Fatalf("NewWriter(empty) returned error: %v", err)
	}
	if _, err := empty.Finalize(repeatedID(0x40)); err == nil {
		t.Error("Writer.Finalize(empty) error = nil, want validation error")
	}
	var output bytes.Buffer
	writer, err := pack.NewWriter(&output, codec)
	if err != nil {
		t.Fatalf("NewWriter() returned error: %v", err)
	}
	blockID, frame := sealedBlock(t, codec, []byte("frame"))
	if err := writer.Append(object.ID{}, frame); err == nil {
		t.Error("Writer.Append(zero ID) error = nil, want validation error")
	}
	if err := writer.Append(blockID, nil); err == nil {
		t.Error("Writer.Append(empty frame) error = nil, want validation error")
	}
	if err := writer.Append(blockID, frame); err != nil {
		t.Fatalf("Writer.Append(first) returned error: %v", err)
	}
	lengthBeforeDuplicate := output.Len()
	if err := writer.Append(blockID, frame); !errors.Is(err, pack.ErrDuplicateBlock) {
		t.Errorf("Writer.Append(duplicate) error = %v, want ErrDuplicateBlock", err)
	}
	if got := output.Len(); got != lengthBeforeDuplicate {
		t.Errorf("output length after duplicate = %d, want unchanged %d", got, lengthBeforeDuplicate)
	}
	if _, err := writer.Finalize(repeatedID(0x44)); err != nil {
		t.Fatalf("Writer.Finalize() returned error: %v", err)
	}
	if err := writer.Append(repeatedID(0x02), frame); !errors.Is(err, pack.ErrClosed) {
		t.Errorf("Writer.Append(finalized) error = %v, want ErrClosed", err)
	}

	var fullOutput bytes.Buffer
	fullWriter, err := pack.NewWriter(&fullOutput, codec)
	if err != nil {
		t.Fatalf("NewWriter(full output) returned error: %v", err)
	}
	fullID, fullFrame := sealedBlock(t, codec, make([]byte, pack.TargetSize-object.SealedSize(0)))
	if err := fullWriter.Append(fullID, fullFrame); err != nil {
		t.Fatalf("Writer.Append(full pack) returned error: %v", err)
	}
	fullLength := len(fullFrame)
	if err := fullWriter.Append(repeatedID(0x04), frame); !errors.Is(err, pack.ErrFull) {
		t.Errorf("Writer.Append(over capacity) error = %v, want ErrFull", err)
	}
	if got := fullOutput.Len(); got != fullLength {
		t.Errorf("output length after ErrFull = %d, want unchanged %d", got, fullLength)
	}
	fullIndex, err := fullWriter.Finalize(repeatedID(0x46))
	if err != nil {
		t.Fatalf("Writer.Finalize(full pack) returned error: %v", err)
	}
	if got := int(fullIndex.Length); got != fullLength {
		t.Errorf("Index.Length after ErrFull = %d, want %d", got, fullLength)
	}

	failed, err := pack.NewWriter(shortWriter{}, codec)
	if err != nil {
		t.Fatalf("NewWriter(shortWriter) returned error: %v", err)
	}
	if err := failed.Append(blockID, frame); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("Writer.Append(short write) error = %v, want io.ErrShortWrite", err)
	}
	if _, err := failed.Finalize(repeatedID(0x45)); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("Writer.Finalize(after short write) error = %v, want io.ErrShortWrite", err)
	}
}

func TestWriterRejectsInvalidFrames(t *testing.T) {
	t.Parallel()

	codec := newCodec(t)
	otherCodec, err := object.NewCodec(bytes.Repeat([]byte{0x33}, 32))
	if err != nil {
		t.Fatalf("NewCodec(other) returned error: %v", err)
	}
	blockID, frame := sealedBlock(t, codec, []byte("valid"))
	otherID, otherFrame := sealedBlock(t, otherCodec, []byte("other repository"))
	wrongKind, err := codec.Seal(object.PackIndex, blockID, []byte("not a block"))
	if err != nil {
		t.Fatalf("Codec.Seal(PackIndex) returned error: %v", err)
	}
	forgedLength := bytes.Clone(frame)
	forgedLength[48] ^= 0x01
	tests := []struct {
		name    string
		blockID object.ID
		frame   []byte
	}{
		{name: "wrong_id", blockID: repeatedID(0x88), frame: frame},
		{name: "wrong_kind", blockID: blockID, frame: wrongKind},
		{name: "truncated", blockID: blockID, frame: frame[:len(frame)-1]},
		{name: "forged_length", blockID: blockID, frame: forgedLength},
		{name: "wrong_repository", blockID: otherID, frame: otherFrame},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			writer, err := pack.NewWriter(&output, codec)
			if err != nil {
				t.Fatalf("NewWriter() returned error: %v", err)
			}
			if err := writer.Append(tt.blockID, tt.frame); err == nil {
				t.Errorf("Writer.Append(%s) error = nil, want authentication error", tt.name)
			}
			if got := output.Len(); got != 0 {
				t.Errorf("output length after Writer.Append(%s) = %d, want 0", tt.name, got)
			}
		})
	}
}

func TestOpenIndexRejectsInnerPackIDMismatch(t *testing.T) {
	t.Parallel()

	codec := newCodec(t)
	index := validIndex()
	encoded, err := index.MarshalBinary()
	if err != nil {
		t.Fatalf("Index.MarshalBinary() returned error: %v", err)
	}
	outerID := repeatedID(0x77)
	sealed, err := codec.Seal(object.PackIndex, outerID, encoded)
	if err != nil {
		t.Fatalf("Codec.Seal(PackIndex) returned error: %v", err)
	}
	if _, err := pack.OpenIndex(codec, outerID, sealed); err == nil {
		t.Error("OpenIndex(inner ID mismatch) error = nil, want identity error")
	}
}

func TestWriterKeepsSinkErrorsSticky(t *testing.T) {
	t.Parallel()

	codec := newCodec(t)
	blockID, frame := sealedBlock(t, codec, []byte("sink failure"))
	sinkErr := errors.New("sink failed")
	tests := []struct {
		name string
		full bool
	}{
		{name: "partial_with_error", full: false},
		{name: "full_with_error", full: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer, err := pack.NewWriter(errorWriter{err: sinkErr, full: tt.full}, codec)
			if err != nil {
				t.Fatalf("NewWriter() returned error: %v", err)
			}
			if err := writer.Append(blockID, frame); !errors.Is(err, sinkErr) {
				t.Errorf("Writer.Append(first) error = %v, want sink error", err)
			}
			if err := writer.Append(repeatedID(0x66), frame); !errors.Is(err, sinkErr) {
				t.Errorf("Writer.Append(after failure) error = %v, want sticky sink error", err)
			}
			if _, err := writer.Finalize(repeatedID(0x55)); !errors.Is(err, sinkErr) {
				t.Errorf("Writer.Finalize(after failure) error = %v, want sticky sink error", err)
			}
		})
	}
}

func FuzzIndexUnmarshal(f *testing.F) {
	seed, err := validIndex().MarshalBinary()
	if err != nil {
		f.Fatalf("Index.MarshalBinary() returned error: %v", err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, encoded []byte) {
		var index pack.Index
		if err := index.UnmarshalBinary(encoded); err != nil {
			return
		}
		canonical, err := index.MarshalBinary()
		if err != nil {
			t.Fatalf("Index.MarshalBinary(decoded) returned error: %v", err)
		}
		if !bytes.Equal(canonical, encoded) {
			t.Errorf("Index.MarshalBinary(decoded) = %x, want original %x", canonical, encoded)
		}
	})
}

func validIndex() pack.Index {
	return pack.Index{
		Version: pack.CurrentVersion,
		PackID:  repeatedID(0x11),
		Digest:  repeatedDigest(0x22),
		Length:  170,
		Members: []pack.Member{
			{BlockID: repeatedID(0x01), Offset: 0, SealedLength: 85},
			{BlockID: repeatedID(0x02), Offset: 85, SealedLength: 85},
		},
	}
}

func newCodec(t testing.TB) *object.Codec {
	t.Helper()
	codec, err := object.NewCodec(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatalf("NewCodec() returned error: %v", err)
	}
	return codec
}

func sealedBlock(t testing.TB, codec *object.Codec, plaintext []byte) (object.ID, []byte) {
	t.Helper()
	blockID := codec.BlockID(plaintext)
	sealed, err := codec.Seal(object.Block, blockID, plaintext)
	if err != nil {
		t.Fatalf("Codec.Seal(Block, %s) returned error: %v", blockID, err)
	}
	return blockID, sealed
}

func repeatedID(value byte) object.ID {
	var id object.ID
	for index := range id {
		id[index] = value
	}
	return id
}

func repeatedDigest(value byte) pack.Digest {
	var digest pack.Digest
	for index := range digest {
		digest[index] = value
	}
	return digest
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

type errorWriter struct {
	err  error
	full bool
}

func (w errorWriter) Write(data []byte) (int, error) {
	if w.full {
		return len(data), w.err
	}
	return len(data) / 2, w.err
}
