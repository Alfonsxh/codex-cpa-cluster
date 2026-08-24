package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
)

const (
	defaultQueueName = "usage"
	maxBatchSize     = 500
)

type QueueConfig struct {
	Address      string
	Password     string
	Name         string
	BatchSize    int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Queue drains the CLIProxyAPI usage list through Redigo. Redigo is used here
// because the upstream exposes a deliberately narrow RESP2 AUTH/LPOP contract,
// not a complete Redis server with HELLO or CLIENT support.
type Queue struct {
	address      string
	password     string
	name         string
	batchSize    int
	dialTimeout  time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func NewQueue(config QueueConfig) (*Queue, error) {
	address := strings.TrimSpace(config.Address)
	if address == "" {
		return nil, errors.New("usage queue address is required")
	}
	if strings.TrimSpace(config.Password) == "" {
		return nil, errors.New("usage queue password is required")
	}
	if config.BatchSize < 1 || config.BatchSize > maxBatchSize {
		return nil, fmt.Errorf("usage queue batch size must be between 1 and %d", maxBatchSize)
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultQueueName
	}
	dialTimeout := config.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}
	readTimeout := config.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 5 * time.Second
	}
	writeTimeout := config.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 5 * time.Second
	}
	return &Queue{
		address: address, password: config.Password, name: name, batchSize: config.BatchSize,
		dialTimeout: dialTimeout, readTimeout: readTimeout, writeTimeout: writeTimeout,
	}, nil
}

func (queue *Queue) Close() error { return nil }

// Drain calls consume once for each non-empty batch. It stops at the first
// short batch, an empty queue, a cancelled context, or a consumer error.
func (queue *Queue) Drain(ctx context.Context, consume func([][]byte) error) error {
	if queue == nil {
		return errors.New("usage queue is not initialized")
	}
	if consume == nil {
		return errors.New("usage queue consumer is required")
	}
	connection, err := redis.DialContext(
		ctx,
		"tcp",
		queue.address,
		redis.DialConnectTimeout(queue.dialTimeout),
		redis.DialReadTimeout(queue.readTimeout),
		redis.DialWriteTimeout(queue.writeTimeout),
	)
	if err != nil {
		return fmt.Errorf("connect to usage queue: %w", err)
	}
	defer connection.Close()
	contextConnection, ok := connection.(redis.ConnWithContext)
	if !ok {
		return errors.New("usage queue connection does not support context cancellation")
	}
	response, err := redis.String(contextConnection.DoContext(ctx, "AUTH", queue.password))
	if err != nil {
		return fmt.Errorf("authenticate usage queue: %w", safeQueueError(ctx, err, queue.password))
	}
	if response != "OK" {
		return errors.New("usage queue authentication failed")
	}
	for {
		reply, err := contextConnection.DoContext(ctx, "LPOP", queue.name, queue.batchSize)
		if err != nil {
			return fmt.Errorf("drain usage queue: %w", safeQueueError(ctx, err, queue.password))
		}
		items, err := usageQueueItems(reply)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if err := consume(items); err != nil {
			return fmt.Errorf("consume usage queue batch: %w", err)
		}
		if len(items) < queue.batchSize {
			return nil
		}
	}
}

func safeQueueError(ctx context.Context, err error, secret string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return errors.New(message)
}

func usageQueueItems(reply any) ([][]byte, error) {
	switch value := reply.(type) {
	case nil:
		return nil, nil
	case []byte:
		return [][]byte{value}, nil
	case string:
		return [][]byte{[]byte(value)}, nil
	case []any:
		result := make([][]byte, 0, len(value))
		for _, item := range value {
			switch item := item.(type) {
			case []byte:
				result = append(result, item)
			case string:
				result = append(result, []byte(item))
			case nil:
				continue
			default:
				return nil, fmt.Errorf("usage queue returned unsupported item type %T", item)
			}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("usage queue returned unsupported reply type %T", reply)
	}
}
