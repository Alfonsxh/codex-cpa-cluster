package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// New returns the common background-task scheduler. Every Go worker gets the
// same no-overlap and panic-recovery policy instead of rebuilding it around a
// hand-written sleep loop.
func New(logger *zap.Logger) *cron.Cron {
	adapter := ZapLogger{Logger: logger}
	return cron.New(
		cron.WithLogger(adapter),
		cron.WithChain(cron.SkipIfStillRunning(adapter), cron.Recover(adapter)),
	)
}

func Every(interval time.Duration) string {
	return "@every " + interval.String()
}

type ZapLogger struct {
	Logger *zap.Logger
}

func (adapter ZapLogger) Info(message string, keysAndValues ...any) {
	adapter.logger().Info(message, fields(keysAndValues)...)
}

func (adapter ZapLogger) Error(err error, message string, keysAndValues ...any) {
	values := fields(keysAndValues)
	values = append(values, zap.Error(err))
	adapter.logger().Error(message, values...)
}

func (adapter ZapLogger) logger() *zap.Logger {
	if adapter.Logger == nil {
		return zap.NewNop()
	}
	return adapter.Logger
}

func fields(keysAndValues []any) []zap.Field {
	result := make([]zap.Field, 0, (len(keysAndValues)+1)/2)
	for index := 0; index < len(keysAndValues); index += 2 {
		key := fmt.Sprint(keysAndValues[index])
		if index+1 < len(keysAndValues) {
			result = append(result, zap.Any(key, keysAndValues[index+1]))
		} else {
			result = append(result, zap.Any("value", keysAndValues[index]))
		}
	}
	return result
}
