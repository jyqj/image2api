package model

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Registry 模型配置的进程内缓存,TTL=30s。
// 热点路径:网关请求都会查模型,绝不能每次打 DB。
type Registry struct {
	dao *DAO

	mu       sync.RWMutex
	bySlug   map[string]*Model
	loadedAt time.Time
	ttl      time.Duration

	refreshing atomic.Bool
}

func NewRegistry(dao *DAO) *Registry {
	return &Registry{dao: dao, bySlug: map[string]*Model{}, ttl: 30 * time.Second}
}

// Preload 启动时预热。
func (r *Registry) Preload(ctx context.Context) error {
	return r.refresh(ctx)
}

// Reload 外部触发强制刷新(管理端写后调用,保证下一次请求立刻看到新数据)。
func (r *Registry) Reload(ctx context.Context) error {
	return r.refresh(ctx)
}

func (r *Registry) refresh(ctx context.Context) error {
	list, err := r.dao.List(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]*Model, len(list))
	for _, v := range list {
		m[v.Slug] = v
	}
	r.mu.Lock()
	r.bySlug = m
	r.loadedAt = time.Now()
	r.mu.Unlock()
	return nil
}

// BySlug 返回 slug 对应模型。命中缓存则 O(1),过期则异步刷新。
func (r *Registry) BySlug(ctx context.Context, slug string) (*Model, error) {
	r.mu.RLock()
	m, ok := r.bySlug[slug]
	expired := time.Since(r.loadedAt) > r.ttl
	r.mu.RUnlock()

	if ok && !expired {
		return m, nil
	}
	if expired && r.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer r.refreshing.Store(false)
			_ = r.refresh(context.Background())
		}()
	}
	if ok {
		return m, nil
	}
	return r.dao.GetBySlug(ctx, slug)
}

// List 返回所有模型(直接查 DB,管理端用)。
func (r *Registry) List(ctx context.Context) ([]*Model, error) {
	return r.dao.List(ctx)
}

// ListEnabled 返回已启用模型(/v1/models 用)。
func (r *Registry) ListEnabled(ctx context.Context) ([]*Model, error) {
	r.mu.RLock()
	expired := time.Since(r.loadedAt) > r.ttl
	cached := make([]*Model, 0, len(r.bySlug))
	for _, v := range r.bySlug {
		if v.Enabled && !v.DeletedAt.Valid {
			cached = append(cached, v)
		}
	}
	r.mu.RUnlock()
	if !expired && len(cached) > 0 {
		return cached, nil
	}
	return r.dao.ListEnabled(ctx)
}
