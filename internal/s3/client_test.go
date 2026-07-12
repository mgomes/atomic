package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

const (
	testAccessKey = "TESTACCESSKEY123"
	testSecretKey = "test-secret-key"
	testBucket    = "backup-bucket"
)

func TestClientObjectLifecycle(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
	})
	ctx := context.Background()
	key := "blocks/ab/object +.block"
	payload := []byte("encrypted block")

	if err := client.Put(ctx, key, payload); err != nil {
		t.Fatalf("Put(%q) returned error: %v", key, err)
	}
	metadata, err := client.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat(%q) returned error: %v", key, err)
	}
	wantMetadata := Object{Key: key, Size: int64(len(payload)), ETag: `"fake-etag"`}
	if diff := cmp.Diff(wantMetadata, metadata); diff != "" {
		t.Errorf("Stat(%q) mismatch (-want +got):\n%s", key, diff)
	}

	got, downloaded, err := client.Get(ctx, key, int64(len(payload)))
	if err != nil {
		t.Fatalf("Get(%q) returned error: %v", key, err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Get(%q) data = %q, want %q", key, got, payload)
	}
	if diff := cmp.Diff(wantMetadata, downloaded); diff != "" {
		t.Errorf("Get(%q) metadata mismatch (-want +got):\n%s", key, diff)
	}

	page, err := client.List(ctx, ListOptions{Prefix: "blocks/"})
	if err != nil {
		t.Fatalf("List(%q) returned error: %v", "blocks/", err)
	}
	if diff := cmp.Diff([]Object{wantMetadata}, page.Objects); diff != "" {
		t.Errorf("List(%q).Objects mismatch (-want +got):\n%s", "blocks/", diff)
	}
	if page.NextContinuationToken != "" {
		t.Errorf("List(%q).NextContinuationToken = %q, want empty", "blocks/", page.NextContinuationToken)
	}

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("Delete(%q) returned error: %v", key, err)
	}
	if _, err := client.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(%q after delete) error = %v, want ErrNotFound", key, err)
	}

	calls := store.Calls()
	if gotURI, wantURI := calls[0].RequestURI, "/backup-bucket/blocks/ab/object%20%2B.block"; gotURI != wantURI {
		t.Errorf("Put(%q) RequestURI = %q, want %q", key, gotURI, wantURI)
	}
	for _, call := range calls {
		if !strings.Contains(call.Authorization, "/auto/s3/aws4_request") {
			t.Errorf("%s %s Authorization = %q, want R2-compatible auto region scope", call.Method, call.RequestURI, call.Authorization)
		}
	}
}

func TestClientPaginatesAndEncodesContinuationTokens(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	store.objects["commits/first.commit"] = []byte("first")
	store.objects["commits/second.commit"] = []byte("second")
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})

	first, err := client.List(context.Background(), ListOptions{Prefix: "commits/", MaxKeys: 1})
	if err != nil {
		t.Fatalf("List(first page) returned error: %v", err)
	}
	if got, want := len(first.Objects), 1; got != want {
		t.Fatalf("List(first page) returned %d objects, want %d", got, want)
	}
	if first.NextContinuationToken == "" {
		t.Fatal("List(first page) continuation token is empty, want token")
	}
	second, err := client.List(context.Background(), ListOptions{
		Prefix:            "commits/",
		ContinuationToken: first.NextContinuationToken,
		MaxKeys:           1,
	})
	if err != nil {
		t.Fatalf("List(second page) returned error: %v", err)
	}
	if got, want := len(second.Objects), 1; got != want {
		t.Fatalf("List(second page) returned %d objects, want %d", got, want)
	}

	if _, err := client.List(context.Background(), ListOptions{
		ContinuationToken: "next+/=",
		MaxKeys:           1,
	}); err != nil {
		t.Fatalf("List(opaque continuation token) returned error: %v", err)
	}
	calls := store.Calls()
	last := calls[len(calls)-1]
	if !strings.Contains(last.RequestURI, "continuation-token=next%2B%2F%3D") {
		t.Errorf("List(opaque continuation token) RequestURI = %q, want AWS-encoded token", last.RequestURI)
	}
}

func TestClientSignsSessionToken(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
		SessionToken:    "temporary+/=token",
	})
	if err := client.Put(context.Background(), "blocks/aa/value.block", []byte("sealed")); err != nil {
		t.Fatalf("Put(with session token) returned error: %v", err)
	}
	call := store.Calls()[0]
	if got, want := call.SessionToken, "temporary+/=token"; got != want {
		t.Errorf("Put() X-Amz-Security-Token = %q, want %q", got, want)
	}
	if !strings.Contains(call.Authorization, "x-amz-security-token") {
		t.Errorf("Put() Authorization = %q, want signed session-token header", call.Authorization)
	}
}

func TestClientRetriesReplayablePut(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	store.failPuts = 1
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	payload := []byte("same encrypted bytes")
	if err := client.Put(context.Background(), "blocks/aa/retry.block", payload); err != nil {
		t.Fatalf("Put(retry) returned error: %v", err)
	}
	calls := store.Calls()
	if got, want := len(calls), 2; got != want {
		t.Fatalf("Put(retry) made %d requests, want %d", got, want)
	}
	for attempt, call := range calls {
		if !bytes.Equal(call.Body, payload) {
			t.Errorf("Put(retry) attempt %d body = %q, want %q", attempt+1, call.Body, payload)
		}
	}
}

func TestClientBoundsDownloadedObjects(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	key := "manifests/value.manifest"
	if err := client.Put(context.Background(), key, []byte("12345")); err != nil {
		t.Fatalf("Put(%q) returned error: %v", key, err)
	}
	if _, _, err := client.Get(context.Background(), key, 4); !errors.Is(err, ErrObjectTooLarge) {
		t.Errorf("Get(%q, max 4) error = %v, want ErrObjectTooLarge", key, err)
	}
}

func TestClientGetsOpaqueGzipEncodedObject(t *testing.T) {
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
		writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		writer.Header().Set("ETag", `"opaque-etag"`)
		writer.WriteHeader(http.StatusOK)
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

	data, object, err := client.Get(context.Background(), "blocks/aa/value.block", int64(len(payload)))
	if err != nil {
		t.Fatalf("Get(gzip-encoded object) returned error: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Errorf("Get(gzip-encoded object) returned transformed bytes")
	}
	if got, want := object.Size, int64(len(payload)); got != want {
		t.Errorf("Get(gzip-encoded object).Size = %d, want %d", got, want)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Amz-Bucket-Region", "us-west-2")
		writer.Header().Set("Content-Type", "application/xml")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, `<Error><Code>AccessDenied</Code><Message>denied</Message><RequestId>request-123</RequestId></Error>`)
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
	_, _, err := client.Get(context.Background(), "blocks/aa/missing.block", 1024)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Get(access denied) error = %v, want *APIError", err)
	}
	want := &APIError{
		StatusCode: http.StatusForbidden,
		Code:       "AccessDenied",
		Message:    "denied",
		RequestID:  "request-123",
		Region:     "us-west-2",
	}
	if diff := cmp.Diff(want, apiErr, cmpopts.IgnoreUnexported(APIError{})); diff != "" {
		t.Errorf("Get(access denied) APIError mismatch (-want +got):\n%s", diff)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("Get(access denied) error = %v, must not match ErrNotFound", err)
	}
}

func TestReadAPIErrorPreservesBodyErrors(t *testing.T) {
	t.Parallel()

	response := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     make(http.Header),
		Body:       errorReadCloser{err: context.Canceled},
	}
	err := readAPIError(response, false)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("readAPIError() error = %v, want context.Canceled", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readAPIError() error = %v, want HTTP 503 *APIError", err)
	}
}

func TestClientDoesNotTreatMissingBucketAsMissingObject(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/xml")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, `<Error><Code>NoSuchBucket</Code><Message>bucket missing</Message></Error>`)
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
	_, _, err := client.Get(context.Background(), "blocks/aa/value.block", 1024)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "NoSuchBucket" {
		t.Fatalf("Get(missing bucket) error = %v, want NoSuchBucket *APIError", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing bucket) error = %v, must not match ErrNotFound", err)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	client := mustClient(t, Options{
		Endpoint:     source.URL,
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   source.Client(),
		MaxAttempts:  1,
	})
	_, _, err := client.Get(context.Background(), "blocks/aa/value.block", 1024)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Get(redirect) error = %v, want 307 *APIError", err)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Errorf("Get(redirect) sent %d requests to redirect target, want 0", got)
	}
}

func TestClientDoesNotUseSuppliedCookieJar(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "" {
			writeFakeError(writer, http.StatusBadRequest, "InvalidRequest", "unexpected cookie")
			return
		}
		writer.Header().Set("Set-Cookie", "provider=mutation; Path=/; Secure")
		writer.Header().Set("Content-Length", "0")
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() returned error: %v", err)
	}
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) returned error: %v", server.URL, err)
	}
	jar.SetCookies(endpoint, []*http.Cookie{{Name: "ambient", Value: "secret", Path: "/", Secure: true}})
	httpClient := server.Client()
	httpClient.Jar = jar
	client := mustClient(t, Options{
		Endpoint:     server.URL,
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
		HTTPClient:   httpClient,
		MaxAttempts:  1,
	})
	if _, err := client.Stat(context.Background(), "blocks/aa/value.block"); err != nil {
		t.Fatalf("Stat(with supplied cookie jar) returned error: %v", err)
	}
	for _, cookie := range jar.Cookies(endpoint) {
		if cookie.Name == "provider" {
			t.Errorf("Stat() mutated the supplied cookie jar with %q", cookie.String())
		}
	}
}

func TestClientHonorsCanceledContextBeforeRequest(t *testing.T) {
	t.Parallel()

	store := newMemoryS3(testBucket)
	client := newTestClient(t, store, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Put(ctx, "blocks/aa/value.block", []byte("value")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put(canceled context) error = %v, want context.Canceled", err)
	}
	if got := len(store.Calls()); got != 0 {
		t.Errorf("Put(canceled context) made %d requests, want 0", got)
	}
}

func TestClientRetriesInvalidSuccessfulList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		firstResponse string
	}{
		{name: "malformed_xml", firstResponse: `<ListBucketResult>`},
		{
			name: "missing_object_size",
			firstResponse: `<ListBucketResult><IsTruncated>false</IsTruncated>` +
				`<Contents><Key>blocks%2Faa%2Fvalue.block</Key></Contents></ListBucketResult>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/xml")
				if attempts.Add(1) == 1 {
					_, _ = io.WriteString(writer, test.firstResponse)
					return
				}
				_, _ = io.WriteString(writer, `<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>false</IsTruncated></ListBucketResult>`)
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
			if _, err := client.List(context.Background(), ListOptions{}); err != nil {
				t.Fatalf("List(invalid first response) returned error: %v", err)
			}
			if got, want := attempts.Load(), int32(2); got != want {
				t.Errorf("List(invalid first response) attempts = %d, want %d", got, want)
			}
		})
	}
}

func TestParseListRequiresStructuralFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
	}{
		{name: "is_truncated", response: `<ListBucketResult/>`},
		{
			name: "object_size",
			response: `<ListBucketResult><IsTruncated>false</IsTruncated>` +
				`<Contents><Key>blocks/value.block</Key></Contents></ListBucketResult>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseList([]byte(test.response), maxListKeys); err == nil {
				t.Error("parseList(structurally incomplete response) error = nil, want error")
			}
		})
	}
}

func TestParseListBoundsContinuationToken(t *testing.T) {
	t.Parallel()

	response := []byte("<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>" +
		strings.Repeat("a", maxContinuation+1) +
		"</NextContinuationToken></ListBucketResult>")
	if _, err := parseList(response, maxListKeys); err == nil {
		t.Error("parseList(oversized continuation token) error = nil, want error")
	}
}

func TestClientBoundsListResponses(t *testing.T) {
	base := []byte(`<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>false</IsTruncated></ListBucketResult>`)
	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{name: "exact_bound", size: maxListResponse},
		{name: "over_bound", size: maxListResponse + 1, wantErr: ErrResponseTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := make([]byte, test.size)
			copy(body, base)
			for i := range len(body) - len(base) {
				body[len(base)+i] = ' '
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/xml")
				_, _ = writer.Write(body)
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
			_, err := client.List(context.Background(), ListOptions{})
			if !errors.Is(err, test.wantErr) {
				t.Errorf("List(response size %d) error = %v, want %v", test.size, err, test.wantErr)
			}
		})
	}
}

func TestNewValidatesEndpointAndAddressing(t *testing.T) {
	t.Parallel()

	base := Options{
		Endpoint:     "https://s3.us-east-1.amazonaws.com",
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
	}
	tests := []struct {
		name   string
		change func(*Options)
	}{
		{name: "http_endpoint", change: func(options *Options) { options.Endpoint = "http://s3.example.com" }},
		{name: "empty_endpoint_host", change: func(options *Options) { options.Endpoint = "https://:443" }},
		{name: "endpoint_path", change: func(options *Options) { options.Endpoint += "/base" }},
		{name: "endpoint_query", change: func(options *Options) { options.Endpoint += "?key=value" }},
		{name: "empty_region", change: func(options *Options) { options.Region = "" }},
		{name: "invalid_bucket", change: func(options *Options) { options.Bucket = "Bad_Bucket" }},
		{name: "missing_access_key", change: func(options *Options) { options.Credentials.AccessKeyID = "" }},
		{name: "invalid_access_key", change: func(options *Options) { options.Credentials.AccessKeyID = "key/value" }},
		{name: "missing_secret", change: func(options *Options) { options.Credentials.SecretAccessKey = "" }},
		{name: "oversized_secret", change: func(options *Options) {
			options.Credentials.SecretAccessKey = strings.Repeat("a", maxCredentialBytes+1)
		}},
		{name: "invalid_session_token", change: func(options *Options) { options.Credentials.SessionToken = "token with spaces" }},
		{name: "non_ascii_session_token", change: func(options *Options) { options.Credentials.SessionToken = "token-雪" }},
		{name: "oversized_session_token", change: func(options *Options) { options.Credentials.SessionToken = strings.Repeat("a", maxCredentialBytes+1) }},
		{name: "unknown_style", change: func(options *Options) { options.AddressStyle = 99 }},
		{name: "dotted_virtual_bucket", change: func(options *Options) { options.Bucket = "backup.bucket"; options.AddressStyle = VirtualHostStyle }},
		{name: "too_many_attempts", change: func(options *Options) { options.MaxAttempts = maxAttempts + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.change(&options)
			if _, err := New(options); err == nil {
				t.Errorf("New(%s) error = nil, want validation error", test.name)
			}
		})
	}
}

func TestSecretFormattingIsRedacted(t *testing.T) {
	t.Parallel()

	credentials := Credentials{
		AccessKeyID:     "ACCESSMARKER123",
		SecretAccessKey: "SECRET-MARKER-456",
		SessionToken:    "SESSION-MARKER-789",
	}
	options := Options{
		Endpoint:     "https://s3.us-east-1.amazonaws.com",
		Region:       "us-east-1",
		Bucket:       testBucket,
		AddressStyle: VirtualHostStyle,
		Credentials:  credentials,
		MaxAttempts:  2,
	}
	client := mustClient(t, options)
	values := []struct {
		name  string
		value any
	}{
		{name: "credentials", value: credentials},
		{name: "options", value: options},
		{name: "client", value: client},
	}
	markers := []string{credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			logger.Info("format value", "value", value.value)
			formatted := strings.Join([]string{
				fmt.Sprintf("%v", value.value),
				fmt.Sprintf("%+v", value.value),
				fmt.Sprintf("%#v", value.value),
				fmt.Sprintf("%d", value.value),
				logs.String(),
			}, "\n")
			for _, marker := range markers {
				if strings.Contains(formatted, marker) {
					t.Errorf("formatted %s contains credential marker %q", value.name, marker)
				}
			}
			if !strings.Contains(formatted, redactedValue) {
				t.Errorf("formatted %s = %q, want redaction marker", value.name, formatted)
			}
		})
	}
}

func TestProviderRequestShapes(t *testing.T) {
	t.Parallel()

	key := "blocks/aa/value.block"
	tests := []struct {
		name     string
		endpoint string
		region   string
		style    AddressStyle
		wantURL  string
	}{
		{
			name:     "aws",
			endpoint: "https://s3.us-east-1.amazonaws.com",
			region:   "us-east-1",
			style:    VirtualHostStyle,
			wantURL:  "https://backup-bucket.s3.us-east-1.amazonaws.com/blocks/aa/value.block",
		},
		{
			name:     "cloudflare_r2",
			endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com",
			region:   "auto",
			style:    PathStyle,
			wantURL:  "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/backup-bucket/blocks/aa/value.block",
		},
		{
			name:     "backblaze_b2",
			endpoint: "https://s3.us-west-004.backblazeb2.com",
			region:   "us-west-004",
			style:    PathStyle,
			wantURL:  "https://s3.us-west-004.backblazeb2.com/backup-bucket/blocks/aa/value.block",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := mustClient(t, Options{
				Endpoint:     test.endpoint,
				Region:       test.region,
				Bucket:       testBucket,
				AddressStyle: test.style,
				Credentials:  Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey},
			})
			requestURL := client.requestURL(key, true, nil)
			if got := requestURL.String(); got != test.wantURL {
				t.Errorf("requestURL(%q, %s) = %q, want %q", key, test.name, got, test.wantURL)
			}
			request, err := client.newObjectRequest(context.Background(), http.MethodHead, key, nil, nil, emptyPayloadHash)
			if err != nil {
				t.Fatalf("newObjectRequest(%q, %s) returned error: %v", key, test.name, err)
			}
			if err := client.sign(request, emptyPayloadHash, time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)); err != nil {
				t.Fatalf("sign(%q, %s) returned error: %v", key, test.name, err)
			}
			if wantScope := "/20260711/" + test.region + "/s3/aws4_request"; !strings.Contains(request.Header.Get("Authorization"), wantScope) {
				t.Errorf("sign(%q, %s) Authorization = %q, want scope %q", key, test.name, request.Header.Get("Authorization"), wantScope)
			}
		})
	}
}

func TestWaitBeforeRetryHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitBeforeRetry(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("waitBeforeRetry(canceled context) error = %v, want context.Canceled", err)
	}
}

type capturedRequest struct {
	Method        string
	RequestURI    string
	Authorization string
	SessionToken  string
	Body          []byte
}

type memoryS3 struct {
	mu       sync.Mutex
	bucket   string
	objects  map[string][]byte
	calls    []capturedRequest
	failPuts int
}

func newMemoryS3(bucket string) *memoryS3 {
	return &memoryS3{bucket: bucket, objects: make(map[string][]byte)}
}

func (s *memoryS3) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeFakeError(writer, http.StatusBadRequest, "InvalidRequest", "read request body")
		return
	}
	digest := sha256.Sum256(body)
	if got, want := request.Header.Get("X-Amz-Content-Sha256"), hex.EncodeToString(digest[:]); got != want {
		writeFakeError(writer, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "payload hash mismatch")
		return
	}
	if request.Header.Get("Authorization") == "" {
		writeFakeError(writer, http.StatusForbidden, "AccessDenied", "missing authorization")
		return
	}

	s.mu.Lock()
	s.calls = append(s.calls, capturedRequest{
		Method:        request.Method,
		RequestURI:    request.RequestURI,
		Authorization: request.Header.Get("Authorization"),
		SessionToken:  request.Header.Get("X-Amz-Security-Token"),
		Body:          bytes.Clone(body),
	})
	if request.Method == http.MethodPut && s.failPuts > 0 {
		s.failPuts--
		s.mu.Unlock()
		writeFakeError(writer, http.StatusServiceUnavailable, "SlowDown", "retry later")
		return
	}
	s.mu.Unlock()

	bucketPath := "/" + s.bucket
	if request.URL.Path == bucketPath && request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2" {
		s.list(writer, request)
		return
	}
	prefix := bucketPath + "/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		writeFakeError(writer, http.StatusNotFound, "NoSuchBucket", "bucket not found")
		return
	}
	key := strings.TrimPrefix(request.URL.Path, prefix)
	s.object(writer, request, key, body)
}

func (s *memoryS3) object(writer http.ResponseWriter, request *http.Request, key string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch request.Method {
	case http.MethodPut:
		s.objects[key] = bytes.Clone(body)
		writer.Header().Set("ETag", `"fake-etag"`)
		writer.WriteHeader(http.StatusOK)
	case http.MethodHead:
		data, exists := s.objects[key]
		if !exists {
			writeFakeError(writer, http.StatusNotFound, "NoSuchKey", "key not found")
			return
		}
		writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
		writer.Header().Set("ETag", `"fake-etag"`)
		writer.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, exists := s.objects[key]
		if !exists {
			writeFakeError(writer, http.StatusNotFound, "NoSuchKey", "key not found")
			return
		}
		writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
		writer.Header().Set("ETag", `"fake-etag"`)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(data)
	case http.MethodDelete:
		delete(s.objects, key)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writeFakeError(writer, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
	}
}

func (s *memoryS3) list(writer http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := request.URL.Query().Get("prefix")
	continuation := request.URL.Query().Get("continuation-token")
	limit, err := strconv.Atoi(request.URL.Query().Get("max-keys"))
	if err != nil || limit < 1 {
		writeFakeError(writer, http.StatusBadRequest, "InvalidArgument", "invalid max-keys")
		return
	}
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > continuation {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	result := fakeListResult{
		XMLNS:        "http://s3.amazonaws.com/doc/2006-03-01/",
		EncodingType: "url",
		IsTruncated:  truncated,
	}
	for _, key := range keys {
		result.Contents = append(result.Contents, fakeListObject{
			Key:  uriEncode(key, true),
			Size: int64(len(s.objects[key])),
			ETag: `"fake-etag"`,
		})
	}
	if truncated {
		result.NextContinuationToken = keys[len(keys)-1]
	}
	writer.Header().Set("Content-Type", "application/xml")
	if err := xml.NewEncoder(writer).Encode(result); err != nil {
		return
	}
}

func (s *memoryS3) Calls() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make([]capturedRequest, len(s.calls))
	for i, call := range s.calls {
		calls[i] = call
		calls[i].Body = bytes.Clone(call.Body)
	}
	return calls
}

type fakeListResult struct {
	XMLName               xml.Name         `xml:"ListBucketResult"`
	XMLNS                 string           `xml:"xmlns,attr"`
	EncodingType          string           `xml:"EncodingType"`
	IsTruncated           bool             `xml:"IsTruncated"`
	NextContinuationToken string           `xml:"NextContinuationToken,omitempty"`
	Contents              []fakeListObject `xml:"Contents"`
}

type fakeListObject struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
	ETag string `xml:"ETag"`
}

func writeFakeError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/xml")
	writer.Header().Set("X-Amz-Request-Id", "fake-request")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, "<Error><Code>%s</Code><Message>%s</Message><RequestId>fake-request</RequestId></Error>", code, message)
}

func newTestClient(t testing.TB, handler http.Handler, credentials Credentials) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := mustClient(t, Options{
		Endpoint:     server.URL,
		Region:       "auto",
		Bucket:       testBucket,
		AddressStyle: PathStyle,
		Credentials:  credentials,
		HTTPClient:   server.Client(),
		MaxAttempts:  3,
	})
	client.now = func() time.Time {
		return time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	}
	client.wait = noWait
	return client
}

func mustClient(t testing.TB, options Options) *Client {
	t.Helper()
	client, err := New(options)
	if err != nil {
		t.Fatalf("New(%q) returned error: %v", options.Endpoint, err)
	}
	return client
}

func noWait(ctx context.Context, _ int) error {
	return ctx.Err()
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read(_ []byte) (int, error) {
	return 0, r.err
}

func (errorReadCloser) Close() error {
	return nil
}
