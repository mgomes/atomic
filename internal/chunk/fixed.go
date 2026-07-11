package chunk

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// DefaultSize is the version-one block size.
const DefaultSize = 4 << 20

// Fixed splits a stream into fixed-size blocks. Callback data is valid only
// until the callback returns.
type Fixed struct {
	size int
}

// NewFixed returns a fixed-size splitter.
func NewFixed(size int) (*Fixed, error) {
	if size <= 0 {
		return nil, fmt.Errorf("chunk size must be positive: %d", size)
	}
	return &Fixed{size: size}, nil
}

// Split reads r until EOF and passes each non-empty block to yield.
func (f *Fixed) Split(ctx context.Context, r io.Reader, yield func([]byte) error) error {
	buf := make([]byte, f.size)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if yieldErr := yield(buf[:n]); yieldErr != nil {
				return yieldErr
			}
		}

		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return nil
		default:
			return fmt.Errorf("read chunk: %w", err)
		}
	}
}
