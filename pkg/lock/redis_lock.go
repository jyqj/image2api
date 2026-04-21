package lock

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotAcquired 表示未抢到锁(资源被占用)。
var ErrNotAcquired = errors.New("lock: not acquired")

// RedisLock 是一个简单的 Redis 分布式锁,用于账号池「一号一锁」。
// 通过 SET NX + token 实现原子获取与安全释放。
type RedisLock struct {
	client *redis.Client
}

func NewRedisLock(client *redis.Client) *RedisLock { return &RedisLock{client: client} }

// Acquire 尝试抢锁。成功返回 token(释放时必须提供);失败返回 ErrNotAcquired。
func (l *RedisLock) Acquire(ctx context.Context, key, token string, ttl time.Duration) error {
	ok, err := l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotAcquired
	}
	return nil
}

// 释放锁使用 Lua 保证 CAS 语义(只有持有者才能删)。
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`)

// Release 释放锁。若当前持有者不是 token,不会误删。
func (l *RedisLock) Release(ctx context.Context, key, token string) error {
	_, err := releaseScript.Run(ctx, l.client, []string{key}, token).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	return nil
}

// Refresh 续期(仅当前持有者才能续)。
var refreshScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`)

func (l *RedisLock) Refresh(ctx context.Context, key, token string, ttl time.Duration) error {
	_, err := refreshScript.Run(ctx, l.client, []string{key}, token, ttl.Milliseconds()).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	return nil
}
