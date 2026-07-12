package model

import (
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// GetEnabledChannelsByGroupModelOrdered returns enabled channels for the exact
// group/model pair in ascending channel-ID order. Candidates are filtered by
// the current request path so Advanced Custom channels are only included when
// one of their routes supports the requested endpoint and model. If the exact
// model has no usable candidate, the normalized matching model is used.
func GetEnabledChannelsByGroupModelOrdered(group string, modelName string, requestPath string) ([]*Channel, error) {
	if group == "" || modelName == "" {
		return nil, nil
	}
	if !common.MemoryCacheEnabled {
		return getEnabledChannelsByGroupModelOrderedDB(group, modelName, requestPath)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	loadIDs := func(candidateModel string) []int {
		ids := group2model2channels[group][candidateModel]
		return filterChannelsByRequestPathAndModel(ids, requestPath, modelName)
	}

	ids := loadIDs(modelName)
	if len(ids) == 0 {
		normalized := ratio_setting.FormatMatchingModelName(modelName)
		if normalized != "" && normalized != modelName {
			ids = loadIDs(normalized)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	orderedIDs := append([]int(nil), ids...)
	sort.Ints(orderedIDs)
	channels := make([]*Channel, 0, len(orderedIDs))
	seen := make(map[int]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		channel, ok := channelsIDM[id]
		if !ok || channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		seen[id] = struct{}{}
		channels = append(channels, channel)
	}
	return channels, nil
}

func getEnabledChannelsByGroupModelOrderedDB(group string, modelName string, requestPath string) ([]*Channel, error) {
	load := func(candidateModel string) ([]*Channel, error) {
		var abilities []Ability
		if err := DB.Model(&Ability{}).
			Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, candidateModel, true).
			Order("channel_id ASC").
			Find(&abilities).Error; err != nil {
			return nil, err
		}
		abilities = filterAbilitiesByRequestPathAndModel(abilities, requestPath, modelName)
		if len(abilities) == 0 {
			return nil, nil
		}

		ids := make([]int, 0, len(abilities))
		seen := make(map[int]struct{}, len(abilities))
		for _, ability := range abilities {
			if _, exists := seen[ability.ChannelId]; exists {
				continue
			}
			seen[ability.ChannelId] = struct{}{}
			ids = append(ids, ability.ChannelId)
		}
		sort.Ints(ids)

		var loaded []*Channel
		if err := DB.Where("id IN ? AND status = ?", ids, common.ChannelStatusEnabled).Find(&loaded).Error; err != nil {
			return nil, err
		}
		byID := make(map[int]*Channel, len(loaded))
		for _, channel := range loaded {
			if channel == nil {
				continue
			}
			if requestPath != "" && channel.Type == constant.ChannelTypeAdvancedCustom {
				config := channel.GetOtherSettings().AdvancedCustom
				if config == nil || !config.SupportsPathForModel(requestPath, modelName) {
					continue
				}
			}
			byID[channel.Id] = channel
		}

		channels := make([]*Channel, 0, len(ids))
		for _, id := range ids {
			if channel := byID[id]; channel != nil {
				channels = append(channels, channel)
			}
		}
		return channels, nil
	}

	channels, err := load(modelName)
	if err != nil || len(channels) > 0 {
		return channels, err
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized == "" || normalized == modelName {
		return nil, nil
	}
	return load(normalized)
}

func IsChannelEnabledForGroupModel(group string, modelName string, channelID int) bool {
	if group == "" || modelName == "" || channelID <= 0 {
		return false
	}
	if !common.MemoryCacheEnabled {
		return isChannelEnabledForGroupModelDB(group, modelName, channelID)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	if group2model2channels == nil {
		return false
	}

	if isChannelIDInList(group2model2channels[group][modelName], channelID) {
		return true
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized != "" && normalized != modelName {
		return isChannelIDInList(group2model2channels[group][normalized], channelID)
	}
	return false
}

func IsChannelEnabledForAnyGroupModel(groups []string, modelName string, channelID int) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if IsChannelEnabledForGroupModel(g, modelName, channelID) {
			return true
		}
	}
	return false
}

func isChannelEnabledForGroupModelDB(group string, modelName string, channelID int) bool {
	var count int64
	err := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, modelName, channelID, true).
		Count(&count).Error
	if err == nil && count > 0 {
		return true
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized == "" || normalized == modelName {
		return false
	}
	count = 0
	err = DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, normalized, channelID, true).
		Count(&count).Error
	return err == nil && count > 0
}

func isChannelIDInList(list []int, channelID int) bool {
	for _, id := range list {
		if id == channelID {
			return true
		}
	}
	return false
}
