package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// Implementation identifies this client to an MCP server.
type Implementation struct {
	Name    string
	Version string
}

func (i Implementation) validate() error {
	if strings.TrimSpace(i.Name) == "" || strings.TrimSpace(i.Name) != i.Name {
		return fmt.Errorf("MCP client name is required without surrounding whitespace")
	}
	if strings.TrimSpace(i.Version) == "" || strings.TrimSpace(i.Version) != i.Version {
		return fmt.Errorf("MCP client version is required without surrounding whitespace")
	}
	if len(i.Name) > 256 || len(i.Version) > 128 {
		return fmt.Errorf("MCP client identity exceeds configured bounds")
	}
	return nil
}

type sdkTransportFactory func() (mcpsdk.Transport, error)

type sdkConnector struct {
	implementation Implementation
	transport      sdkTransportFactory
}

func newSDKConnector(implementation Implementation, transport sdkTransportFactory) (Connector, error) {
	if err := implementation.validate(); err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	if transport == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP transport factory is required")}
	}
	return &sdkConnector{implementation: implementation, transport: transport}, nil
}

func (c *sdkConnector) Connect(ctx context.Context) (Session, error) {
	transport, err := c.transport()
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, fmt.Errorf("MCP transport factory returned nil")
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: c.implementation.Name, Version: c.implementation.Version,
	}, &mcpsdk.ClientOptions{
		Capabilities:   &mcpsdk.ClientCapabilities{},
		MultiRoundTrip: &mcpsdk.MultiRoundTripOptions{Disabled: true},
	})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	return &sdkSession{session: session}, nil
}

type sdkSession struct {
	session *mcpsdk.ClientSession
}

var _ Session = (*sdkSession)(nil)

func (s *sdkSession) ServerInfo() ServerInfo {
	result := s.session.InitializeResult()
	if result == nil {
		return ServerInfo{}
	}
	info := ServerInfo{ProtocolVersion: result.ProtocolVersion}
	if result.ServerInfo != nil {
		info.Name = result.ServerInfo.Name
		info.Version = result.ServerInfo.Version
	}
	return info
}

func (s *sdkSession) ListTools(ctx context.Context, cursor string) (ToolPage, error) {
	result, err := s.session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
	if err != nil {
		return ToolPage{}, err
	}
	if result == nil {
		return ToolPage{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP tools/list returned nil")}
	}
	page := ToolPage{NextCursor: result.NextCursor, Tools: make([]model.ToolDefinition, 0, len(result.Tools))}
	for index, remote := range result.Tools {
		if remote == nil {
			return ToolPage{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP tool %d is nil", index)}
		}
		input, err := marshalSDKValue(remote.InputSchema)
		if err != nil {
			return ToolPage{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP tool %q input schema: %w", remote.Name, err)}
		}
		var output json.RawMessage
		if remote.OutputSchema != nil {
			output, err = marshalSDKValue(remote.OutputSchema)
			if err != nil {
				return ToolPage{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP tool %q output schema: %w", remote.Name, err)}
			}
		}
		page.Tools = append(page.Tools, model.ToolDefinition{
			Name: remote.Name, Description: remote.Description,
			InputSchema: input, OutputSchema: output,
		})
	}
	return page, nil
}

func (s *sdkSession) CallTool(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error) {
	result, err := s.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: name, Arguments: append(json.RawMessage(nil), arguments...),
	})
	if err != nil {
		return CallResult{}, err
	}
	if result == nil {
		return CallResult{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP tools/call returned nil")}
	}
	if result.NeedsInput() {
		return CallResult{}, &Error{Code: ErrorUnsupportedContent, Cause: fmt.Errorf("MCP tool requires unsupported client input")}
	}
	converted := CallResult{IsError: result.IsError, Content: make([]message.Content, 0, len(result.Content))}
	for index, value := range result.Content {
		content, include, err := convertSDKContent(value)
		if err != nil {
			return CallResult{}, &Error{Code: ErrorUnsupportedContent, Cause: fmt.Errorf("MCP content %d: %w", index, err)}
		}
		if include {
			converted.Content = append(converted.Content, content)
		}
	}
	if result.StructuredContent != nil {
		converted.StructuredContent, err = marshalSDKValue(result.StructuredContent)
		if err != nil {
			return CallResult{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP structured content: %w", err)}
		}
	}
	return converted, nil
}

func (s *sdkSession) Ping(ctx context.Context) error {
	return s.session.Ping(ctx, nil)
}

func (s *sdkSession) Close() error {
	return s.session.Close()
}

func marshalSDKValue(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if !json.Valid(encoded) {
		return nil, fmt.Errorf("encoded value is not valid JSON")
	}
	return json.RawMessage(encoded), nil
}

func convertSDKContent(value mcpsdk.Content) (message.Content, bool, error) {
	switch content := value.(type) {
	case *mcpsdk.TextContent:
		if content == nil || content.Text == "" {
			return message.Content{}, false, nil
		}
		return message.Content{Type: message.ContentText, Text: content.Text}, true, nil
	case *mcpsdk.ImageContent:
		if content == nil || strings.TrimSpace(content.MIMEType) == "" || len(content.Data) == 0 {
			return message.Content{}, false, fmt.Errorf("image data and MIME type are required")
		}
		image, err := dataImage(content.MIMEType, content.Data)
		return image, err == nil, err
	case *mcpsdk.ResourceLink:
		if content == nil || strings.TrimSpace(content.URI) == "" {
			return message.Content{}, false, fmt.Errorf("resource link URI is required")
		}
		label := strings.TrimSpace(content.Name)
		if label == "" {
			label = "resource"
		}
		return message.Content{Type: message.ContentText, Text: fmt.Sprintf("MCP resource %q: %s", label, content.URI)}, true, nil
	case *mcpsdk.EmbeddedResource:
		if content == nil || content.Resource == nil {
			return message.Content{}, false, fmt.Errorf("embedded resource is required")
		}
		resource := content.Resource
		if resource.Text != "" {
			prefix := "MCP embedded resource"
			if resource.URI != "" {
				prefix += " " + resource.URI
			}
			return message.Content{Type: message.ContentText, Text: prefix + ":\n" + resource.Text}, true, nil
		}
		if len(resource.Blob) > 0 && strings.HasPrefix(strings.ToLower(resource.MIMEType), "image/") {
			image, err := dataImage(resource.MIMEType, resource.Blob)
			return image, err == nil, err
		}
		if len(resource.Blob) > 0 {
			return message.Content{}, false, fmt.Errorf("binary embedded resource %q cannot be represented", resource.MIMEType)
		}
		if resource.URI != "" {
			return message.Content{Type: message.ContentText, Text: "MCP embedded resource: " + resource.URI}, true, nil
		}
		return message.Content{}, false, fmt.Errorf("embedded resource is empty")
	case *mcpsdk.AudioContent:
		return message.Content{}, false, fmt.Errorf("audio content cannot be represented")
	default:
		return message.Content{}, false, fmt.Errorf("unsupported MCP content type %T", value)
	}
}

func dataImage(mediaType string, data []byte) (message.Content, error) {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err != nil || !strings.HasPrefix(strings.ToLower(parsed), "image/") {
		return message.Content{}, fmt.Errorf("image MIME type is invalid")
	}
	return message.Content{
		Type: message.ContentImage,
		Image: &message.ImageReference{
			URL:       "data:" + parsed + ";base64," + base64.StdEncoding.EncodeToString(data),
			MediaType: parsed,
		},
	}, nil
}
