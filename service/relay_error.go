package service

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if c.GetBool("bailian_pilot_no_retry") {
		return PolicyDecision{Action: "stop", Reason: "pilot_attempt_limit", Source: "system"}
	}
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	// A compatible upstream can echo its Authorization value in error fields.
	// Remove the actual request key before logging, persistence and client output.
	value, _ := c.Get(channelRequestLeaseKey)
	if _, managed := value.(channelRequestLease); managed && channelError.UsingKey != "" {
		key := channelError.UsingKey
		err.SetMessage(strings.ReplaceAll(err.Error(), key, "[redacted]"))
		if failure, ok := err.RelayError.(types.OpenAIError); ok {
			failure.Message = strings.ReplaceAll(failure.Message, key, "[redacted]")
			failure.Type = strings.ReplaceAll(failure.Type, key, "[redacted]")
			failure.Param = strings.ReplaceAll(failure.Param, key, "[redacted]")
			if code, ok := failure.Code.(string); ok {
				failure.Code = strings.ReplaceAll(code, key, "[redacted]")
			}
			if code, ok := failure.Code.(types.ErrorCode); ok {
				failure.Code = types.ErrorCode(strings.ReplaceAll(string(code), key, "[redacted]"))
			}
			failure.Metadata = nil
			err.RelayError = failure
			if code := strings.ReplaceAll(string(err.GetErrorCode()), key, "[redacted]"); code != string(err.GetErrorCode()) {
				// The upstream-controlled code is also retained privately by the
				// error value and may enter admin_info independently of its body.
				failure.Code = code
				*err = *types.WithOpenAIError(failure, err.StatusCode)
			}
		}
	}
	upstreamURLs := common.UpstreamAddresses(c)
	logger.LogError(c, common.LogText("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		value, _ := c.Get(channelRequestLeaseKey)
		if state, ok := value.(channelRequestLease); ok {
			id, reference, version, slot := channelError.ChannelId, state.reference, state.lease.Version(), common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
			gopool.Go(func() { _ = model.DisableManagedChannelSlot(id, reference, version, slot) })
		} else {
			reason := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
			gopool.Go(func() { DisableChannel(channelError, reason) })
		}
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(upstreamURLs...), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
