package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func prepareGroupProbeRequest(request dto.Request) error {
	prompt := "Reply only with p" + strconv.FormatInt(time.Now().UnixNano(), 36) + common.GetUUID()[:6]
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		req.Messages = []dto.Message{{Role: "user", Content: prompt}}
		if req.MaxCompletionTokens != nil {
			req.MaxCompletionTokens = common.GetPointer(uint(64))
			req.MaxTokens = nil
		} else {
			req.MaxTokens = common.GetPointer(uint(64))
		}
	case *dto.OpenAIResponsesRequest:
		body, err := common.Marshal([]map[string]string{{"role": "user", "content": prompt}})
		if err != nil {
			return err
		}
		req.Input = json.RawMessage(body)
		req.MaxOutputTokens = common.GetPointer(uint(64))
	case *dto.ClaudeRequest:
		req.Messages = []dto.ClaudeMessage{{Role: "user", Content: prompt}}
		req.MaxTokens = common.GetPointer(uint(64))
	case *dto.GeminiChatRequest:
		req.Contents = []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: prompt}}}}
		req.GenerationConfig.MaxOutputTokens = common.GetPointer(uint(64))
	default:
		return errors.New("group scheduling probes require a streaming text model")
	}
	return nil
}

func GetGroupSchedulingStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// Probe digests include credentials, but the API exposes only health data.
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	settings := channel.GetOtherSettings().GroupScheduling
	states := make([]model.GroupProbeState, 0)
	thresholds := map[string]int64{}
	if settings != nil {
		for _, group := range settings.Groups {
			thresholds[group] = operation_setting.GroupSchedulingTolerance(group)
		}
		for _, name := range settings.ProbeModels {
			state := model.GroupProbeStates([]*model.Channel{channel}, name)[id]
			state.Model = name
			state.Healthy = model.GroupProbeFresh(state, time.Now())
			states = append(states, state)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"probes": states, "thresholds": thresholds}})
}

// RunGroupSchedulingProbes is independent of the 15-second system task poller:
// fast recovery needs sub-second deadlines and no database task per attempt.
func RunGroupSchedulingProbes(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	slots := make(chan struct{}, 16)
	var workers sync.WaitGroup
	defer workers.Wait()
	var channels []*model.Channel
	var refreshed time.Time
	testUserID := 0
	for {
		refresh := false
		select {
		case <-ctx.Done():
			return
		case <-model.GroupProbeWake:
			refresh = true
		case <-ticker.C:
		}
		if refresh || time.Since(refreshed) >= 5*time.Second {
			var current []*model.Channel
			if err := model.DB.Where("status = ?", common.ChannelStatusEnabled).Find(&current).Error; err == nil {
				channels = nil
				for _, channel := range current {
					settings := channel.GetOtherSettings().GroupScheduling
					if settings != nil && settings.Enabled && len(settings.Groups) > 0 {
						channels = append(channels, channel)
					}
				}
			}
			if len(channels) > 0 {
				testUserID, _ = resolveChannelTestUserID(nil)
			}
			refreshed = time.Now()
		}
		if testUserID == 0 {
			continue
		}
		for _, channel := range channels {
			settings := channel.GetOtherSettings().GroupScheduling
			for _, name := range settings.ProbeModels {
				select {
				case slots <- struct{}{}:
				default:
					continue
				}
				owner, claimed := model.TryGroupProbe(channel, name, time.Now())
				if !claimed {
					<-slots
					continue
				}
				probeUserID := testUserID
				workers.Go(func() {
					success := false
					var ttft time.Duration
					defer func() {
						if recover() != nil {
							common.SysError(fmt.Sprintf("group scheduling probe panicked: channel_id=%d", channel.Id))
						}
						model.CompleteGroupProbe(channel, name, owner, ttft, success, time.Now())
						<-slots
					}()
					probeCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.Timeout())*time.Second)
					defer cancel()
					result := runChannelTest(probeCtx, channel, probeUserID, name, "", true, nil, true)
					success = result.localErr == nil && result.newAPIError == nil
					ttft = result.ttft
				})
			}
		}
	}
}
