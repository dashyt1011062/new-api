package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func testChannels(ids ...int) []*model.Channel {
	channels := make([]*model.Channel, 0, len(ids))
	for _, id := range ids {
		channels = append(channels, &model.Channel{Id: id, Status: common.ChannelStatusEnabled})
	}
	return channels
}

func TestNextChannelByIDCycleWrapsAfterStickyChannel(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	tried := map[int]struct{}{30: {}}

	next := nextChannelByIDCycle(channels, 30, tried)
	if next == nil || next.Id != 17 {
		t.Fatalf("expected channel 17 after wrapping from 30, got %#v", next)
	}

	tried[17] = struct{}{}
	next = nextChannelByIDCycle(channels, 30, tried)
	if next == nil || next.Id != 20 {
		t.Fatalf("expected channel 20 after excluding 30 and 17, got %#v", next)
	}
}

func TestNextChannelByIDCycleMovesToNextHigherID(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	tried := map[int]struct{}{20: {}}

	next := nextChannelByIDCycle(channels, 20, tried)
	if next == nil || next.Id != 28 {
		t.Fatalf("expected channel 28 after channel 20, got %#v", next)
	}
}

func TestNextChannelByIDCycleNeverRepeatsTriedChannel(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	tried := map[int]struct{}{20: {}, 28: {}}

	next := nextChannelByIDCycle(channels, 20, tried)
	if next == nil || next.Id != 30 {
		t.Fatalf("expected channel 30 after skipping tried channel 28, got %#v", next)
	}
	tried[30] = struct{}{}

	next = nextChannelByIDCycle(channels, 20, tried)
	if next == nil || next.Id != 17 {
		t.Fatalf("expected wrapped channel 17 after skipping tried channels, got %#v", next)
	}
}

func TestNextChannelByIDCycleSkipsDisabledCandidate(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	channels[2].Status = common.ChannelStatusManuallyDisabled
	tried := map[int]struct{}{20: {}}

	next := nextChannelByIDCycle(channels, 20, tried)
	if next == nil || next.Id != 30 {
		t.Fatalf("expected channel 30 after disabled channel 28, got %#v", next)
	}
}

func TestNextChannelByIDCycleStopsAfterAllCandidatesTried(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	tried := map[int]struct{}{17: {}, 20: {}, 28: {}, 30: {}}

	if next := nextChannelByIDCycle(channels, 30, tried); next != nil {
		t.Fatalf("expected no channel after all candidates were tried, got channel %d", next.Id)
	}
}

func TestNextChannelByIDCycleStartsAfterMissingChannelID(t *testing.T) {
	channels := testChannels(17, 20, 28, 30)
	tried := map[int]struct{}{25: {}}

	next := nextChannelByIDCycle(channels, 25, tried)
	if next == nil || next.Id != 28 {
		t.Fatalf("expected channel 28 after missing start channel 25, got %#v", next)
	}
}

func TestRetryParamChannelIDCycleIgnoresGlobalRetryLimit(t *testing.T) {
	retry := 99
	param := &RetryParam{
		Retry:               &retry,
		channelIDCycle:      true,
		cycleStartChannelID: 30,
		cycleChannels:       testChannels(17, 20, 28, 30),
		triedChannelIDs:     map[int]struct{}{30: {}},
	}

	if !param.ShouldAttempt(0) {
		t.Fatal("expected channel cycle to continue even when global retry limit is zero")
	}
	for _, want := range []int{17, 20, 28} {
		next := param.nextChannelInIDCycle()
		if next == nil || next.Id != want {
			t.Fatalf("expected channel %d, got %#v", want, next)
		}
		param.MarkChannelTried(next.Id)
	}
	if param.ShouldAttempt(0) {
		t.Fatal("expected channel cycle to stop after one complete pass")
	}
}
