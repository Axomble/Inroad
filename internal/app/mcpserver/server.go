// Package mcpserver adapts the shared agenttool registry to MCP.
//
// It deliberately contains no CRM or campaign business logic: OAuth scopes
// filter the registry, and every invocation still executes through the same
// tenant and role checks as the in-app agent.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/inroad/inroad/internal/app/agenttool"
	"github.com/inroad/inroad/internal/app/auth"
)

// VerifyFunc validates the request's OAuth bearer token and returns its
// tenant-scoped identity, expiry, and registered client id.
type VerifyFunc func(context.Context, *http.Request) (principal agenttool.Principal, scopes []string, expiresAt time.Time, clientID string, ok bool, err error)

// Handler is an authenticated, streamable MCP endpoint plus its public OAuth
// protected-resource metadata endpoint.
type Handler struct {
	stream   http.Handler
	metadata http.Handler
}

// New builds a stateless streamable MCP handler. Stateless mode avoids binding
// authorization to an untrusted session id; every request revalidates its
// bearer token and reconstructs the scoped tool view.
func New(reg agenttool.Registry, verify VerifyFunc, resourceURL, issuerURL string) *Handler {
	metadataConfig := oauthMetadata(resourceURL, issuerURL)
	metadata := mcpauth.ProtectedResourceMetadataHandler(&metadataConfig)
	stream := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		info := mcpauth.TokenInfoFromContext(r.Context())
		principal, ok := principalFromTokenInfo(info)
		if !ok {
			return nil
		}
		return serverFor(reg, principal, info.Scopes)
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
		MaxRequestBodyBytes:          1 << 20,
	})
	protected := mcpauth.RequireBearerToken(func(ctx context.Context, _ string, r *http.Request) (*mcpauth.TokenInfo, error) {
		principal, scopes, expiresAt, clientID, ok, err := verify(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
		}
		if !ok || principal.WorkspaceID == uuid.Nil || principal.UserID == uuid.Nil || expiresAt.IsZero() {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{
			Scopes:     scopes,
			Expiration: expiresAt,
			UserID:     principal.UserID.String(),
			Extra: map[string]any{
				"workspace_id": principal.WorkspaceID.String(),
				"user_id":      principal.UserID.String(),
				"client_id":    clientID,
			},
		}, nil
	}, &mcpauth.RequireBearerTokenOptions{ResourceMetadataURL: resourceURL})(stream)
	return &Handler{stream: protected, metadata: metadata}
}

// StreamableHTTP serves /v1/mcp.
func (h *Handler) StreamableHTTP() http.Handler { return h.stream }

// Metadata serves RFC 9728 protected-resource metadata.
func (h *Handler) Metadata() http.Handler { return h.metadata }

func oauthMetadata(resourceURL, issuerURL string) (out oauthex.ProtectedResourceMetadata) {
	// This local shape is converted through the SDK handler; keeping the list
	// explicit prevents accidental exposure of non-grantable scopes.
	out.Resource = resourceURL
	out.AuthorizationServers = []string{issuerURL}
	out.ScopesSupported = append([]string(nil), auth.OAuthGrantableScopes...)
	out.BearerMethodsSupported = []string{"header"}
	out.ResourceName = "Inroad agent tools"
	return out
}

func principalFromTokenInfo(info *mcpauth.TokenInfo) (agenttool.Principal, bool) {
	if info == nil || info.Extra == nil {
		return agenttool.Principal{}, false
	}
	workspace, wok := info.Extra["workspace_id"].(string)
	user, uok := info.Extra["user_id"].(string)
	if !wok || !uok {
		return agenttool.Principal{}, false
	}
	workspaceID, err := uuid.Parse(workspace)
	if err != nil {
		return agenttool.Principal{}, false
	}
	userID, err := uuid.Parse(user)
	if err != nil {
		return agenttool.Principal{}, false
	}
	clientID, _ := info.Extra["client_id"].(string)
	return agenttool.Principal{WorkspaceID: workspaceID, UserID: userID, Role: "member", AgentClientID: clientID}, true
}

func serverFor(reg agenttool.Registry, principal agenttool.Principal, scopes []string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "inroad", Title: "Inroad Agent Tools", Version: "1.0.0"}, &mcp.ServerOptions{})
	defs := reg.Definitions(principal)
	for _, tool := range defs {
		allowed := allowedTool(tool, scopes)
		if !allowed {
			continue
		}
		var schema any
		if len(tool.InputSchema) > 0 {
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				continue
			}
		}
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		readOnly := tool.Risk == agenttool.RiskRead
		destructive := tool.Risk >= agenttool.RiskConsequential
		openWorld := false
		definition := &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: readOnly, OpenWorldHint: &openWorld,
		}}
		name := tool.Name
		mcp.AddTool[map[string]any, map[string]any](server, definition, func(callCtx context.Context, _ *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			args, err := json.Marshal(input)
			if err != nil {
				return nil, nil, err
			}
			result, err := reg.Execute(callCtx, principal, name, args)
			if err != nil {
				return nil, nil, err
			}
			if !result.Success {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: result.Error}},
				}, nil, nil
			}
			payload, err := json.Marshal(result)
			if err != nil {
				return nil, nil, err
			}
			var out map[string]any
			if err := json.Unmarshal(payload, &out); err != nil {
				return nil, nil, err
			}
			return nil, out, nil
		})
	}
	return server
}

func allowedTool(tool agenttool.Tool, scopes []string) bool {
	required := requiredScope(tool.Name, tool.Risk)
	if required == "" {
		return false
	}
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func requiredScope(name string, risk agenttool.Risk) string {
	domain := ""
	switch {
	case strings.HasPrefix(name, "inroad_crm_"), strings.HasPrefix(name, "inroad_company_"), strings.HasPrefix(name, "inroad_deal_"), strings.HasPrefix(name, "inroad_pipeline_"), strings.HasPrefix(name, "inroad_note_"), strings.HasPrefix(name, "inroad_task_"), strings.HasPrefix(name, "inroad_events_"), strings.HasPrefix(name, "inroad_thread_"):
		domain = "crm"
	case strings.HasPrefix(name, "inroad_contact"), strings.HasPrefix(name, "inroad_search"):
		domain = "contacts"
	case strings.HasPrefix(name, "inroad_list"):
		domain = "lists"
	case strings.HasPrefix(name, "inroad_mailbox"):
		domain = "mailboxes"
	case strings.HasPrefix(name, "inroad_campaign"):
		domain = "campaigns"
	case strings.HasPrefix(name, "inroad_deliverability"):
		domain = "campaigns"
	case strings.HasPrefix(name, "inroad_warmup"):
		domain = "mailboxes"
	default:
		return ""
	}
	if risk == agenttool.RiskRead {
		return domain + ":read"
	}
	return domain + ":write"
}
