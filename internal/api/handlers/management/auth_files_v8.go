package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// StartOAuthV8 dispatches login using the provider query parameter.
func (h *Handler) StartOAuthV8(c *gin.Context) {
	switch strings.ToLower(strings.TrimSpace(c.Query("provider"))) {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "antigravity":
		h.RequestAntigravityToken(c)
	case "claude", "anthropic":
		h.RequestAnthropicToken(c)
	case "codex":
		h.RequestCodexToken(c)
	case "kimi":
		h.RequestKimiToken(c)
	case "xai":
		h.RequestXAIToken(c)
	case "cursor":
		h.RequestCursorToken(c)
	case "github":
		h.RequestGitHubToken(c)
	case "gitlab":
		h.RequestGitLabToken(c)
	case "qoder":
		h.RequestQoderToken(c)
	case "kilo":
		h.RequestKiloToken(c)
	case "kiro":
		h.RequestKiroToken(c)
	case "iflow":
		h.RequestIFlowToken(c)
	default:
		if !h.ServePluginAuthURL(c) {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
		}
	}
}

// ImportOAuthV8 dispatches credential import using the provider query parameter.
func (h *Handler) ImportOAuthV8(c *gin.Context) {
	switch strings.ToLower(strings.TrimSpace(c.Query("provider"))) {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "vertex":
		h.ImportVertexCredential(c)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
	}
}
