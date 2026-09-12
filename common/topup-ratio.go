package common

import (
	"fmt"
	"sync"
)

var topupGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}
var topupGroupRatioMutex sync.RWMutex

func TopupGroupRatio2JSONString() string {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	jsonBytes, err := Marshal(topupGroupRatio)
	if err != nil {
		SysError("error marshalling topup group ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateTopupGroupRatioByJSONString(jsonStr string) error {
	parsed, err := parseTopupGroupRatio(jsonStr)
	if err != nil {
		return err
	}
	topupGroupRatioMutex.Lock()
	topupGroupRatio = parsed
	topupGroupRatioMutex.Unlock()
	return nil
}

func CheckTopupGroupRatio(jsonStr string) error {
	_, err := parseTopupGroupRatio(jsonStr)
	return err
}

func parseTopupGroupRatio(jsonStr string) (map[string]float64, error) {
	parsed := make(map[string]float64)
	if err := UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return nil, err
	}
	for name, ratio := range parsed {
		if ratio < 0 {
			return nil, fmt.Errorf("top-up group ratio must be not less than 0: %s", name)
		}
	}
	return parsed, nil
}

func GetTopupGroupRatio(name string) float64 {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	ratio, ok := topupGroupRatio[name]
	if !ok {
		SysError("topup group ratio not found: " + name)
		return 1
	}
	return ratio
}
