package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxAttempts = 4
	maxAttempts        = 10
	maxObjectKeyBytes  = 1024
	maxListResponse    = 4 << 20
	maxRangeResponse   = 64 << 20
	maxSuccessResponse = 64 << 10
	maxContinuation    = 64 << 10
	maxCredentialBytes = 64 << 10
	maxListKeys        = 1000
)

var (
	_accessKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
	_regionPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	_bucketPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// AddressStyle controls where the bucket name appears in request URLs.
type AddressStyle uint8

const (
	// PathStyle sends requests to endpoint/bucket/key.
	PathStyle AddressStyle = iota + 1
	// VirtualHostStyle sends requests to bucket.endpoint/key.
	VirtualHostStyle
)

// Credentials contains one S3 access-key pair and an optional session token.
type Credentials struct {
	// AccessKeyID identifies the signing key.
	AccessKeyID string
	// SecretAccessKey derives request signing keys.
	SecretAccessKey string
	// SessionToken is required for temporary credentials.
	SessionToken string
}

// Options configures a Client.
type Options struct {
	// Endpoint is an absolute HTTPS service endpoint without a bucket or path.
	Endpoint string
	// Region is the SigV4 region, such as us-east-1 or auto for Cloudflare R2.
	Region string
	// Bucket is an existing bucket addressed by every request.
	Bucket string
	// AddressStyle must be PathStyle or VirtualHostStyle.
	AddressStyle AddressStyle
	// Credentials signs every request.
	Credentials Credentials
	// HTTPClient optionally supplies transport and timeout policy. Redirects are
	// always disabled on the defensive copy used by Client.
	HTTPClient *http.Client
	// MaxAttempts bounds attempts for replay-safe requests. Zero uses four.
	MaxAttempts int
}

// Object describes opaque object metadata returned by S3.
type Object struct {
	// Key is the provider's decoded object key.
	Key string
	// Size is the stored byte length.
	Size int64
	// ETag is opaque provider metadata and is not an integrity checksum.
	ETag string
}

// ListOptions selects one ListObjectsV2 page.
type ListOptions struct {
	// Prefix restricts returned keys.
	Prefix string
	// ContinuationToken resumes a previous truncated response.
	ContinuationToken string
	// MaxKeys accepts 1 through 1000. Zero requests 1000.
	MaxKeys int
}

// Page contains one ListObjectsV2 response page.
type Page struct {
	// Objects contains at most the requested number of objects.
	Objects []Object
	// NextContinuationToken is nonempty only when another page is available.
	NextContinuationToken string
}

// Client performs the S3 operations needed by Atomic. It is safe for
// concurrent use.
type Client struct {
	endpoint     url.URL
	region       string
	bucket       string
	addressStyle AddressStyle
	credentials  Credentials
	http         *http.Client
	maxAttempts  int
	now          func() time.Time
	wait         func(context.Context, int) error
}

// New validates options and returns a concrete S3 client.
func New(options Options) (*Client, error) {
	endpoint, err := validateOptions(options)
	if err != nil {
		return nil, err
	}

	httpClient := http.DefaultClient
	if options.HTTPClient != nil {
		httpClient = options.HTTPClient
	}
	clientCopy := &http.Client{
		Transport: httpClient.Transport,
		Timeout:   httpClient.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	attempts := options.MaxAttempts
	if attempts == 0 {
		attempts = defaultMaxAttempts
	}
	return &Client{
		endpoint:     *endpoint,
		region:       options.Region,
		bucket:       options.Bucket,
		addressStyle: options.AddressStyle,
		credentials:  options.Credentials,
		http:         clientCopy,
		maxAttempts:  attempts,
		now:          time.Now,
		wait:         waitBeforeRetry,
	}, nil
}

// Put uploads one complete object with a signed SHA-256 payload.
func (c *Client) Put(ctx context.Context, key string, data []byte) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	payload := bytes.Clone(data)
	digest := sha256.Sum256(payload)
	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newObjectRequest(ctx, http.MethodPut, key, nil, bytes.NewReader(payload), digest)
		if err != nil {
			return false, err
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		if err := c.sign(request, digest, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode != http.StatusOK {
			apiErr := readAPIError(response, false)
			return retryable(apiErr), apiErr
		}
		if err := discardResponse(response, maxSuccessResponse); err != nil {
			return !errors.Is(err, ErrResponseTooLarge), err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

// Get downloads one complete object and refuses a body larger than maxBytes.
func (c *Client) Get(ctx context.Context, key string, maxBytes int64) ([]byte, Object, error) {
	if err := validateObjectKey(key); err != nil {
		return nil, Object{}, err
	}
	if maxBytes < 0 {
		return nil, Object{}, errors.New("maximum object size must not be negative")
	}

	var (
		data   []byte
		object Object
	)
	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newObjectRequest(ctx, http.MethodGet, key, nil, nil, emptyPayloadHash)
		if err != nil {
			return false, err
		}
		request.Header.Set("Accept-Encoding", "identity")
		if err := c.sign(request, emptyPayloadHash, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode != http.StatusOK {
			apiErr := readAPIError(response, true)
			return retryable(apiErr), apiErr
		}
		if response.ContentLength > maxBytes {
			_ = response.Body.Close()
			return false, fmt.Errorf("%w: response declares %d bytes, maximum is %d", ErrObjectTooLarge, response.ContentLength, maxBytes)
		}
		body, tooLarge, err := readAndClose(response.Body, maxBytes)
		if err != nil {
			return true, err
		}
		if tooLarge {
			return false, fmt.Errorf("%w: maximum is %d bytes", ErrObjectTooLarge, maxBytes)
		}
		data = body
		object = Object{Key: key, Size: int64(len(body)), ETag: response.Header.Get("ETag")}
		return false, nil
	})
	if err != nil {
		return nil, Object{}, fmt.Errorf("get object %q: %w", key, err)
	}
	return data, object, nil
}

// GetRange downloads exactly length bytes starting at offset, up to 64 MiB. It
// rejects providers that ignore the range or return a different interval.
// Object.Size is the complete stored object size reported by Content-Range.
func (c *Client) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, Object, error) {
	if err := validateObjectKey(key); err != nil {
		return nil, Object{}, err
	}
	if offset < 0 {
		return nil, Object{}, errors.New("range offset must not be negative")
	}
	if length < 1 || length > maxRangeResponse {
		return nil, Object{}, fmt.Errorf("range length must be between 1 and %d bytes", maxRangeResponse)
	}
	if offset > math.MaxInt64-(length-1) {
		return nil, Object{}, errors.New("range end exceeds the maximum signed offset")
	}
	end := offset + length - 1

	var (
		data   []byte
		object Object
	)
	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newObjectRequest(ctx, http.MethodGet, key, nil, nil, emptyPayloadHash)
		if err != nil {
			return false, err
		}
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
		if err := c.sign(request, emptyPayloadHash, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode != http.StatusPartialContent {
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				apiErr := readAPIError(response, true)
				return retryable(apiErr), apiErr
			}
			statusErr := fmt.Errorf("range response returned HTTP %d, want %d", response.StatusCode, http.StatusPartialContent)
			return false, errors.Join(statusErr, response.Body.Close())
		}

		total, err := validateRangeResponse(response, offset, end, length)
		if err != nil {
			return true, errors.Join(err, response.Body.Close())
		}
		body, tooLarge, err := readAndClose(response.Body, length)
		if err != nil {
			return true, err
		}
		if tooLarge {
			return true, fmt.Errorf("range response exceeds requested length %d", length)
		}
		if int64(len(body)) != length {
			return true, fmt.Errorf("range response contains %d bytes, want %d", len(body), length)
		}
		data = body
		object = Object{Key: key, Size: total, ETag: response.Header.Get("ETag")}
		return false, nil
	})
	if err != nil {
		return nil, Object{}, fmt.Errorf("get object %q range %d-%d: %w", key, offset, end, err)
	}
	return data, object, nil
}

// Stat returns metadata for one object without downloading it. S3 omits error
// bodies from HEAD responses, so ErrNotFound cannot distinguish a missing key
// from a missing bucket for this operation.
func (c *Client) Stat(ctx context.Context, key string) (Object, error) {
	if err := validateObjectKey(key); err != nil {
		return Object{}, err
	}

	var object Object
	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newObjectRequest(ctx, http.MethodHead, key, nil, nil, emptyPayloadHash)
		if err != nil {
			return false, err
		}
		if err := c.sign(request, emptyPayloadHash, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode != http.StatusOK {
			apiErr := readAPIError(response, true)
			return retryable(apiErr), apiErr
		}
		if response.ContentLength < 0 {
			_ = response.Body.Close()
			return true, errors.New("successful HEAD response omitted Content-Length")
		}
		if err := discardResponse(response, maxSuccessResponse); err != nil {
			return !errors.Is(err, ErrResponseTooLarge), err
		}
		object = Object{Key: key, Size: response.ContentLength, ETag: response.Header.Get("ETag")}
		return false, nil
	})
	if err != nil {
		return Object{}, fmt.Errorf("stat object %q: %w", key, err)
	}
	return object, nil
}

// Delete removes one object. S3 treats a missing key as a successful delete.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}

	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newObjectRequest(ctx, http.MethodDelete, key, nil, nil, emptyPayloadHash)
		if err != nil {
			return false, err
		}
		if err := c.sign(request, emptyPayloadHash, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			apiErr := readAPIError(response, false)
			return retryable(apiErr), apiErr
		}
		if err := discardResponse(response, maxSuccessResponse); err != nil {
			return !errors.Is(err, ErrResponseTooLarge), err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}

// List returns one bounded ListObjectsV2 page.
func (c *Client) List(ctx context.Context, options ListOptions) (Page, error) {
	if err := validateListOptions(&options); err != nil {
		return Page{}, err
	}

	query := []queryPair{
		{name: "encoding-type", value: "url"},
		{name: "list-type", value: "2"},
		{name: "max-keys", value: strconv.Itoa(options.MaxKeys)},
		{name: "prefix", value: options.Prefix},
	}
	if options.ContinuationToken != "" {
		query = append(query, queryPair{name: "continuation-token", value: options.ContinuationToken})
	}

	var page Page
	err := c.retry(ctx, func() (bool, error) {
		request, err := c.newBucketRequest(ctx, http.MethodGet, query, nil, emptyPayloadHash)
		if err != nil {
			return false, err
		}
		if err := c.sign(request, emptyPayloadHash, c.now()); err != nil {
			return false, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			return ctx.Err() == nil, err
		}
		if response.StatusCode != http.StatusOK {
			apiErr := readAPIError(response, false)
			return retryable(apiErr), apiErr
		}
		body, tooLarge, err := readAndClose(response.Body, maxListResponse)
		if err != nil {
			return true, err
		}
		if tooLarge {
			return false, fmt.Errorf("%w: list response exceeds %d bytes", ErrResponseTooLarge, maxListResponse)
		}
		parsed, err := parseList(body, options.MaxKeys)
		if err != nil {
			return true, err
		}
		page = parsed
		return false, nil
	})
	if err != nil {
		return Page{}, fmt.Errorf("list objects with prefix %q: %w", options.Prefix, err)
	}
	return page, nil
}

func validateOptions(options Options) (*url.URL, error) {
	endpoint, err := url.Parse(options.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse S3 endpoint: %w", err)
	}
	if endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.Hostname() == "" {
		return nil, errors.New("S3 endpoint must be an absolute HTTPS URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("S3 endpoint must not contain user information, a query, or a fragment")
	}
	if endpoint.Path != "" && endpoint.Path != "/" || endpoint.RawPath != "" && endpoint.RawPath != "/" {
		return nil, errors.New("S3 endpoint must not contain a base path")
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	endpoint.Host = strings.ToLower(endpoint.Host)
	if !ascii(endpoint.Hostname()) {
		return nil, errors.New("S3 endpoint host must contain only ASCII characters")
	}

	if !_regionPattern.MatchString(options.Region) {
		return nil, errors.New("S3 region must contain lowercase letters, digits, and hyphens")
	}
	if err := validateBucket(options.Bucket); err != nil {
		return nil, err
	}
	switch options.AddressStyle {
	case PathStyle:
	case VirtualHostStyle:
		if strings.Contains(options.Bucket, ".") {
			return nil, errors.New("virtual-hosted HTTPS does not support bucket names containing dots")
		}
		if net.ParseIP(endpoint.Hostname()) != nil {
			return nil, errors.New("virtual-hosted addressing requires a DNS endpoint")
		}
	default:
		return nil, errors.New("S3 address style must be path or virtual host")
	}
	if err := validateCredentials(options.Credentials); err != nil {
		return nil, err
	}
	if options.MaxAttempts < 0 || options.MaxAttempts > maxAttempts {
		return nil, fmt.Errorf("S3 maximum attempts must be between 1 and %d, or zero for the default", maxAttempts)
	}
	return endpoint, nil
}

func validateBucket(bucket string) error {
	if !_bucketPattern.MatchString(bucket) || strings.Contains(bucket, "..") ||
		strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") ||
		net.ParseIP(bucket) != nil {
		return errors.New("S3 bucket must be a DNS-compatible name containing 3 through 63 lowercase letters, digits, dots, or hyphens")
	}
	return nil
}

func validateCredentials(credentials Credentials) error {
	if !_accessKeyPattern.MatchString(credentials.AccessKeyID) {
		return errors.New("S3 access key ID contains an invalid character")
	}
	if credentials.SecretAccessKey == "" {
		return errors.New("S3 secret access key must not be empty")
	}
	if len(credentials.SecretAccessKey) > maxCredentialBytes {
		return fmt.Errorf("S3 secret access key exceeds %d bytes", maxCredentialBytes)
	}
	if hasControlByte(credentials.SecretAccessKey) {
		return errors.New("S3 secret access key contains a control character")
	}
	if credentials.SessionToken != "" {
		if len(credentials.SessionToken) > maxCredentialBytes {
			return fmt.Errorf("S3 session token exceeds %d bytes", maxCredentialBytes)
		}
		if hasHeaderUnsafeByte(credentials.SessionToken) {
			return errors.New("S3 session token contains an invalid character")
		}
	}
	return nil
}

func validateObjectKey(key string) error {
	if key == "" {
		return errors.New("S3 object key must not be empty")
	}
	if len(key) > maxObjectKeyBytes {
		return fmt.Errorf("S3 object key has %d bytes, maximum is %d", len(key), maxObjectKeyBytes)
	}
	if !utf8.ValidString(key) {
		return errors.New("S3 object key must be valid UTF-8")
	}
	return nil
}

func validateListOptions(options *ListOptions) error {
	if len(options.Prefix) > maxObjectKeyBytes || !utf8.ValidString(options.Prefix) {
		return fmt.Errorf("S3 list prefix must be valid UTF-8 with at most %d bytes", maxObjectKeyBytes)
	}
	if len(options.ContinuationToken) > maxContinuation {
		return fmt.Errorf("S3 continuation token exceeds %d bytes", maxContinuation)
	}
	if options.MaxKeys == 0 {
		options.MaxKeys = maxListKeys
	}
	if options.MaxKeys < 1 || options.MaxKeys > maxListKeys {
		return fmt.Errorf("S3 list maximum keys must be between 1 and %d", maxListKeys)
	}
	return nil
}

func (c *Client) newObjectRequest(
	ctx context.Context,
	method string,
	key string,
	query []queryPair,
	body io.Reader,
	payloadHash [sha256.Size]byte,
) (*http.Request, error) {
	requestURL := c.requestURL(key, true, query)
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create S3 request: %w", err)
	}
	request.URL.Path = requestURL.Path
	request.URL.RawPath = requestURL.RawPath
	request.URL.RawQuery = requestURL.RawQuery
	request.Header.Set("X-Amz-Content-Sha256", fmt.Sprintf("%x", payloadHash))
	return request, nil
}

func (c *Client) newBucketRequest(
	ctx context.Context,
	method string,
	query []queryPair,
	body io.Reader,
	payloadHash [sha256.Size]byte,
) (*http.Request, error) {
	requestURL := c.requestURL("", false, query)
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create S3 request: %w", err)
	}
	request.URL.Path = requestURL.Path
	request.URL.RawPath = requestURL.RawPath
	request.URL.RawQuery = requestURL.RawQuery
	request.Header.Set("X-Amz-Content-Sha256", fmt.Sprintf("%x", payloadHash))
	return request, nil
}

func (c *Client) requestURL(key string, object bool, query []queryPair) url.URL {
	requestURL := c.endpoint
	decodedPath := ""
	encodedPath := ""
	if c.addressStyle == PathStyle {
		decodedPath = "/" + c.bucket
		encodedPath = "/" + uriEncode(c.bucket, true)
	} else {
		requestURL.Host = c.bucket + "." + requestURL.Host
	}
	if object {
		decodedPath += "/" + key
		encodedPath += "/" + uriEncode(key, false)
	} else if decodedPath == "" {
		decodedPath = "/"
		encodedPath = "/"
	}
	requestURL.Path = decodedPath
	requestURL.RawPath = encodedPath
	requestURL.RawQuery = encodeQuery(query)
	return requestURL
}

func (c *Client) retry(ctx context.Context, operation func() (bool, error)) error {
	var lastErr error
	for attemptIndex := range c.maxAttempts {
		attempt := attemptIndex + 1
		if err := ctx.Err(); err != nil {
			return err
		}
		retry, err := operation()
		if err == nil || !retry {
			return err
		}
		lastErr = err
		if attempt == c.maxAttempts {
			break
		}
		if err := c.wait(ctx, attempt); err != nil {
			return err
		}
	}
	return lastErr
}

func waitBeforeRetry(ctx context.Context, attempt int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	maximum := 100 * time.Millisecond * time.Duration(1<<(attempt-1))
	if maximum > 2*time.Second {
		maximum = 2 * time.Second
	}
	delay := time.Duration(rand.Int64N(int64(maximum) + 1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func discardResponse(response *http.Response, limit int64) error {
	_, tooLarge, err := readAndClose(response.Body, limit)
	if err != nil {
		return err
	}
	if tooLarge {
		return fmt.Errorf("%w: successful response exceeds %d bytes", ErrResponseTooLarge, limit)
	}
	return nil
}

func readAndClose(body io.ReadCloser, limit int64) ([]byte, bool, error) {
	data, readErr := io.ReadAll(io.LimitReader(body, limit))
	tooLarge := false
	if readErr == nil && int64(len(data)) == limit {
		var probe [1]byte
		read, probeErr := io.ReadFull(body, probe[:])
		tooLarge = read > 0
		if probeErr != nil && !errors.Is(probeErr, io.EOF) && !errors.Is(probeErr, io.ErrUnexpectedEOF) {
			readErr = probeErr
		}
	}
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, false, err
	}
	if tooLarge {
		return nil, true, nil
	}
	return data, false, nil
}

func validateRangeResponse(response *http.Response, wantStart, wantEnd, wantLength int64) (int64, error) {
	values := response.Header.Values("Content-Range")
	if len(values) != 1 {
		return 0, fmt.Errorf("range response has %d Content-Range headers, want 1", len(values))
	}
	start, end, total, err := parseContentRange(values[0])
	if err != nil {
		return 0, err
	}
	if start != wantStart || end != wantEnd {
		return 0, fmt.Errorf("range response covers bytes %d-%d, want %d-%d", start, end, wantStart, wantEnd)
	}
	if response.ContentLength >= 0 && response.ContentLength != wantLength {
		return 0, fmt.Errorf("range response declares %d bytes, want %d", response.ContentLength, wantLength)
	}
	return total, nil
}

func parseContentRange(value string) (int64, int64, int64, error) {
	const prefix = "bytes "
	if !strings.HasPrefix(value, prefix) {
		return 0, 0, 0, fmt.Errorf("range response Content-Range %q does not start with %q", value, prefix)
	}
	interval, totalText, ok := strings.Cut(strings.TrimPrefix(value, prefix), "/")
	if !ok {
		return 0, 0, 0, fmt.Errorf("range response Content-Range %q has no total size", value)
	}
	startText, endText, ok := strings.Cut(interval, "-")
	if !ok {
		return 0, 0, 0, fmt.Errorf("range response Content-Range %q has no interval end", value)
	}
	start, err := parseDecimal(startText)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse Content-Range start: %w", err)
	}
	end, err := parseDecimal(endText)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse Content-Range end: %w", err)
	}
	total, err := parseDecimal(totalText)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse Content-Range total: %w", err)
	}
	if start > end || end >= total {
		return 0, 0, 0, fmt.Errorf("range response Content-Range %q is inconsistent", value)
	}
	return start, end, total, nil
}

func parseDecimal(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("value is empty")
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return 0, fmt.Errorf("value %q is not decimal", value)
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse decimal value %q: %w", value, err)
	}
	return parsed, nil
}

type listResult struct {
	XMLName               xml.Name     `xml:"ListBucketResult"`
	EncodingType          string       `xml:"EncodingType"`
	IsTruncated           *bool        `xml:"IsTruncated"`
	NextContinuationToken string       `xml:"NextContinuationToken"`
	Contents              []listObject `xml:"Contents"`
}

type listObject struct {
	Key  string `xml:"Key"`
	Size *int64 `xml:"Size"`
	ETag string `xml:"ETag"`
}

func parseList(data []byte, limit int) (Page, error) {
	var result listResult
	decoder := xml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&result); err != nil {
		return Page{}, fmt.Errorf("decode ListObjectsV2 response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Page{}, errors.New("ListObjectsV2 response contains trailing XML")
		}
		return Page{}, fmt.Errorf("decode trailing ListObjectsV2 data: %w", err)
	}
	if len(result.Contents) > limit {
		return Page{}, fmt.Errorf("ListObjectsV2 returned %d objects, requested at most %d", len(result.Contents), limit)
	}
	if result.IsTruncated == nil {
		return Page{}, errors.New("ListObjectsV2 response omitted IsTruncated")
	}
	truncated := *result.IsTruncated
	if truncated && result.NextContinuationToken == "" {
		return Page{}, errors.New("truncated ListObjectsV2 response omitted its continuation token")
	}
	if truncated && len(result.NextContinuationToken) > maxContinuation {
		return Page{}, fmt.Errorf("ListObjectsV2 continuation token exceeds %d bytes", maxContinuation)
	}

	var objects []Object
	if len(result.Contents) > 0 {
		objects = make([]Object, 0, len(result.Contents))
	}
	for _, listed := range result.Contents {
		key := listed.Key
		if result.EncodingType == "url" {
			decoded, err := url.PathUnescape(key)
			if err != nil {
				return Page{}, fmt.Errorf("decode listed S3 object key: %w", err)
			}
			key = decoded
		}
		if err := validateObjectKey(key); err != nil {
			return Page{}, fmt.Errorf("validate listed S3 object: %w", err)
		}
		if listed.Size == nil {
			return Page{}, fmt.Errorf("listed S3 object %q omitted its size", key)
		}
		if *listed.Size < 0 {
			return Page{}, fmt.Errorf("listed S3 object %q has negative size", key)
		}
		objects = append(objects, Object{Key: key, Size: *listed.Size, ETag: listed.ETag})
	}
	nextToken := ""
	if truncated {
		nextToken = result.NextContinuationToken
	}
	return Page{Objects: objects, NextContinuationToken: nextToken}, nil
}

func hasControlByte(value string) bool {
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}

func hasHeaderUnsafeByte(value string) bool {
	for i := range len(value) {
		if value[i] <= 0x20 || value[i] > 0x7e {
			return true
		}
	}
	return false
}

func ascii(value string) bool {
	for i := range len(value) {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

func retryable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	switch apiErr.Code {
	case "InternalError", "RequestTimeout", "ServiceUnavailable", "SlowDown":
		return true
	default:
		return false
	}
}
