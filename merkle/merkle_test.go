package merkle_test

import (
	"testing"

	"github.com/mgomes/atomic/merkle"
)

func TestBuilderMatchesBatchRoot(t *testing.T) {
	t.Parallel()

	leaves := make([]merkle.Digest, 0, 17)
	var builder merkle.Builder
	for i := range 17 {
		digest := merkle.Leaf([]byte{byte(i)})
		leaves = append(leaves, digest)
		builder.Add(digest)
	}

	got := builder.Digest()
	want := merkle.Root(leaves)
	if got != want {
		t.Errorf("Builder.Digest() = %s, want Root() = %s", got, want)
	}
}

func TestRootDependsOnLeafOrder(t *testing.T) {
	t.Parallel()

	a := merkle.Leaf([]byte("a"))
	b := merkle.Leaf([]byte("b"))
	if got, other := merkle.Root([]merkle.Digest{a, b}), merkle.Root([]merkle.Digest{b, a}); got == other {
		t.Errorf("Root([a,b]) = Root([b,a]) = %s, want distinct roots", got)
	}
}

func TestRootGoldenVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		leaves []merkle.Digest
		want   string
	}{
		{name: "empty", want: "5febe233b87c1478e0acc84747e836c793789110538672c01238ec23e962e0be"},
		{
			name:   "one",
			leaves: []merkle.Digest{merkle.Leaf([]byte("one"))},
			want:   "2cb3f6c1de697137356470dcf780a5d6b54912c1938a20fc370b33b1447589ab",
		},
		{
			name: "three",
			leaves: []merkle.Digest{
				merkle.Leaf([]byte("one")),
				merkle.Leaf([]byte("two")),
				merkle.Leaf([]byte("three")),
			},
			want: "797d60af0d6a12b8bbd5459f50def668666fb284abb84622a3fb7a95426fb08b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := merkle.Root(tt.leaves).String()
			if got != tt.want {
				t.Errorf("Root(%s) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}
