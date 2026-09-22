package catalog

import (
	"sync"
	"time"
)

// entityState 是目录内部保存的实体状态（骨架阶段仅占位）。
type entityState struct {
	current  Entity
	versions []EntityVersion
}

// Catalog 是本地内存元数据目录。
// 一把 RWMutex 串行化所有写操作并为读操作提供一致性快照，
// 保证并发登记/更新过程中不会读到中间状态或丢失更新。
type Catalog struct {
	mu       sync.RWMutex
	entities map[string]*entityState     // key: entity name
	outEdges map[string]map[string]*Edge // key: upstream name -> downstream -> edge
	inEdges  map[string]map[string]*Edge // key: downstream name -> upstream -> edge
	now      func() time.Time
}

// New 创建一个空目录。
func New() *Catalog {
	return &Catalog{
		entities: map[string]*entityState{},
		outEdges: map[string]map[string]*Edge{},
		inEdges:  map[string]map[string]*Edge{},
		now:      time.Now,
	}
}
