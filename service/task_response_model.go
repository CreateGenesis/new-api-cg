package service

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TaskResponseModelInfo uses current channel settings without changing the
// persisted task identity or the provider payload used for settlement.
func TaskResponseModelInfo(task *model.Task) *relaycommon.RelayInfo {
	if task == nil || model.DB == nil {
		return nil
	}
	requested := task.PrivateData.RequestedModelName
	if requested == "" {
		requested = task.Properties.OriginModelName
	}
	if requested == "" {
		return nil
	}
	channel, err := model.GetChannelById(task.ChannelId, false)
	if err != nil {
		return nil
	}
	var settings dto.ChannelOtherSettings
	if channel.OtherSettings == "" || common.UnmarshalJsonStr(channel.OtherSettings, &settings) != nil {
		return nil
	}
	return &relaycommon.RelayInfo{
		RequestedModelName: requested,
		RelayFormat:        types.RelayFormatTask,
		ChannelMeta:        &relaycommon.ChannelMeta{ChannelOtherSettings: settings},
	}
}

// RewriteTaskResponseModels handles native task query envelopes, including
// mixed-channel lists. Unknown envelopes without a task identity stay untouched.
func RewriteTaskResponseModels(value any, tasks []*model.Task) ([]byte, error) {
	data, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	infos := make(map[string]*relaycommon.RelayInfo, len(tasks))
	for _, task := range tasks {
		info := TaskResponseModelInfo(task)
		if info.ResponseModelName() != "" {
			infos[task.TaskID] = info
		}
	}
	if len(infos) == 0 {
		return data, nil
	}
	// A single-task endpoint also permits protocol envelopes without an ID.
	if len(tasks) == 1 {
		data = infos[tasks[0].TaskID].RewriteResponseModelJSON(data)
	}
	// Inspect only known collection envelopes. Collection entries must identify
	// their task; never infer the channel from array order or an upstream model.
	for _, path := range []string{"", "data", "tasks", "items"} {
		value := gjson.ParseBytes(data)
		if path != "" {
			value = value.Get(path)
		}
		entries := []gjson.Result{value}
		if value.IsArray() {
			entries = value.Array()
		}
		for index, entry := range entries {
			if !entry.IsObject() {
				continue
			}
			var info *relaycommon.RelayInfo
			for _, key := range []string{"id", "task_id"} {
				if candidate := infos[entry.Get(key).String()]; candidate != nil {
					info = candidate
					break
				}
			}
			if info == nil {
				continue
			}
			patched := info.RewriteResponseModelJSON([]byte(entry.Raw))
			entryPath := path
			if value.IsArray() {
				if entryPath != "" {
					entryPath += "."
				}
				entryPath += strconv.Itoa(index)
			}
			if entryPath == "" {
				data = patched
				continue
			}
			updated, err := sjson.SetRawBytes(data, entryPath, patched)
			if err != nil {
				return nil, err
			}
			data = updated
		}
	}
	return data, nil
}
