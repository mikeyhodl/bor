package heimdallapp

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"

	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

func (h *HeimdallAppClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	totalRecords := make([]*clerk.EventRecordWithTime, 0)

	for {
		fromRecord, err := h.hApp.ClerkKeeper.GetEventRecord(h.NewContext(), fromID)
		if err != nil {
			return nil, err
		}

		events, err := h.hApp.ClerkKeeper.GetEventRecordListWithTime(h.NewContext(), fromRecord.RecordTime, time.Unix(to, 0), 1, stateFetchLimit)
		if err != nil {
			return nil, err
		}

		totalRecords = append(totalRecords, toEvents(events)...)

		if len(events) < stateFetchLimit {
			break
		}

		fromID += uint64(stateFetchLimit)
	}

	return totalRecords, nil
}

func toEvents(hdEvents []types.EventRecord) []*clerk.EventRecordWithTime {
	events := make([]*clerk.EventRecordWithTime, len(hdEvents))

	for i, ev := range hdEvents {
		events[i] = toEvent(ev)
	}

	return events
}

func toEvent(hdEvent types.EventRecord) *clerk.EventRecordWithTime {
	return &clerk.EventRecordWithTime{
		EventRecord: clerk.EventRecord{
			ID:       hdEvent.Id,
			Contract: common.HexToAddress(hdEvent.Contract),
			Data:     hdEvent.Data,
			TxHash:   common.HexToHash(hdEvent.TxHash),
			LogIndex: hdEvent.LogIndex,
			ChainID:  hdEvent.BorChainId,
		},
		Time: hdEvent.RecordTime,
	}
}
