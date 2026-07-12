package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const signingAlgorithm = "AWS4-HMAC-SHA256"

var emptyPayloadHash = sha256.Sum256(nil)

type queryPair struct {
	name  string
	value string
}

func (c *Client) sign(request *http.Request, payloadHash [sha256.Size]byte, at time.Time) error {
	timestamp := at.UTC().Format("20060102T150405Z")
	request.Header.Set("X-Amz-Date", timestamp)
	request.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(payloadHash[:]))
	if c.credentials.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", c.credentials.SessionToken)
	} else {
		request.Header.Del("X-Amz-Security-Token")
	}

	canonical, signedHeaders, err := canonicalRequest(request, payloadHash)
	if err != nil {
		return fmt.Errorf("canonicalize S3 request: %w", err)
	}
	date := at.UTC().Format("20060102")
	scope := date + "/" + c.region + "/s3/aws4_request"
	canonicalDigest := sha256.Sum256([]byte(canonical))
	stringToSign := strings.Join([]string{
		signingAlgorithm,
		timestamp,
		scope,
		hex.EncodeToString(canonicalDigest[:]),
	}, "\n")

	rootKey := append([]byte("AWS4"), c.credentials.SecretAccessKey...)
	defer clear(rootKey)
	dateKey := hmacSHA256(rootKey, date)
	regionKey := hmacSHA256(dateKey, c.region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	defer clear(dateKey)
	defer clear(regionKey)
	defer clear(serviceKey)
	defer clear(signingKey)
	signature := hmacSHA256(signingKey, stringToSign)
	request.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		signingAlgorithm,
		c.credentials.AccessKeyID,
		scope,
		signedHeaders,
		hex.EncodeToString(signature),
	))
	return nil
}

func canonicalRequest(request *http.Request, payloadHash [sha256.Size]byte) (string, string, error) {
	uri, err := canonicalURI(request.URL)
	if err != nil {
		return "", "", err
	}
	query, err := canonicalRawQuery(request.URL.RawQuery)
	if err != nil {
		return "", "", err
	}
	headers, signedHeaders, err := canonicalHeaders(request)
	if err != nil {
		return "", "", err
	}
	return strings.Join([]string{
		request.Method,
		uri,
		query,
		headers,
		signedHeaders,
		hex.EncodeToString(payloadHash[:]),
	}, "\n"), signedHeaders, nil
}

func canonicalURI(requestURL *url.URL) (string, error) {
	escaped := requestURL.EscapedPath()
	if escaped == "" {
		escaped = "/"
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", fmt.Errorf("decode S3 request path: %w", err)
	}
	return uriEncode(decoded, false), nil
}

func canonicalRawQuery(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	pairs := make([]queryPair, 0, strings.Count(raw, "&")+1)
	for _, part := range strings.Split(raw, "&") {
		name, value, _ := strings.Cut(part, "=")
		decodedName, err := url.PathUnescape(name)
		if err != nil {
			return "", fmt.Errorf("decode S3 query name: %w", err)
		}
		decodedValue, err := url.PathUnescape(value)
		if err != nil {
			return "", fmt.Errorf("decode S3 query value: %w", err)
		}
		pairs = append(pairs, queryPair{name: decodedName, value: decodedValue})
	}
	return encodeQuery(pairs), nil
}

func encodeQuery(pairs []queryPair) string {
	encoded := make([]queryPair, len(pairs))
	for i, pair := range pairs {
		encoded[i] = queryPair{
			name:  uriEncode(pair.name, true),
			value: uriEncode(pair.value, true),
		}
	}
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i].name == encoded[j].name {
			return encoded[i].value < encoded[j].value
		}
		return encoded[i].name < encoded[j].name
	})
	var builder strings.Builder
	for i, pair := range encoded {
		if i > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString(pair.name)
		builder.WriteByte('=')
		builder.WriteString(pair.value)
	}
	return builder.String()
}

func canonicalHeaders(request *http.Request) (string, string, error) {
	values := make(map[string][]string)
	host := request.Host
	if host == "" {
		host = request.URL.Host
	}
	values["host"] = []string{host}
	for name, headerValues := range request.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || !strings.HasPrefix(lower, "x-amz-") && !stableSignedHeader(lower) {
			continue
		}
		if !validHeaderName(lower) {
			return "", "", fmt.Errorf("S3 signed header name %q is invalid", name)
		}
		values[lower] = append(values[lower], headerValues...)
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for _, name := range names {
		normalized := make([]string, len(values[name]))
		for i, value := range values[name] {
			var err error
			normalized[i], err = canonicalHeaderValue(value)
			if err != nil {
				return "", "", fmt.Errorf("canonicalize S3 header %q: %w", name, err)
			}
		}
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(strings.Join(normalized, ","))
		builder.WriteByte('\n')
	}
	return builder.String(), strings.Join(names, ";"), nil
}

func stableSignedHeader(name string) bool {
	switch name {
	case "content-md5", "content-type", "date", "if-match", "if-none-match", "range":
		return true
	default:
		return false
	}
}

func canonicalHeaderValue(value string) (string, error) {
	var builder strings.Builder
	pendingSpace := false
	for i := range len(value) {
		character := value[i]
		switch {
		case character == ' ' || character == '\t':
			pendingSpace = builder.Len() > 0
		case character < 0x20 || character == 0x7f:
			return "", errors.New("contains a control character")
		default:
			if pendingSpace {
				builder.WriteByte(' ')
				pendingSpace = false
			}
			builder.WriteByte(character)
		}
	}
	return builder.String(), nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		character := name[i]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func uriEncode(value string, encodeSlash bool) string {
	const hexadecimal = "0123456789ABCDEF"
	var builder strings.Builder
	builder.Grow(len(value))
	for i := range len(value) {
		character := value[i]
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '.' || character == '_' || character == '~' ||
			character == '/' && !encodeSlash {
			builder.WriteByte(character)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexadecimal[character>>4])
		builder.WriteByte(hexadecimal[character&0x0f])
	}
	return builder.String()
}

func hmacSHA256(key []byte, value string) []byte {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	return digest.Sum(nil)
}
