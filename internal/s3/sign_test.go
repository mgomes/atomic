package s3

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/aws/signatures.json
var awsSignatureFixtures []byte

type signatureFixture struct {
	Name          string            `json:"name"`
	Method        string            `json:"method"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	Authorization string            `json:"authorization"`
}

func TestSignMatchesAWSS3Examples(t *testing.T) {
	t.Parallel()

	var fixtures []signatureFixture
	if err := json.Unmarshal(awsSignatureFixtures, &fixtures); err != nil {
		t.Fatalf("Unmarshal(AWS signature fixtures) returned error: %v", err)
	}
	client := &Client{
		region: "us-east-1",
		credentials: Credentials{
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
	}
	at := time.Date(2013, time.May, 24, 0, 0, 0, 0, time.UTC)
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			request, err := http.NewRequest(fixture.Method, fixture.URL, strings.NewReader(fixture.Body))
			if err != nil {
				t.Fatalf("NewRequest(%q) returned error: %v", fixture.URL, err)
			}
			for name, value := range fixture.Headers {
				request.Header.Set(name, value)
			}
			payloadHash := sha256.Sum256([]byte(fixture.Body))
			if err := client.sign(request, payloadHash, at); err != nil {
				t.Fatalf("sign(%s %s) returned error: %v", fixture.Method, fixture.URL, err)
			}
			if got := request.Header.Get("Authorization"); got != fixture.Authorization {
				t.Errorf("sign(%s %s) Authorization = %q, want %q", fixture.Method, fixture.URL, got, fixture.Authorization)
			}
		})
	}
}

func TestCanonicalRawQueryUsesAWSByteEncoding(t *testing.T) {
	t.Parallel()

	raw := "token=next%2b%2f%3d&empty&prefix=hello%20world&duplicate=z&duplicate=a"
	got, err := canonicalRawQuery(raw)
	if err != nil {
		t.Fatalf("canonicalRawQuery(%q) returned error: %v", raw, err)
	}
	want := "duplicate=a&duplicate=z&empty=&prefix=hello%20world&token=next%2B%2F%3D"
	if got != want {
		t.Errorf("canonicalRawQuery(%q) = %q, want %q", raw, got, want)
	}
}

func TestCanonicalRawQueryRejectsMalformedEscapes(t *testing.T) {
	t.Parallel()

	if _, err := canonicalRawQuery("token=%zz"); err == nil {
		t.Error("canonicalRawQuery(malformed escape) error = nil, want error")
	}
}

func TestCanonicalURIPreservesS3PathShape(t *testing.T) {
	t.Parallel()

	requestURL := &url.URL{Path: "/bucket/a//b $/café"}
	got, err := canonicalURI(requestURL)
	if err != nil {
		t.Fatalf("canonicalURI(%q) returned error: %v", requestURL.Path, err)
	}
	want := "/bucket/a//b%20%24/caf%C3%A9"
	if got != want {
		t.Errorf("canonicalURI(%q) = %q, want %q", requestURL.Path, got, want)
	}
}

func TestCanonicalHeaderValueCollapsesSpaces(t *testing.T) {
	t.Parallel()

	got, err := canonicalHeaderValue("  one\t two   three  ")
	if err != nil {
		t.Fatalf("canonicalHeaderValue() returned error: %v", err)
	}
	if want := "one two three"; got != want {
		t.Errorf("canonicalHeaderValue() = %q, want %q", got, want)
	}
}

func FuzzURIEncodeRoundTrip(f *testing.F) {
	f.Add("blocks/ab/object +.block")
	f.Add("café/雪")
	f.Fuzz(func(t *testing.T, value string) {
		encoded := uriEncode(value, false)
		if strings.Contains(encoded, "+") {
			t.Errorf("uriEncode(%q) = %q, want no plus encoding", value, encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil {
			t.Fatalf("PathUnescape(uriEncode(%q)) returned error: %v", value, err)
		}
		if decoded != value {
			t.Errorf("PathUnescape(uriEncode(%q)) = %q, want original value", value, decoded)
		}
	})
}
