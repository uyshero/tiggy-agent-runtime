package mcp

import (
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// StreamableHTTPOptions 配置 Streamable HTTP MCP 端点。
// 自定义 Headers 会被拷贝，应用于显式提供的凭据。
type StreamableHTTPOptions struct {
	Implementation      Implementation
	Endpoint            string
	HTTPClient          *http.Client
	Headers             http.Header
	AllowInsecureHTTP   bool
	EnableStandaloneSSE bool
	MaxSSEReconnects    int
	MaxMessageBytes     int64
}

// NewStreamableHTTPConnector 构造可复用的 Streamable HTTP Connector。
// 明文 HTTP 默认拒绝，除非显式允许或主机为 loopback。
func NewStreamableHTTPConnector(options StreamableHTTPOptions) (Connector, error) {
	if err := options.Implementation.validate(); err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	endpoint, err := validateEndpoint(options.Endpoint, options.AllowInsecureHTTP)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	headers, err := validateHeaders(options.Headers)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	maximum := options.MaxMessageBytes
	if maximum == 0 {
		maximum = defaultMaxWireMessageBytes
	}
	if maximum < 0 || options.MaxSSEReconnects < -1 {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP HTTP limits are invalid")}
	}
	maxRetries := options.MaxSSEReconnects
	if maxRetries == 0 {
		maxRetries = 3
	}
	baseClient := options.HTTPClient
	if baseClient == nil {
		baseClient = &http.Client{}
	}
	clientCopy := *baseClient
	baseTransport := clientCopy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	clientCopy.Transport = &boundedRoundTripper{
		base: baseTransport, headers: headers, maxMessageBytes: maximum,
	}
	implementation := options.Implementation
	enableSSE := options.EnableStandaloneSSE
	return newSDKConnector(implementation, func() (mcpsdk.Transport, error) {
		return &mcpsdk.StreamableClientTransport{
			Endpoint: endpoint, HTTPClient: &clientCopy, MaxRetries: maxRetries,
			DisableStandaloneSSE: !enableSSE,
		}, nil
	})
}

func validateEndpoint(value string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("parse MCP endpoint: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("MCP endpoint must use HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("MCP endpoint host is required and userinfo/fragments are forbidden")
	}
	if parsed.Scheme == "http" && !allowInsecure && !isLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("non-loopback MCP endpoint requires HTTPS")
	}
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validateHeaders(values http.Header) (http.Header, error) {
	result := make(http.Header, len(values))
	for name, entries := range values {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("MCP HTTP header name is invalid")
		}
		canonical := http.CanonicalHeaderKey(name)
		switch strings.ToLower(canonical) {
		case "accept", "content-type", "mcp-protocol-version", "mcp-session-id":
			return nil, fmt.Errorf("MCP protocol header %q cannot be overridden", canonical)
		}
		for _, value := range entries {
			if strings.ContainsAny(value, "\r\n\x00") {
				return nil, fmt.Errorf("MCP HTTP header value is invalid")
			}
			result.Add(canonical, value)
		}
	}
	return result, nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char))) {
			return false
		}
	}
	return true
}

type boundedRoundTripper struct {
	base            http.RoundTripper
	headers         http.Header
	maxMessageBytes int64
}

func (t *boundedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for name, entries := range t.headers {
		for _, value := range entries {
			cloned.Header.Add(name, value)
		}
	}
	response, err := t.base.RoundTrip(cloned)
	if err != nil {
		return nil, err
	}
	if response.Body != nil {
		mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if mediaType == "text/event-stream" {
			response.Body = &boundedEventReadCloser{reader: response.Body, maximum: t.maxMessageBytes}
		} else {
			response.Body = &boundedTotalReadCloser{reader: response.Body, maximum: t.maxMessageBytes}
		}
	}
	return response, nil
}

type boundedTotalReadCloser struct {
	reader  io.ReadCloser
	maximum int64
	current int64
	pending error
}

func (r *boundedTotalReadCloser) Read(value []byte) (int, error) {
	if r.pending != nil {
		return 0, r.pending
	}
	count, err := r.reader.Read(value)
	remaining := r.maximum - r.current
	if int64(count) > remaining {
		accepted := int(remaining)
		r.current += int64(accepted)
		r.pending = fmt.Errorf("MCP HTTP response exceeded %d bytes", r.maximum)
		if accepted == 0 {
			return 0, r.pending
		}
		return accepted, nil
	}
	r.current += int64(count)
	return count, err
}

func (r *boundedTotalReadCloser) Close() error {
	return r.reader.Close()
}

type boundedEventReadCloser struct {
	reader     io.ReadCloser
	maximum    int64
	current    int64
	lineBytes  int64
	lineOnlyCR bool
	pending    error
}

func (r *boundedEventReadCloser) Read(value []byte) (int, error) {
	if r.pending != nil {
		return 0, r.pending
	}
	count, err := r.reader.Read(value)
	for index, current := range value[:count] {
		r.current++
		if current == '\n' {
			emptyLine := r.lineBytes == 0 || r.lineBytes == 1 && r.lineOnlyCR
			r.lineBytes = 0
			r.lineOnlyCR = false
			if emptyLine {
				r.current = 0
			}
		} else {
			if r.lineBytes == 0 {
				r.lineOnlyCR = current == '\r'
			} else {
				r.lineOnlyCR = false
			}
			r.lineBytes++
		}
		if r.current > r.maximum {
			r.pending = fmt.Errorf("MCP SSE event exceeded %d bytes", r.maximum)
			if index == 0 {
				return 0, r.pending
			}
			return index, nil
		}
	}
	return count, err
}

func (r *boundedEventReadCloser) Close() error {
	return r.reader.Close()
}
