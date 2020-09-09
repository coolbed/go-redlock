package redlock

import (
	"errors"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/thanhpk/randstr"
)

const (
	// MinLockExpire represents minimum lock expire time(ms).
	MinLockExpire = 300
	// DefaultLockExpire represents default lock expire time(ms).
	DefaultLockExpire = 3000
	// DefaultRetryTimes represents default auto-retry times.
	DefaultRetryTimes = 3
)

var (
	// ErrLockWithoutName is returned when creating a lock with a empty name.
	ErrLockWithoutName = errors.New("empty lock name")
	// ErrLockExpireTooSmall is returned when creating a lock with expire smaller than 300ms.
	ErrLockExpireTooSmall = errors.New("lock expire time too small")
	// ErrNotAcquired is returned when a lock cannot be acquired.
	ErrNotAcquired = errors.New("lock not acquired")
	// ErrLockNotHeld is returned when trying to release an inactive lock.
	ErrLockNotHeld = errors.New("lock not held")
)

// RedisClient is a minimal client interface.
type RedisClient interface {
	SetNX(key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Eval(script string, keys []string, args ...interface{}) *redis.Cmd
	EvalSha(sha1 string, keys []string, args ...interface{}) *redis.Cmd
	ScriptExists(scripts ...string) *redis.BoolSliceCmd
	ScriptLoad(script string) *redis.StringCmd
}

// Config ...
type Config struct {
	Expire          int  // 锁过期时间(毫秒)
	Block           bool // 未获得锁时是否一直阻塞直到最终获取锁
	AutoRetry       bool // 未获得锁时是否自动重试
	Retries         int  // 自动重试次数
	AutoRefresh     bool // 获得锁后是否自动刷新以保持自己能够一直持有锁
	refreshInterval int  // 自动刷新间隔时间为Expire的2/3
}

// RedLock represents a redis lock.
type RedLock struct {
	cli  RedisClient // redis连接
	name string      // 锁的key
	id   string      // 锁的value
	conf Config      // 锁的配置
}

// New ...
func New(cli RedisClient, name, id string, conf Config) (*RedLock, error) {
	// 锁必需设置过期时间
	if conf.Expire <= 0 {
		conf.Expire = DefaultLockExpire
	}
	// 锁过期时间不能设置过小
	if conf.Expire < MinLockExpire {
		return nil, ErrLockExpireTooSmall
	}
	// 对于开启自动重试的锁，获取锁失败时默认重试3次
	if conf.AutoRetry && conf.Retries <= 0 {
		conf.Retries = DefaultRetryTimes
	}
	// 开启自动刷新的锁，会在过期时间还剩1/3时自动延长锁的生命周期
	if conf.AutoRefresh {
		conf.refreshInterval = int(float32(conf.Expire) * 2 / 3)
	}
	// 锁必需显式指定一个名称
	if name == "" {
		return nil, ErrLockWithoutName
	}
	if id == "" {
		id = randstr.String(18)
	}
	lock := &RedLock{
		cli:  cli,
		name: name,
		id:   id,
		conf: conf,
	}
	return lock, nil
}

// Lock ...
func (rd *RedLock) Lock() {

}

// Unlock ...
func (rd *RedLock) Unlock() {

}
