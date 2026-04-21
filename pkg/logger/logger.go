package logger

import (
	"fmt"
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	global *zap.Logger
	once   sync.Once
)

// Init 初始化全局日志。format=console|json,output=stdout|<file path>.
func Init(level, format, output string) error {
	var initErr error
	once.Do(func() {
		lvl := zapcore.InfoLevel
		if err := lvl.UnmarshalText([]byte(level)); err != nil {
			initErr = fmt.Errorf("invalid log level %q: %w", level, err)
			return
		}

		encCfg := zap.NewProductionEncoderConfig()
		encCfg.TimeKey = "ts"
		encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		encCfg.EncodeDuration = zapcore.StringDurationEncoder
		encCfg.EncodeLevel = zapcore.CapitalLevelEncoder

		var encoder zapcore.Encoder
		if format == "json" {
			encoder = zapcore.NewJSONEncoder(encCfg)
		} else {
			encCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
			encoder = zapcore.NewConsoleEncoder(encCfg)
		}

		var ws zapcore.WriteSyncer
		if output == "" || output == "stdout" {
			ws = zapcore.AddSync(os.Stdout)
		} else {
			f, err := os.OpenFile(output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				initErr = fmt.Errorf("open log file %q: %w", output, err)
				return
			}
			ws = zapcore.AddSync(f)
		}

		core := zapcore.NewCore(encoder, ws, lvl)
		global = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	})
	return initErr
}

// L 返回全局 logger。
func L() *zap.Logger {
	if global == nil {
		// 兜底:未初始化时返回开发 logger,避免 panic。
		l, _ := zap.NewDevelopment()
		return l
	}
	return global
}

// Sync 刷新缓冲。
func Sync() {
	if global != nil {
		_ = global.Sync()
	}
}
