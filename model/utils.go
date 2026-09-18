package model

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	BatchUpdateTypeCount // if you add a new type, you need to add a new map and a new lock
)

var batchUpdateStores []map[int]int
var batchUpdateWalletStore map[int]int64
var batchUpdateLocks []sync.Mutex

func init() {
	for range BatchUpdateTypeCount {
		batchUpdateStores = append(batchUpdateStores, make(map[int]int))
		batchUpdateLocks = append(batchUpdateLocks, sync.Mutex{})
	}
	batchUpdateWalletStore = make(map[int]int64)
}

func InitBatchUpdater() {
	gopool.Go(func() {
		for {
			time.Sleep(time.Duration(common.BatchUpdateInterval) * time.Second)
			batchUpdate()
		}
	})
}

func addNewRecord(type_ int, id int, value int) {
	if type_ == BatchUpdateTypeUserQuota {
		addNewWalletRecord(id, int64(value))
		return
	}
	batchUpdateLocks[type_].Lock()
	defer batchUpdateLocks[type_].Unlock()
	old, ok := batchUpdateStores[type_][id]
	if !ok {
		batchUpdateStores[type_][id] = value
		return
	}

	sum := old + value
	if (value > 0 && sum < old) || (value < 0 && sum > old) {
		common.SysError(fmt.Sprintf("batch update overflow: type=%d id=%d old=%d value=%d", type_, id, old, value))
		if value > 0 {
			sum = math.MaxInt
		} else {
			sum = math.MinInt
		}
	}
	batchUpdateStores[type_][id] = sum
}

func addNewWalletRecord(id int, value int64) {
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	defer batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	old, ok := batchUpdateWalletStore[id]
	if !ok {
		batchUpdateWalletStore[id] = value
		return
	}

	sum := old + value
	if (value > 0 && sum < old) || (value < 0 && sum > old) {
		common.SysError(fmt.Sprintf("batch wallet update overflow: id=%d old=%d value=%d", id, old, value))
		if value > 0 {
			sum = math.MaxInt64
		} else {
			sum = math.MinInt64
		}
	}
	batchUpdateWalletStore[id] = sum
}

func batchUpdate() {
	// check if there's any data to update
	hasData := false
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	if len(batchUpdateWalletStore) > 0 {
		hasData = true
	}
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	for i := BatchUpdateTypeTokenQuota; i < BatchUpdateTypeCount && !hasData; i++ {
		batchUpdateLocks[i].Lock()
		if len(batchUpdateStores[i]) > 0 {
			hasData = true
		}
		batchUpdateLocks[i].Unlock()
	}

	if !hasData {
		return
	}

	common.SysLog("batch update started")
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	userQuotaStore := batchUpdateWalletStore
	batchUpdateWalletStore = make(map[int]int64)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()

	stores := make([]map[int]int, BatchUpdateTypeCount)
	for i := BatchUpdateTypeTokenQuota; i < BatchUpdateTypeCount; i++ {
		batchUpdateLocks[i].Lock()
		stores[i] = batchUpdateStores[i]
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}

	for i := BatchUpdateTypeTokenQuota; i < BatchUpdateTypeCount; i++ {
		for key, value := range stores[i] {
			switch i {
			case BatchUpdateTypeTokenQuota:
				err := increaseTokenQuota(key, value)
				if err != nil {
					common.SysLog("failed to batch update token quota: " + err.Error())
				}
			case BatchUpdateTypeChannelUsedQuota:
				updateChannelUsedQuota(key, value)
			}
		}
	}

	usedQuotaStore := stores[BatchUpdateTypeUsedQuota]
	requestCountStore := stores[BatchUpdateTypeRequestCount]

	userIDs := make(map[int]struct{}, len(userQuotaStore)+len(usedQuotaStore)+len(requestCountStore))
	for key := range userQuotaStore {
		userIDs[key] = struct{}{}
	}
	for key := range usedQuotaStore {
		userIDs[key] = struct{}{}
	}
	for key := range requestCountStore {
		userIDs[key] = struct{}{}
	}
	for key := range userIDs {
		updateUserQuotaUsedQuotaAndRequestCount(key, userQuotaStore[key], usedQuotaStore[key], requestCountStore[key])
	}
	common.SysLog("batch update finished")
}

func RecordExist(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func shouldUpdateRedis(fromDB bool, err error) bool {
	return common.RedisEnabled && fromDB && err == nil
}
