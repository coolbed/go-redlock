package redlock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/thanhpk/randstr"
)

const (
	// MinLockExpire is the minimum lock expire time.
	MinLockExpire = 1000

	// DefaultLockExpire is the default lock expire time.
	DefaultLockExpire = 3000

	// MaxRefreshRetryTimes is the maximum retry times of
	// auto refresh if the refresh fails continuously.
	MaxRefreshRetryTimes = 100
)

const (
	lockNameKeyFmt   = "__lock:%s__"
	lockSignalKeyFmt = "__signal:%s__"
)

var (
	// ErrLockWithoutName is returned when creating a lock with a empty name.
	ErrLockWithoutName = errors.New("empty lock name")

	// ErrLockExpireTooSmall is returned when creating a lock with expire smaller than 300ms.
	ErrLockExpireTooSmall = errors.New("lock expiration too small")

	// ErrAlreadyAcquired is returned when try to lock an already acquired lock.
	ErrAlreadyAcquired = errors.New("lock already acquired")

	// ErrNotAcquired is returned when a lock cannot be acquired.
	ErrNotAcquired = errors.New("lock not acquired")

	// ErrLockNotHeld is returned when trying to release an unacquired lock.
	ErrLockNotHeld = errors.New("lock not held")
)

var (
	luaRefreshLock = redis.NewScript(`
		if redis.call("get", KEYS[1]) ~= ARGV[2] then
        	return 1
    	else
        	redis.call("pexpire", KEYS[1], ARGV[1])
        	return 0
    	end
	`)
	luaUnlock = redis.NewScript(`
		if redis.call("get", KEYS[1]) ~= ARGV[1] then
        	return 1
    	else
        	redis.call("del", KEYS[2])
			redis.call("lpush", KEYS[2], 1)
			redis.call("expire", KEYS[2], 600)
        	redis.call("del", KEYS[1])
        	return 0
    	end
	`)
)

// RedLock represents a redis lock.
type RedLock struct {
	cli                *redis.Client // redis client
	name               string        // redis key of lock
	holder             string        // lock holder name
	signalName         string        // redis key of lock release signal
	expiration         int           // expiration of lock in milliseconds
	autoRefresh        bool          // automatically refresh the lock or not
	refreshInterval    int           // refresh interval if autoRefresh is enabled
	failedRefreshCount int           // count of continuous failed auto refresh
	stopRefresh        chan struct{} // channel used to notify the background auto-refresh goroutine to stop
}

// New creates and returns a new redis lock.
//
// Note that a distributed lock without expire time is dangerous,
// so expire time is always required. If no expire given, say expire <= 0,
// a default 3 seconds expire time will be used.
//
// Furthermore a very small expire time, such as 5ms, does not make sense at all.
// The lock possibely has already expired after the caller just acquired it
// considering the network communication time between the caller and redis server.
// Always give an expiration larger than 1s.
//
// A lock with autoRefresh enabled will refresh its expiration periodically in a
// background goroutine so that the lock holder can hold the lock all the time
// before releasing it. The refresh interval is always the 2/3 of lock expiration.
func New(cli *redis.Client, name string, expiration int, autoRefresh bool) (*RedLock, error) {
	if name == "" {
		return nil, ErrLockWithoutName
	}
	if expiration <= 0 {
		expiration = DefaultLockExpire
	}
	if expiration < MinLockExpire {
		return nil, ErrLockExpireTooSmall
	}
	lock := &RedLock{
		cli:             cli,
		name:            fmt.Sprintf(lockNameKeyFmt, name),
		signalName:      fmt.Sprintf(lockSignalKeyFmt, name),
		holder:          randstr.String(18),
		expiration:      expiration,
		autoRefresh:     autoRefresh,
		refreshInterval: int(float32(expiration) * 2 / 3),
	}
	return lock, nil
}

// hasAcquired returns if the lock has been acquired by the caller.
func (rd *RedLock) hasAcquired(ctx context.Context) (bool, error) {
	holder, err := rd.cli.Get(ctx, rd.name).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	return holder == rd.holder, nil
}

func (rd *RedLock) refresh() error {
	timeout := time.Duration(rd.refreshInterval) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ret, err := luaRefreshLock.Run(ctx, rd.cli, []string{rd.name}, rd.expiration, rd.holder).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrLockNotHeld
		}
		return err
	}
	if i, ok := ret.(int64); !ok || i != 0 {
		return ErrLockNotHeld
	}
	return nil
}

// autoRefresh runs in background and refresh the lock's expiration automatically.
func (rd *RedLock) startAutoRefresh() {
	rd.stopRefresh = make(chan struct{})
	go func() {
		interval := time.Duration(rd.refreshInterval) * time.Millisecond
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-rd.stopRefresh:
				return
			case <-ticker.C:
				err := rd.refresh()
				if err != nil {
					if errors.Is(err, ErrLockNotHeld) {
						return
					}
					rd.failedRefreshCount++
					if rd.failedRefreshCount > MaxRefreshRetryTimes {
						return
					}
					continue
				}
				rd.failedRefreshCount = 0
			}
		}
	}()
}

// Lock locks rd.
// It returns an error if the lock is not acquired or context timeout is triggered.
func (rd *RedLock) Lock(ctx context.Context, block bool) error {
	yes, err := rd.hasAcquired(ctx)
	if err != nil {
		return err
	}
	if yes {
		return ErrAlreadyAcquired
	}
	yes, err = rd.cli.SetNX(ctx, rd.name, rd.holder,
		time.Duration(rd.expiration)*time.Millisecond).Result()
	if err != nil {
		return err
	}
	if yes {
		if rd.autoRefresh {
			rd.startAutoRefresh()
		}
		return nil
	}
	if !block {
		return ErrNotAcquired
	}

	var leftTime time.Duration
	deadline, ok := ctx.Deadline()
	if ok {
		leftTime = deadline.Sub(time.Now())
	}
	if leftTime <= 0 {
		return ErrNotAcquired
	}

	for {
		_, err := rd.cli.BLPop(ctx, leftTime, rd.signalName).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return ErrNotAcquired
			}
			return err
		}
		yes, err = rd.cli.SetNX(ctx, rd.name, rd.holder,
			time.Duration(rd.expiration)*time.Millisecond).Result()
		if err != nil {
			return err
		}
		if yes {
			return nil
		}
	}
}

// Unlock unlocks rd.
func (rd *RedLock) Unlock(ctx context.Context) error {
	ret, err := luaUnlock.Run(ctx, rd.cli, []string{rd.name, rd.signalName}, rd.holder).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrLockNotHeld
		}
		return err
	}
	if i, ok := ret.(int64); !ok || i != 0 {
		return ErrLockNotHeld
	}
	if rd.autoRefresh {
		close(rd.stopRefresh)
	}
	return nil
}
