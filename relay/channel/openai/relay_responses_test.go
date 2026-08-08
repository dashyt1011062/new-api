package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResponsesStreamTestContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		Body:   io.NopCloser(strings.NewReader(body)),
		Header: make(http.Header),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:       &relaycommon.ChannelMeta{},
		DisablePing:       true,
		IsStream:          true,
		OriginModelName:   "gpt-test",
		UpstreamModelName: "gpt-test",
	}
	return c, recorder, resp, info
}

func TestOaiResponsesStreamHandler_MetadataOnlyIsRetryable(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, types.ErrorCodeEmptyResponse, err.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
}

func TestOaiResponsesStreamHandler_FlushesAfterDeliverableEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `event: response.created`)
	assert.Contains(t, recorder.Body.String(), `event: response.output_text.delta`)
	assert.Contains(t, recorder.Body.String(), `event: response.completed`)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, *usage)
}
