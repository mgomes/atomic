package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientGetsExactRanges(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	key := "packs/value.pack"
	payload := []byte("0123456789abcdef")
	store.objects[key] = bytes.Clone(payload)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	tests := []struct {
		name   string
		offset int64
		length int64
		want   []byte
	}{
		{name: "first", offset: 0, length: 1, want: []byte("0")},
		{name: "middle", offset: 5, length: 4, want: []byte("5678")},
		{name: "last", offset: int64(len(payload) - 1), length: 1, want: []byte("f")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, metadata, err := client.GetRange(context.Background(), key, test.offset, test.length)
			if err != nil {
				t.Fatalf("GetRange(%q, %d, %d) returned error: %v", key, test.offset, test.length, err)
			}
			if !bytes.Equal(got, test.want) {
				t.Errorf("GetRange(%q, %d, %d) = %q, want %q", key, test.offset, test.length, got, test.want)
			}
			if got, want := metadata.Size, int64(len(payload)); got != want {
				t.Errorf("GetRange(%q, %d, %d).Size = %d, want complete size %d", key, test.offset, test.length, got, want)
			}
			if got, want := metadata.ETag, `"fake-etag"`; got != want {
				t.Errorf("GetRange(%q, %d, %d).ETag = %q, want %q", key, test.offset, test.length, got, want)
			}

			calls := store.Calls()
			call := calls[len(calls)-1]
			wantRange := fmt.Sprintf("bytes=%d-%d", test.offset, test.offset+test.length-1)
			if call.Range != wantRange {
				t.Errorf("GetRange(%q, %d, %d) Range = %q, want %q", key, test.offset, test.length, call.Range, wantRange)
			}
			if call.AcceptEncoding != "identity" {
				t.Errorf("GetRange(%q, %d, %d) Accept-Encoding = %q, want identity", key, test.offset, test.length, call.AcceptEncoding)
			}
			if !strings.Contains(call.Authorization, "SignedHeaders=host;range;") {
				t.Errorf("GetRange(%q, %d, %d) Authorization = %q, want signed Range header", key, test.offset, test.length, call.Authorization)
			}
		})
	}
}

func TestClientRejectsInvalidRangesBeforeRequest(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	tests := []struct {
		name   string
		offset int64
		length int64
	}{
		{name: "negative_offset", offset: -1, length: 1},
		{name: "zero_length", offset: 0, length: 0},
		{name: "negative_length", offset: 0, length: -1},
		{name: "oversized_length", offset: 0, length: maxRangeResponse + 1},
		{name: "end_overflow", offset: math.MaxInt64, length: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := client.GetRange(context.Background(), "packs/value.pack", test.offset, test.length); err == nil {
				t.Errorf("GetRange(offset %d, length %d) error = nil, want validation error", test.offset, test.length)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client.GetRange(ctx, "packs/value.pack", 0, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("GetRange(canceled context) error = %v, want context.Canceled", err)
	}
	if got := len(store.Calls()); got != 0 {
		t.Errorf("GetRange(invalid or canceled inputs) made %d requests, want 0", got)
	}
}

func TestClientRejectsMalformedPartialResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		contentRanges []string
		contentLength string
		body          string
		flushHeaders  bool
	}{
		{name: "missing_content_range", body: "3456"},
		{name: "malformed_content_range", contentRanges: []string{"items 3-6/10"}, body: "3456"},
		{name: "duplicate_content_range", contentRanges: []string{"bytes 3-6/10", "bytes 3-6/10"}, body: "3456"},
		{name: "wrong_start", contentRanges: []string{"bytes 2-6/10"}, body: "23456"},
		{name: "wrong_end", contentRanges: []string{"bytes 3-7/10"}, body: "34567"},
		{name: "inconsistent_total", contentRanges: []string{"bytes 3-6/6"}, body: "3456"},
		{name: "declared_length", contentRanges: []string{"bytes 3-6/10"}, contentLength: "3", body: "345"},
		{name: "short_body", contentRanges: []string{"bytes 3-6/10"}, contentLength: "4", body: "345"},
		{name: "oversized_body", contentRanges: []string{"bytes 3-6/10"}, body: "34567", flushHeaders: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				attempts.Add(1)
				for _, value := range test.contentRanges {
					writer.Header().Add("Content-Range", value)
				}
				if test.contentLength != "" {
					writer.Header().Set("Content-Length", test.contentLength)
				}
				writer.WriteHeader(http.StatusPartialContent)
				if test.flushHeaders {
					writer.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			t.Cleanup(server.Close)
			client := mustClient(t, Options{
				Endpoint:     server.URL,
				Region:       "us-east-1",
				Bucket:       testBucket,
				AddressStyle: PathStyle,
				Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
				HTTPClient:   server.Client(),
				MaxAttempts:  1,
			})
			if _, _, err := client.GetRange(context.Background(), "packs/value.pack", 3, 4); err == nil {
				t.Errorf("GetRange(%s response) error = nil, want protocol error", test.name)
			}
			if got := attempts.Load(); got != 1 {
				t.Errorf("GetRange(%s response) attempts = %d, want 1", test.name, got)
			}
		})
	}
}

func TestClientDoesNotFallBackWhenRangeIsIgnored(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	var reads atomic.Int32
	var closes atomic.Int32
	var requestedRange string
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		requestedRange = request.Header.Get("Range")
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          &countingReadCloser{reads: &reads, closes: &closes},
			ContentLength: maxSuccessResponse + 1,
			Request:       request,
		}, nil
	})}
	client := mustClient(t, Options{
		Endpoint:     "https://s3.example.com",
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   httpClient,
		MaxAttempts:  3,
	})
	client.wait = noWait
	if _, _, err := client.GetRange(context.Background(), "packs/value.pack", 3, 4); err == nil {
		t.Error("GetRange(ignored range) error = nil, want protocol error")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("GetRange(ignored range) attempts = %d, want no retry", got)
	}
	if requestedRange == "" {
		t.Error("GetRange(ignored range) request omitted Range header")
	}
	if got := reads.Load(); got != 0 {
		t.Errorf("GetRange(ignored range) body reads = %d, want 0", got)
	}
	if got := closes.Load(); got != 1 {
		t.Errorf("GetRange(ignored range) body closes = %d, want 1", got)
	}
}

func TestClientRetriesTruncatedPartialResponse(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	var invalidRanges atomic.Int32
	var unsignedRanges atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=3-6" {
			invalidRanges.Add(1)
		}
		if !strings.Contains(request.Header.Get("Authorization"), "SignedHeaders=host;range;") {
			unsignedRanges.Add(1)
		}
		writer.Header().Set("Content-Range", "bytes 3-6/10")
		writer.Header().Set("Content-Length", "4")
		writer.WriteHeader(http.StatusPartialContent)
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(writer, "345")
			return
		}
		_, _ = io.WriteString(writer, "3456")
	}))
	t.Cleanup(server.Close)
	client := mustClient(t, Options{
		Endpoint:     server.URL,
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   server.Client(),
		MaxAttempts:  2,
	})
	client.wait = noWait
	got, _, err := client.GetRange(context.Background(), "packs/value.pack", 3, 4)
	if err != nil {
		t.Fatalf("GetRange(after truncated response) returned error: %v", err)
	}
	if !bytes.Equal(got, []byte("3456")) {
		t.Errorf("GetRange(after truncated response) = %q, want %q", got, "3456")
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("GetRange(after truncated response) attempts = %d, want 2", got)
	}
	if got := invalidRanges.Load(); got != 0 {
		t.Errorf("GetRange(after truncated response) invalid Range headers = %d, want 0", got)
	}
	if got := unsignedRanges.Load(); got != 0 {
		t.Errorf("GetRange(after truncated response) unsigned Range headers = %d, want 0", got)
	}
}

func TestClientRangeErrorsPreserveS3Semantics(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	if _, _, err := client.GetRange(context.Background(), "packs/missing.pack", 0, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRange(missing object) error = %v, want ErrNotFound", err)
	}
	store.objects["packs/short.pack"] = []byte("short")
	_, _, err := client.GetRange(context.Background(), "packs/short.pack", 10, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("GetRange(unsatisfiable) error = %v, want HTTP 416 *APIError", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("GetRange(unsatisfiable) error = %v, must not match ErrNotFound", err)
	}
}

func TestClientGetsOpaqueGzipRangeWithoutContentLength(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write([]byte("encrypted bytes that happen to be gzip encoded")); err != nil {
		t.Fatalf("gzip.Write() returned error: %v", err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatalf("gzip.Close() returned error: %v", err)
	}
	payload := bytes.Clone(compressed.Bytes())
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Header.Get("Accept-Encoding"), "identity"; got != want {
			writeFakeError(writer, http.StatusBadRequest, "InvalidRequest", "unexpected Accept-Encoding")
			return
		}
		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
		writer.WriteHeader(http.StatusPartialContent)
		writer.(http.Flusher).Flush()
		_, _ = writer.Write(payload)
	}))
	t.Cleanup(server.Close)
	client := mustClient(t, Options{
		Endpoint:     server.URL,
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   server.Client(),
		MaxAttempts:  1,
	})
	got, _, err := client.GetRange(context.Background(), "packs/value.pack", 0, int64(len(payload)))
	if err != nil {
		t.Fatalf("GetRange(gzip-encoded object) returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("GetRange(gzip-encoded object) returned transformed bytes")
	}
}

func TestClientRangeHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var attempts atomic.Int32
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode:    http.StatusPartialContent,
			Header:        http.Header{"Content-Range": []string{"bytes 0-3/10"}, "Content-Length": []string{"4"}},
			Body:          &cancelReadCloser{ctx: request.Context(), cancel: cancel},
			ContentLength: 4,
			Request:       request,
		}, nil
	})}
	client := mustClient(t, Options{
		Endpoint:     "https://s3.example.com",
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   httpClient,
		MaxAttempts:  3,
	})
	if _, _, err := client.GetRange(ctx, "packs/value.pack", 0, 4); !errors.Is(err, context.Canceled) {
		t.Errorf("GetRange(canceled during body) error = %v, want context.Canceled", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("GetRange(canceled during body) attempts = %d, want 1", got)
	}
}

func FuzzParseContentRange(f *testing.F) {
	f.Add("bytes 0-0/1")
	f.Add("bytes 3-6/10")
	f.Add("bytes */10")
	f.Fuzz(func(t *testing.T, value string) {
		start, end, total, err := parseContentRange(value)
		if err != nil {
			return
		}
		if start < 0 || start > end || end >= total {
			t.Errorf("parseContentRange(%q) = %d, %d, %d, want 0 <= start <= end < total", value, start, end, total)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type cancelReadCloser struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Read(_ []byte) (int, error) {
	r.cancel()
	return 0, r.ctx.Err()
}

func (*cancelReadCloser) Close() error {
	return nil
}

type countingReadCloser struct {
	reads  *atomic.Int32
	closes *atomic.Int32
}

func (r *countingReadCloser) Read(data []byte) (int, error) {
	r.reads.Add(1)
	for index := range data {
		data[index] = 0
	}
	return len(data), nil
}

func (r *countingReadCloser) Close() error {
	r.closes.Add(1)
	return nil
}
