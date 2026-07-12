package s3

import (
	"fmt"
	"log/slog"
)

const redactedValue = "<redacted>"

// String returns the address style's configuration name.
func (a AddressStyle) String() string {
	switch a {
	case PathStyle:
		return "path"
	case VirtualHostStyle:
		return "virtual-host"
	default:
		return fmt.Sprintf("unknown(%d)", a)
	}
}

// String returns a redacted description of the credentials.
func (c Credentials) String() string {
	return "Credentials{AccessKeyID:<redacted> SecretAccessKey:<redacted> SessionToken:<redacted>}"
}

// GoString returns a redacted Go-syntax description of the credentials.
func (c Credentials) GoString() string {
	return c.String()
}

// Format writes a redacted description for every formatting verb.
func (c Credentials) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(c.String()))
}

// LogValue returns structured credentials with every credential value redacted.
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("access_key_id", redactedValue),
		slog.String("secret_access_key", redactedValue),
		slog.String("session_token", redactedValue),
	)
}

// String returns a redacted description of the client options.
func (o Options) String() string {
	return fmt.Sprintf(
		"Options{Endpoint:%q Region:%q Bucket:%q AddressStyle:%s Credentials:%s HTTPClient:%t MaxAttempts:%d}",
		o.Endpoint,
		o.Region,
		o.Bucket,
		o.AddressStyle,
		o.Credentials,
		o.HTTPClient != nil,
		o.MaxAttempts,
	)
}

// GoString returns a redacted Go-syntax description of the client options.
func (o Options) GoString() string {
	return o.String()
}

// Format writes a redacted description for every formatting verb.
func (o Options) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(o.String()))
}

// LogValue returns structured client options with credentials redacted.
func (o Options) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("endpoint", o.Endpoint),
		slog.String("region", o.Region),
		slog.String("bucket", o.Bucket),
		slog.String("address_style", o.AddressStyle.String()),
		slog.Any("credentials", o.Credentials),
		slog.Bool("http_client", o.HTTPClient != nil),
		slog.Int("max_attempts", o.MaxAttempts),
	)
}

// String returns a redacted description of the client.
func (c *Client) String() string {
	if c == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"Client{Endpoint:%q Region:%q Bucket:%q AddressStyle:%s Credentials:<redacted> MaxAttempts:%d}",
		c.endpoint.String(),
		c.region,
		c.bucket,
		c.addressStyle,
		c.maxAttempts,
	)
}

// GoString returns a redacted Go-syntax description of the client.
func (c *Client) GoString() string {
	return c.String()
}

// Format writes a redacted description for every formatting verb.
func (c *Client) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(c.String()))
}

// LogValue returns structured client state with credentials redacted.
func (c *Client) LogValue() slog.Value {
	if c == nil {
		return slog.StringValue("<nil>")
	}
	return slog.GroupValue(
		slog.String("endpoint", c.endpoint.String()),
		slog.String("region", c.region),
		slog.String("bucket", c.bucket),
		slog.String("address_style", c.addressStyle.String()),
		slog.String("credentials", redactedValue),
		slog.Int("max_attempts", c.maxAttempts),
	)
}
