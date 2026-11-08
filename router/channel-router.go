package router

import (
	"bytes"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

type permissionRoute struct {
	method     string
	path       string
	permission authz.Permission
	handler    gin.HandlerFunc
}

// handlePermissionRoute registers a Casbin-guarded route and declares the same
// permission as the access token scope that route requires.
func handlePermissionRoute(group *gin.RouterGroup, method, relativePath string, permission authz.Permission, handlers ...gin.HandlerFunc) {
	middleware.DeclareAccessTokenPermissionRoute(method, joinPaths(group.BasePath(), relativePath), permission)
	group.Handle(method, relativePath, append([]gin.HandlerFunc{middleware.RequirePermission(permission), rejectUnsupportedManagedChannelOperation}, handlers...)...)
}

// This guard follows authentication AND permission checks. Legacy operations
// must not silently succeed with an empty key or duplicate a credential ref.
// Batch operations without a proven per-channel implementation are rejected
// as a whole when they could touch a managed channel.
func rejectUnsupportedManagedChannelOperation(c *gin.Context) {
	full := c.FullPath()
	if !strings.HasPrefix(full, "/api/channel/") {
		c.Next()
		return
	}
	route := strings.TrimPrefix(full, "/api/channel")
	if strings.HasPrefix(route, "/credentials") {
		c.Next()
		return
	}
	if c.Request.Method == http.MethodGet {
		switch route {
		case "/", "/search", "/models", "/models_enabled", "/default_base_urls", "/ops", "/priority/order", "/:id":
			c.Next()
			return
		}
	}
	if (route == "/" && c.Request.Method == http.MethodPost) || route == "/priority/reorder" {
		c.Next()
		return
	}
	query := model.DB.Model(&model.Channel{}).Where("credential_ref IS NOT NULL AND credential_ref <> ?", "")
	if id := c.Param("id"); id != "" {
		n, err := strconv.Atoi(id)
		if err != nil || n <= 0 {
			c.Next()
			return
		}
		query = query.Where("id = ?", n)
	} else if route == "/" && c.Request.Method == http.MethodPut {
		var input struct {
			ID int `json:"id"`
		}
		if c.ShouldBindBodyWith(&input, binding.JSON) != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
			return
		}
		if body, ok := c.Get(gin.BodyBytesKey); ok {
			if data, valid := body.([]byte); valid {
				c.Request.Body = io.NopCloser(bytes.NewReader(data))
			}
		}
		query = query.Where("id = ?", input.ID)
	}
	var count int64
	if query.Count(&count).Error != nil || count > 0 {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "MANAGED_CREDENTIAL_OPERATION_UNSUPPORTED", "message": common.NewMessage("Invalid request").Error()})
		return
	}
	c.Next()
}

// joinPaths matches gin's route path joining, which keeps a trailing slash
// from the relative path, so declared keys equal c.FullPath().
func joinPaths(absolutePath, relativePath string) string {
	if relativePath == "" {
		return absolutePath
	}
	finalPath := path.Join(absolutePath, relativePath)
	if relativePath[len(relativePath)-1] == '/' && finalPath[len(finalPath)-1] != '/' {
		return finalPath + "/"
	}
	return finalPath
}

func registerChannelRoutes(apiRouter *gin.RouterGroup) {
	channelRoute := apiRouter.Group("/channel")
	channelRoute.Use(middleware.AdminAuth())

	channelRoute.POST("/:id/key",
		middleware.RootAuth(),
		middleware.CriticalRateLimit(),
		middleware.DisableCache(),
		middleware.SecureVerificationRequired(),
		rejectUnsupportedManagedChannelOperation,
		controller.GetChannelKey,
	)

	for _, route := range channelPermissionRoutes {
		handlePermissionRoute(channelRoute, route.method, route.path, route.permission, route.handler)
	}
}

var channelPermissionRoutes = []permissionRoute{
	{method: http.MethodPost, path: "/credentials/:id/rewrap", permission: authz.ChannelSensitiveWrite, handler: controller.RewrapSupplierCredential},
	{method: http.MethodPost, path: "/credentials", permission: authz.ChannelSensitiveWrite, handler: controller.CreateSupplierCredential},
	{method: http.MethodGet, path: "/credentials/:id", permission: authz.ChannelRead, handler: controller.GetSupplierCredential},
	{method: http.MethodPut, path: "/credentials/:id", permission: authz.ChannelSensitiveWrite, handler: controller.ReplaceSupplierCredential},
	{method: http.MethodPost, path: "/credentials/:id/status", permission: authz.ChannelSensitiveWrite, handler: controller.SetSupplierCredentialStatus},
	{method: http.MethodPost, path: "/credentials/:id/test", permission: authz.ChannelOperate, handler: controller.TestSupplierCredential},
	{method: http.MethodGet, path: "/priority/order", permission: authz.ChannelRead, handler: controller.GetChannelPriorityOrder},
	{method: http.MethodPost, path: "/priority/reorder", permission: authz.ChannelWrite, handler: controller.ReorderChannelPriority},
	{method: http.MethodGet, path: "/", permission: authz.ChannelRead, handler: controller.GetAllChannels},
	{method: http.MethodGet, path: "/search", permission: authz.ChannelRead, handler: controller.SearchChannels},
	{method: http.MethodGet, path: "/models", permission: authz.ChannelRead, handler: controller.ChannelListModels},
	{method: http.MethodGet, path: "/default_base_urls", permission: authz.ChannelRead, handler: controller.GetChannelDefaultBaseURLs},
	{method: http.MethodGet, path: "/models_enabled", permission: authz.ChannelRead, handler: controller.EnabledListModels},
	{method: http.MethodGet, path: "/ops", permission: authz.ChannelRead, handler: controller.GetChannelOps},
	{method: http.MethodGet, path: "/:id", permission: authz.ChannelRead, handler: controller.GetChannel},
	{method: http.MethodGet, path: "/test", permission: authz.ChannelOperate, handler: controller.TestAllChannels},
	{method: http.MethodGet, path: "/test/:id", permission: authz.ChannelOperate, handler: controller.TestChannel},
	{method: http.MethodGet, path: "/update_balance", permission: authz.ChannelOperate, handler: controller.UpdateAllChannelsBalance},
	{method: http.MethodGet, path: "/update_balance/:id", permission: authz.ChannelOperate, handler: controller.UpdateChannelBalance},
	{method: http.MethodPost, path: "/", permission: authz.ChannelSensitiveWrite, handler: controller.AddChannel},
	{method: http.MethodPut, path: "/", permission: authz.ChannelWrite, handler: controller.UpdateChannel},
	{method: http.MethodPost, path: "/status/batch", permission: authz.ChannelOperate, handler: controller.BatchUpdateChannelStatus},
	{method: http.MethodPost, path: "/:id/status", permission: authz.ChannelOperate, handler: controller.UpdateChannelStatus},
	{method: http.MethodDelete, path: "/disabled", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteDisabledChannel},
	{method: http.MethodPost, path: "/tag/disabled", permission: authz.ChannelOperate, handler: controller.DisableTagChannels},
	{method: http.MethodPost, path: "/tag/enabled", permission: authz.ChannelOperate, handler: controller.EnableTagChannels},
	{method: http.MethodPut, path: "/tag", permission: authz.ChannelWrite, handler: controller.EditTagChannels},
	{method: http.MethodDelete, path: "/:id", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteChannel},
	{method: http.MethodPost, path: "/batch", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteChannelBatch},
	{method: http.MethodPost, path: "/fix", permission: authz.ChannelOperate, handler: controller.FixChannelsAbilities},
	{method: http.MethodGet, path: "/fetch_models/:id", permission: authz.ChannelOperate, handler: controller.FetchUpstreamModels},
	{method: http.MethodGet, path: "/:id/vllm/status", permission: authz.ChannelRead, handler: controller.GetVLLMChannelStatus},
	{method: http.MethodGet, path: "/:id/sglang/status", permission: authz.ChannelRead, handler: controller.GetSGLangChannelStatus},
	{method: http.MethodPost, path: "/fetch_models", permission: authz.ChannelSensitiveWrite, handler: controller.FetchModels},
	{method: http.MethodPost, path: "/:id/codex/refresh", permission: authz.ChannelSensitiveWrite, handler: controller.RefreshCodexChannelCredential},
	{method: http.MethodGet, path: "/:id/codex/usage", permission: authz.ChannelRead, handler: controller.GetCodexChannelUsage},
	{method: http.MethodGet, path: "/:id/codex/usage/reset-credits", permission: authz.ChannelRead, handler: controller.GetCodexChannelRateLimitResetCredits},
	{method: http.MethodPost, path: "/:id/codex/usage/reset", permission: authz.ChannelOperate, handler: controller.ResetCodexChannelUsage},
	{method: http.MethodPost, path: "/ollama/pull", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaPullModel},
	{method: http.MethodPost, path: "/ollama/pull/stream", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaPullModelStream},
	{method: http.MethodDelete, path: "/ollama/delete", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaDeleteModel},
	{method: http.MethodGet, path: "/ollama/version/:id", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaVersion},
	{method: http.MethodPost, path: "/batch/tag", permission: authz.ChannelWrite, handler: controller.BatchSetChannelTag},
	{method: http.MethodGet, path: "/tag/models", permission: authz.ChannelRead, handler: controller.GetTagModels},
	{method: http.MethodPost, path: "/copy/:id", permission: authz.ChannelSensitiveWrite, handler: controller.CopyChannel},
	{method: http.MethodPost, path: "/multi_key/manage", permission: authz.ChannelOperate, handler: controller.ManageMultiKeys},
	{method: http.MethodPost, path: "/upstream_updates/apply", permission: authz.ChannelWrite, handler: controller.ApplyChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/apply_all", permission: authz.ChannelWrite, handler: controller.ApplyAllChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/detect", permission: authz.ChannelOperate, handler: controller.DetectChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/detect_all", permission: authz.ChannelOperate, handler: controller.DetectAllChannelUpstreamModelUpdates},
}
