package merkle_test

import (
	"testing"

	"github.com/mgomes/ressik/merkle"
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
		{name: "empty", want: "bb378523696b698e06389e95e828f51ea456dbd299836ad7a04bddea4b04a941"},
		{
			name:   "one",
			leaves: []merkle.Digest{merkle.Leaf([]byte("one"))},
			want:   "7bcdd8367e2f95f165e701ff4f060a31fb781f407b165392ff00ec9d826baaab",
		},
		{
			name: "three",
			leaves: []merkle.Digest{
				merkle.Leaf([]byte("one")),
				merkle.Leaf([]byte("two")),
				merkle.Leaf([]byte("three")),
			},
			want: "d5348ec57bb5b0e2dca908cee197314a1ee3e8d5eb93e5de8e340b2131e1fafa",
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
