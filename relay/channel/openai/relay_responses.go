package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const maxBufferedResponsesStreamEvents = 32

type bufferedResponsesStreamEvent struct {
	response dto.ResponsesStreamResponse
	data     string
}

func responsesStreamEventStartsDelivery(streamResponse dto.ResponsesStreamResponse) bool {
	if strings.HasSuffix(streamResponse.Type, ".delta") && strings.TrimSpace(streamResponse.Delta) != "" {
		return true
	}

	if streamResponse.Type != "response.completed" && streamResponse.Type != "response.done" {
		return false
	}
	if streamResponse.Response == nil {
		return false
	}
	return service.ValidUsage(streamResponse.Response.Usage) || len(streamResponse.Response.Output) > 0
}

func copyResponsesStreamUsage(response *dto.OpenAIResponsesResponse, usage *dto.Usage, c *gin.Context) {
	if response == nil {
		return
	}
	if response.Usage != nil {
		if response.Usage.InputTokens != 0 {
			usage.PromptTokens = response.Usage.InputTokens
		}
		if response.Usage.OutputTokens != 0 {
			usage.CompletionTokens = response.Usage.OutputTokens
		}
		if response.Usage.TotalTokens != 0 {
			usage.TotalTokens = response.Usage.TotalTokens
		}
		if response.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = response.Usage.InputTokensDetails.CachedTokens
			usage.PromptTokensDetails.CacheWriteTokens = response.Usage.InputTokensDetails.CacheWriteTokens
		}
	}
	if response.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", response.GetQuality())
		c.Set("image_generation_call_size", response.GetSize())
	}
}

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	if responsesResponse.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", responsesResponse.GetQuality())
		c.Set("image_generation_call_size", responsesResponse.GetSize())
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
			usage.PromptTokensDetails.CacheWriteTokens = responsesResponse.Usage.InputTokensDetails.CacheWriteTokens
		}
	}
	if info == nil || info.ResponsesUsageInfo == nil || info.ResponsesUsageInfo.BuiltInTools == nil {
		return &usage, nil
	}
	// 解析 Tools 用量
	for _, tool := range responsesResponse.Tools {
		buildToolinfo, ok := info.ResponsesUsageInfo.BuiltInTools[common.Interface2String(tool["type"])]
		if !ok || buildToolinfo == nil {
			logger.LogError(c, fmt.Sprintf("BuiltInTools not found for tool type: %v", tool["type"]))
			continue
		}
		buildToolinfo.CallCount++
	}
	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder
	// Do not commit metadata-only events to the client before the upstream proves it can deliver a response.
	bufferedEvents := make([]bufferedResponsesStreamEvent, 0, 4)
	deliveryStarted := false
	var responseErr *types.NewAPIError

	emit := func(event bufferedResponsesStreamEvent) error {
		return helper.ResponseChunkData(c, event.response, event.data)
	}
	flushBufferedEvents := func() error {
		for _, event := range bufferedEvents {
			if err := emit(event); err != nil {
				return err
			}
		}
		bufferedEvents = nil
		deliveryStarted = true
		return nil
	}

	streamErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			if !deliveryStarted {
				responseErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
				sr.Stop(responseErr)
			} else {
				sr.Error(err)
			}
			return
		}

		if streamResponse.Type == "response.error" || streamResponse.Type == "response.failed" || streamResponse.Type == "response.incomplete" {
			if !deliveryStarted {
				responseErr = types.NewOpenAIError(
					fmt.Errorf("upstream responses stream ended with %s", streamResponse.Type),
					types.ErrorCodeBadResponse,
					http.StatusBadGateway,
				)
				sr.Stop(responseErr)
			}
			return
		}

		switch streamResponse.Type {
		case "response.completed", "response.done":
			copyResponsesStreamUsage(streamResponse.Response, usage, c)
		case "response.output_text.delta":
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			// 函数调用处理
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
						if webSearchTool, exists := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]; exists && webSearchTool != nil {
							webSearchTool.CallCount++
						}
					}
				}
			}
		}

		event := bufferedResponsesStreamEvent{response: streamResponse, data: data}
		if deliveryStarted {
			if err := emit(event); err != nil {
				sr.Error(err)
			}
			return
		}

		if len(bufferedEvents) >= maxBufferedResponsesStreamEvents {
			responseErr = types.NewOpenAIError(
				fmt.Errorf("upstream responses stream did not produce a deliverable event"),
				types.ErrorCodeEmptyResponse,
				http.StatusBadGateway,
			)
			sr.Stop(responseErr)
			return
		}
		bufferedEvents = append(bufferedEvents, event)
		if responsesStreamEventStartsDelivery(streamResponse) {
			if err := flushBufferedEvents(); err != nil {
				sr.Error(err)
			}
		}
	})
	if streamErr != nil {
		return nil, types.NewOpenAIError(streamErr, types.ErrorCodeEmptyResponse, http.StatusBadGateway)
	}
	if responseErr != nil {
		return nil, responseErr
	}
	if !deliveryStarted {
		return nil, types.NewOpenAIError(
			fmt.Errorf("upstream responses stream ended without a deliverable event"),
			types.ErrorCodeEmptyResponse,
			http.StatusBadGateway,
		)
	}

	if usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}
