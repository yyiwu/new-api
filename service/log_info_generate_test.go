package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTextOtherInfoSeparatesRequestedValuesFromAdminOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(ctx, constant.ContextKeySystemPromptOverride, true)
	start := time.Unix(1, 0)
	info := &relaycommon.RelayInfo{
		OriginModelName:          "requested-model",
		RequestedReasoningEffort: "low",
		ReasoningEffort:          "high",
		StartTime:                start,
		FirstResponseTime:        start.Add(time.Second),
		ParamOverrideAudit:       []string{"set reasoning.effort = high"},
		ChannelMeta: &relaycommon.ChannelMeta{
			IsModelMapped:     true,
			UpstreamModelName: "routed-secret-model",
		},
	}

	other := GenerateTextOtherInfo(ctx, info, 1, 1, 1, 0, 0, 0, 1).Snapshot()

	assert.Equal(t, "low", other[model.LogOtherRequestedReasoningEffortKey])
	for _, key := range []string{"is_model_mapped", "upstream_model_name", "is_system_prompt_overwritten", "po"} {
		assert.NotContains(t, other, key)
	}
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "high", adminInfo["effective_reasoning_effort"])
	assert.NotContains(t, adminInfo, "upstream_model_name")
	assert.Equal(t, true, adminInfo["is_system_prompt_overwritten"])
	rootInfo, ok := other["root_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, rootInfo["is_model_mapped"])
	assert.Equal(t, "routed-secret-model", rootInfo["upstream_model_name"])
	assert.Equal(t, []string{"set reasoning.effort = high"}, rootInfo["po"])
}
