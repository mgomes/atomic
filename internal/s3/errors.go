package s3

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
)

const maxErrorResponse = 64 << 10

var (
	// ErrNotFound matches NoSuchKey from Get and HTTP 404 from Stat. S3 HEAD
	// responses cannot distinguish a missing key from a missing bucket.
	ErrNotFound = errors.New("S3 object not found")
	// ErrObjectTooLarge reports a Get response beyond the caller's bound.
	ErrObjectTooLarge = errors.New("S3 object is too large")
	// ErrResponseTooLarge reports an unexpectedly large protocol response.
	ErrResponseTooLarge = errors.New("S3 response is too large")
)

// APIError describes a non-successful S3 response without exposing its raw
// body or request authorization.
type APIError struct {
	// StatusCode is the HTTP response status.
	StatusCode int
	// Code is the provider's machine-readable S3 error code when available.
	Code string
	// Message is the provider's human-readable error description when available.
	Message string
	// RequestID identifies the request in provider support systems.
	RequestID string
	// HostID contains AWS's extended request identifier when available.
	HostID string
	// Region contains the provider's corrected bucket region when available.
	Region string

	objectNotFound bool
}

// Error returns a concise provider error with its request ID when available.
func (e *APIError) Error() string {
	message := fmt.Sprintf("S3 returned HTTP %d", e.StatusCode)
	if e.Code != "" {
		message += " " + e.Code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	if e.RequestID != "" {
		message += " (request " + e.RequestID + ")"
	}
	return message
}

// Is allows errors.Is to match ErrNotFound for classified missing objects.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.objectNotFound
}

type errorResponse struct {
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
	RequestID string `xml:"RequestId"`
	HostID    string `xml:"HostId"`
	Region    string `xml:"Region"`
}

func readAPIError(response *http.Response, objectLookup bool) error {
	body, tooLarge, bodyErr := readAndClose(response.Body, maxErrorResponse)
	var parsed errorResponse
	_ = xml.Unmarshal(body, &parsed)
	if parsed.Code == "" {
		parsed.Code = response.Header.Get("X-Amz-Error-Code")
	}

	requestID := parsed.RequestID
	if requestID == "" {
		requestID = response.Header.Get("X-Amz-Request-Id")
	}
	hostID := parsed.HostID
	if hostID == "" {
		hostID = response.Header.Get("X-Amz-Id-2")
	}
	region := parsed.Region
	if region == "" {
		region = response.Header.Get("X-Amz-Bucket-Region")
	}
	statusOnlyHead := parsed.Code == "" && response.Request != nil && response.Request.Method == http.MethodHead
	objectNotFound := objectLookup && response.StatusCode == http.StatusNotFound &&
		(statusOnlyHead || parsed.Code == "NoSuchKey" || parsed.Code == "NotFound")
	apiErr := &APIError{
		StatusCode:     response.StatusCode,
		Code:           parsed.Code,
		Message:        parsed.Message,
		RequestID:      requestID,
		HostID:         hostID,
		Region:         region,
		objectNotFound: objectNotFound,
	}
	if tooLarge {
		bodyErr = errors.Join(bodyErr, fmt.Errorf("%w: error response exceeds %d bytes", ErrResponseTooLarge, maxErrorResponse))
	}
	if bodyErr != nil {
		return errors.Join(apiErr, bodyErr)
	}
	return apiErr
}
