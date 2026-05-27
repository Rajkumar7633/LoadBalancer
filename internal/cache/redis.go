package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// RedisCache implements distributed caching with Redis
type RedisCache struct {
	client *redis.Client
	logger *zap.Logger
	config Config
}

// Config holds Redis configuration
type Config struct {
	Addr         string        `json:"addr"`
	Password     string        `json:"password"`
	DB           int           `json:"db"`
	PoolSize     int           `json:"pool_size"`
	MinIdleConns int           `json:"min_idle_conns"`
	DialTimeout  time.Duration `json:"dial_timeout"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	PoolTimeout  time.Duration `json:"pool_timeout"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
}

// NewRedisCache creates a new Redis cache client
func NewRedisCache(config Config, logger *zap.Logger) (*RedisCache, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         config.Addr,
		Password:     config.Password,
		DB:           config.DB,
		PoolSize:     config.PoolSize,
		MinIdleConns: config.MinIdleConns,
		DialTimeout:  config.DialTimeout,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		PoolTimeout:  config.PoolTimeout,
		IdleTimeout:  config.IdleTimeout,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	cache := &RedisCache{
		client: rdb,
		logger: logger,
		config: config,
	}

	logger.Info("Redis cache connection established",
		zap.String("addr", config.Addr),
		zap.Int("db", config.DB))

	return cache, nil
}

// Set stores a key-value pair with TTL
func (rc *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	jsonValue, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	if err := rc.client.Set(ctx, key, jsonValue, ttl).Err(); err != nil {
		return fmt.Errorf("failed to set key: %w", err)
	}

	return nil
}

// Get retrieves a value by key
func (rc *RedisCache) Get(ctx context.Context, key string, dest interface{}) error {
	val, err := rc.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return ErrKeyNotFound
		}
		return fmt.Errorf("failed to get key: %w", err)
	}

	if err := json.Unmarshal([]byte(val), dest); err != nil {
		return fmt.Errorf("failed to unmarshal value: %w", err)
	}

	return nil
}

// Delete removes a key
func (rc *RedisCache) Delete(ctx context.Context, key string) error {
	if err := rc.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete key: %w", err)
	}

	return nil
}

// Exists checks if a key exists
func (rc *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	count, err := rc.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check key existence: %w", err)
	}

	return count > 0, nil
}

// SetTTL updates the TTL for a key
func (rc *RedisCache) SetTTL(ctx context.Context, key string, ttl time.Duration) error {
	if err := rc.client.Expire(ctx, key, ttl).Err(); err != nil {
		return fmt.Errorf("failed to set TTL: %w", err)
	}

	return nil
}

// GetTTL retrieves the remaining TTL for a key
func (rc *RedisCache) GetTTL(ctx context.Context, key string) (time.Duration, error) {
	ttl, err := rc.client.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get TTL: %w", err)
	}

	return ttl, nil
}

// Increment increments a numeric key value
func (rc *RedisCache) Increment(ctx context.Context, key string) (int64, error) {
	result, err := rc.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to increment key: %w", err)
	}

	return result, nil
}

// IncrementBy increments a numeric key by a specific amount
func (rc *RedisCache) IncrementBy(ctx context.Context, key string, value int64) (int64, error) {
	result, err := rc.client.IncrBy(ctx, key, value).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to increment key by value: %w", err)
	}

	return result, nil
}

// Decrement decrements a numeric key value
func (rc *RedisCache) Decrement(ctx context.Context, key string) (int64, error) {
	result, err := rc.client.Decr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to decrement key: %w", err)
	}

	return result, nil
}

// PushToList adds a value to a list
func (rc *RedisCache) PushToList(ctx context.Context, key string, values ...interface{}) error {
	if err := rc.client.LPush(ctx, key, values...).Err(); err != nil {
		return fmt.Errorf("failed to push to list: %w", err)
	}

	return nil
}

// PopFromList removes and returns a value from a list
func (rc *RedisCache) PopFromList(ctx context.Context, key string) (string, error) {
	result, err := rc.client.RPop(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", ErrKeyNotFound
		}
		return "", fmt.Errorf("failed to pop from list: %w", err)
	}

	return result, nil
}

// GetListRange retrieves a range of values from a list
func (rc *RedisCache) GetListRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	result, err := rc.client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get list range: %w", err)
	}

	return result, nil
}

// AddToSet adds a value to a set
func (rc *RedisCache) AddToSet(ctx context.Context, key string, members ...interface{}) error {
	if err := rc.client.SAdd(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("failed to add to set: %w", err)
	}

	return nil
}

// RemoveFromSet removes a value from a set
func (rc *RedisCache) RemoveFromSet(ctx context.Context, key string, members ...interface{}) error {
	if err := rc.client.SRem(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("failed to remove from set: %w", err)
	}

	return nil
}

// IsMemberOfSet checks if a value is in a set
func (rc *RedisCache) IsMemberOfSet(ctx context.Context, key string, member interface{}) (bool, error) {
	result, err := rc.client.SIsMember(ctx, key, member).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check set membership: %w", err)
	}

	return result, nil
}

// GetSetMembers retrieves all members of a set
func (rc *RedisCache) GetSetMembers(ctx context.Context, key string) ([]string, error) {
	result, err := rc.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get set members: %w", err)
	}

	return result, nil
}

// SetHashField sets a field in a hash
func (rc *RedisCache) SetHashField(ctx context.Context, key, field string, value interface{}) error {
	if err := rc.client.HSet(ctx, key, field, value).Err(); err != nil {
		return fmt.Errorf("failed to set hash field: %w", err)
	}

	return nil
}

// GetHashField retrieves a field from a hash
func (rc *RedisCache) GetHashField(ctx context.Context, key, field string) (string, error) {
	result, err := rc.client.HGet(ctx, key, field).Result()
	if err != nil {
		if err == redis.Nil {
			return "", ErrKeyNotFound
		}
		return "", fmt.Errorf("failed to get hash field: %w", err)
	}

	return result, nil
}

// GetHashAll retrieves all fields and values from a hash
func (rc *RedisCache) GetHashAll(ctx context.Context, key string) (map[string]string, error) {
	result, err := rc.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get all hash fields: %w", err)
	}

	return result, nil
}

// DeleteHashField deletes a field from a hash
func (rc *RedisCache) DeleteHashField(ctx context.Context, key, field string) error {
	if err := rc.client.HDel(ctx, key, field).Err(); err != nil {
		return fmt.Errorf("failed to delete hash field: %w", err)
	}

	return nil
}

// Publish publishes a message to a channel
func (rc *RedisCache) Publish(ctx context.Context, channel string, message interface{}) error {
	if err := rc.client.Publish(ctx, channel, message).Err(); err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

// Subscribe subscribes to a channel
func (rc *RedisCache) Subscribe(ctx context.Context, channels ...string) (*redis.PubSub, error) {
	pubsub := rc.client.Subscribe(ctx, channels...)

	// Test the subscription by checking the context
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("failed to subscribe to channels: %w", ctx.Err())
	default:
		// Subscription is successful if no immediate error
	}

	return pubsub, nil
}

// Close closes the Redis connection
func (rc *RedisCache) Close() error {
	return rc.client.Close()
}

// Ping tests the Redis connection
func (rc *RedisCache) Ping(ctx context.Context) error {
	return rc.client.Ping(ctx).Err()
}

// GetStats returns Redis statistics
func (rc *RedisCache) GetStats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Get info
	info, err := rc.client.Info(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get Redis info: %w", err)
	}
	stats["info"] = info

	// Get config
	config, err := rc.client.ConfigGet(ctx, "*").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get Redis config: %w", err)
	}
	stats["config"] = config

	// Get pool stats
	poolStats := rc.client.PoolStats()
	stats["pool_stats"] = map[string]interface{}{
		"hits":        poolStats.Hits,
		"misses":      poolStats.Misses,
		"timeouts":    poolStats.Timeouts,
		"total_conns": poolStats.TotalConns,
		"idle_conns":  poolStats.IdleConns,
		"stale_conns": poolStats.StaleConns,
	}

	return stats, nil
}

// CacheMetrics represents cache performance metrics
type CacheMetrics struct {
	Hits        int64   `json:"hits"`
	Misses      int64   `json:"misses"`
	HitRate     float64 `json:"hit_rate"`
	Sets        int64   `json:"sets"`
	Gets        int64   `json:"gets"`
	Deletes     int64   `json:"deletes"`
	Keys        int64   `json:"keys"`
	MemoryUsed  int64   `json:"memory_used"`
	MemoryPeak  int64   `json:"memory_peak"`
	EvictedKeys int64   `json:"evicted_keys"`
}

// GetCacheMetrics retrieves cache performance metrics
func (rc *RedisCache) GetCacheMetrics(ctx context.Context) (*CacheMetrics, error) {
	info, err := rc.client.Info(ctx, "stats", "memory", "keyspace").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get Redis metrics: %w", err)
	}

	metrics := &CacheMetrics{}

	// Parse info to extract metrics
	lines := strings.Split(info, "\r\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "stats:") {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				key := strings.TrimPrefix(parts[0], "stats:")
				value := parts[1]

				switch key {
				case "keyspace_hits":
					if val, err := strconv.ParseInt(value, 10, 64); err == nil {
						metrics.Hits = val
					}
				case "keyspace_misses":
					if val, err := strconv.ParseInt(value, 10, 64); err == nil {
						metrics.Misses = val
					}
				}
			}
		} else if strings.HasPrefix(line, "memory:") {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				key := strings.TrimPrefix(parts[0], "memory:")
				value := parts[1]

				switch key {
				case "used_memory":
					if val, err := strconv.ParseInt(value, 10, 64); err == nil {
						metrics.MemoryUsed = val
					}
				case "used_memory_peak":
					if val, err := strconv.ParseInt(value, 10, 64); err == nil {
						metrics.MemoryPeak = val
					}
				}
			}
		} else if strings.HasPrefix(line, "evicted_keys:") {
			if val, err := strconv.ParseInt(strings.Split(line, ":")[1], 10, 64); err == nil {
				metrics.EvictedKeys = val
			}
		}
	}

	// Calculate hit rate
	if metrics.Hits+metrics.Misses > 0 {
		metrics.HitRate = float64(metrics.Hits) / float64(metrics.Hits+metrics.Misses)
	}

	return metrics, nil
}

// ErrKeyNotFound is returned when a key is not found
var ErrKeyNotFound = fmt.Errorf("key not found")

// DistributedLock represents a distributed lock
type DistributedLock struct {
	client *redis.Client
	key    string
	value  string
	ttl    time.Duration
}

// AcquireLock acquires a distributed lock
func (rc *RedisCache) AcquireLock(ctx context.Context, key string, ttl time.Duration) (*DistributedLock, error) {
	value := fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63())

	success, err := rc.client.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if !success {
		return nil, fmt.Errorf("lock already held")
	}

	return &DistributedLock{
		client: rc.client,
		key:    key,
		value:  value,
		ttl:    ttl,
	}, nil
}

// Release releases the distributed lock
func (dl *DistributedLock) Release(ctx context.Context) error {
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`

	result, err := dl.client.Eval(ctx, script, []string{dl.key}, dl.value).Result()
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	if result.(int64) == 0 {
		return fmt.Errorf("lock not held or expired")
	}

	return nil
}

// Extend extends the lock TTL
func (dl *DistributedLock) Extend(ctx context.Context, ttl time.Duration) error {
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("expire", KEYS[1], ARGV[2])
		else
			return 0
		end
	`

	result, err := dl.client.Eval(ctx, script, []string{dl.key}, dl.value, int(ttl.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("failed to extend lock: %w", err)
	}

	if result.(int64) == 0 {
		return fmt.Errorf("lock not held or expired")
	}

	dl.ttl = ttl
	return nil
}
