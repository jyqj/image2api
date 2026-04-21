// Package usage 实现 usage_logs 的异步批量写入。
//
// 设计目标:
//  1. 网关请求落盘不在关键路径上(同步 INSERT 会拖垮高并发)。
//  2. Channel 缓冲 + 定时 flush + 批量 INSERT。
//  3. 丢弃策略:channel 满时异步降级到 Warn 日志,不阻塞调用方。
//
// 参数(默认):
//   - buffer: 8192 条
//   - batch : 500 条
//   - flush : 200ms
package usage

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/pkg/logger"
)

// Options 可选参数。
type Options struct {
	Buffer        int
	Batch         int
	FlushInterval time.Duration
}

// Logger 异步写入器。
type Logger struct {
	db     *sqlx.DB
	ch     chan *Log
	opt    Options
	closed chan struct{}
	wg     sync.WaitGroup
}

// New 创建并启动后台 flusher。调用方在进程退出前应 Close。
func New(db *sqlx.DB, opt Options) *Logger {
	if opt.Buffer <= 0 {
		opt.Buffer = 8192
	}
	if opt.Batch <= 0 {
		opt.Batch = 500
	}
	if opt.FlushInterval <= 0 {
		opt.FlushInterval = 200 * time.Millisecond
	}
	l := &Logger{
		db:     db,
		ch:     make(chan *Log, opt.Buffer),
		opt:    opt,
		closed: make(chan struct{}),
	}
	l.wg.Add(1)
	go l.loop()
	return l
}

// Write 非阻塞投递一条日志。channel 满时降级到 Warn 日志并丢弃。
func (l *Logger) Write(row *Log) {
	if row == nil {
		return
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	select {
	case l.ch <- row:
	default:
		logger.L().Warn("usage_logs channel full, dropping entry",
			zap.String("request_id", row.RequestID))
	}
}

// Close 停止后台 flusher,并把剩余 buffer 落盘。
func (l *Logger) Close() {
	close(l.closed)
	l.wg.Wait()
}

func (l *Logger) loop() {
	defer l.wg.Done()
	tick := time.NewTicker(l.opt.FlushInterval)
	defer tick.Stop()

	batch := make([]*Log, 0, l.opt.Batch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := l.bulkInsert(batch); err != nil {
			logger.L().Error("usage_logs bulk insert", zap.Error(err),
				zap.Int("rows", len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-l.closed:
			// drain
			for {
				select {
				case row := <-l.ch:
					batch = append(batch, row)
					if len(batch) >= l.opt.Batch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case row := <-l.ch:
			batch = append(batch, row)
			if len(batch) >= l.opt.Batch {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (l *Logger) bulkInsert(rows []*Log) error {
	if len(rows) == 0 {
		return nil
	}
	const cols = 15
	var b strings.Builder
	b.WriteString(`INSERT INTO usage_logs
        (model_id, account_id, request_id, type,
         input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
         image_count, duration_ms, status, error_code, ip, ua, created_at)
        VALUES `)

	args := make([]interface{}, 0, len(rows)*cols)
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.ModelID, r.AccountID, r.RequestID, r.Type,
			r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens,
			r.ImageCount, r.DurationMs, r.Status, r.ErrorCode,
			r.IP, r.UA, r.CreatedAt,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := l.db.ExecContext(ctx, b.String(), args...)
	return err
}
