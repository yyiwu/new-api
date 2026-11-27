package common

import (
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type UsageReceipt struct {
	Quota   int      `json:"quota"`
	CostUSD *float64 `json:"cost_usd"`
	LogID   *string  `json:"log_id"`
}

func NewUsageReceipt(quota int, logID string) *UsageReceipt {
	if quota < 0 {
		return nil
	}
	receipt := &UsageReceipt{Quota: quota}
	if units := common.QuotaPerUnit; units > 0 && !math.IsInf(units, 0) {
		if cost := float64(quota) / units; !math.IsInf(cost, 0) {
			receipt.CostUSD = &cost
		}
	}
	if logID != "" {
		receipt.LogID = &logID
	}
	return receipt
}

func IsResponsesTerminalEvent(event string) bool {
	switch event {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func ApplyUsageReceipt(body []byte, path string, receipt *UsageReceipt) ([]byte, error) {
	usage := gjson.GetBytes(body, path)
	if receipt == nil && !usage.Get("quota").Exists() && !usage.Get("cost_usd").Exists() && !usage.Get("log_id").Exists() {
		return body, nil
	}
	fields := make(map[string]common.RawMessage)
	if usage.IsObject() {
		if err := common.Unmarshal([]byte(usage.Raw), &fields); err != nil {
			return nil, err
		}
	} else if usage.Exists() && usage.Type != gjson.Null {
		return body, nil
	}
	if receipt == nil {
		delete(fields, "quota")
		delete(fields, "cost_usd")
		delete(fields, "log_id")
	} else {
		charge, err := common.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		if err := common.Unmarshal(charge, &fields); err != nil {
			return nil, err
		}
	}
	encoded, err := common.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, path, encoded)
}
