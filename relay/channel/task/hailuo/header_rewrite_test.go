package hailuo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTaskAppliesHeaderRewriteToFileLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "rewritten", r.Header.Get("X-Rewrite-Test"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == QueryTaskEndpoint {
			_, _ = w.Write([]byte(`{"task_id":"upstream-task","status":"Success","file_id":"file-1","base_resp":{"status_code":0}}`))
			return
		}
		assert.Equal(t, "/v1/files/retrieve", r.URL.Path)
		_, _ = w.Write([]byte(`{"file":{"download_url":"https://cdn.example/video.mp4"},"base_resp":{"status_code":0}}`))
	}))
	t.Cleanup(server.Close)

	adaptor := &TaskAdaptor{}
	resp, err := adaptor.FetchTask(server.URL, "test-key", map[string]any{
		"task_id": "upstream-task",
	}, "", false, relaycommon.HeaderRewriteInput{
		ChannelSetting: dto.ChannelSettings{
			HeaderRewrite: &types.ChannelHeaderRewriteSettings{
				HeaderRewriteRule: types.HeaderRewriteRule{
					Set: map[string]string{"X-Rewrite-Test": "rewritten"},
				},
			},
		},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	result, err := adaptor.ParseTaskResult(body)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example/video.mp4", result.Url)
}
