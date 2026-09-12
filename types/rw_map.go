package types

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

type RWMap[K comparable, V any] struct {
	data  map[K]V
	mutex sync.RWMutex
}

func (m *RWMap[K, V]) UnmarshalJSON(b []byte) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[K]V)
	return common.Unmarshal(b, &m.data)
}

func (m *RWMap[K, V]) MarshalJSON() ([]byte, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return common.Marshal(m.data)
}

func NewRWMap[K comparable, V any]() *RWMap[K, V] {
	return &RWMap[K, V]{
		data: make(map[K]V),
	}
}

func (m *RWMap[K, V]) Get(key K) (V, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	value, exists := m.data[key]
	return value, exists
}

func (m *RWMap[K, V]) Set(key K, value V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data[key] = value
}

func (m *RWMap[K, V]) AddAll(other map[K]V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for k, v := range other {
		m.data[k] = v
	}
}

// ReplaceAll swaps the entire map content in a single lock window. The
// replacement is copied first so callers cannot mutate internal state, and a
// failed update path can simply never call this method, leaving the previous
// map untouched for concurrent readers.
func (m *RWMap[K, V]) ReplaceAll(other map[K]V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	replacement := make(map[K]V, len(other))
	for k, v := range other {
		replacement[k] = v
	}
	m.data = replacement
}

func (m *RWMap[K, V]) Clear() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[K]V)
}

func (m *RWMap[K, V]) ReadAll() map[K]V {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	copiedMap := make(map[K]V)
	for k, v := range m.data {
		copiedMap[k] = v
	}
	return copiedMap
}

func (m *RWMap[K, V]) Len() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return len(m.data)
}

func LoadFromJsonString[K comparable, V any](m *RWMap[K, V], jsonStr string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[K]V)
	return common.Unmarshal([]byte(jsonStr), &m.data)
}

func LoadFromJsonStringWithCallback[K comparable, V any](m *RWMap[K, V], jsonStr string, onSuccess func()) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[K]V)
	err := common.Unmarshal([]byte(jsonStr), &m.data)
	if err == nil && onSuccess != nil {
		onSuccess()
	}
	return err
}

func (m *RWMap[K, V]) MarshalJSONString() string {
	bytes, err := m.MarshalJSON()
	if err != nil {
		return "{}"
	}
	return string(bytes)
}
