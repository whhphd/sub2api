package service

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"gopkg.in/natefinch/lumberjack.v2"
)

type turnStateLog struct {
	once          sync.Once
	writer        *lumberjack.Logger
	failures      atomic.Uint64
	lastWarning   atomic.Int64
	writeOverride func([]byte) (int, error) // tests only; configured before use
}

func turnStateRequestRef(ctx context.Context) string {
	id, _ := ctx.Value(ctxkey.RequestID).(string)
	return codexDiagnosticHash(id)
}

func (s *OpenAIGatewayService) logTurnState(a *turnStateAttempt, event, reason string, extra map[string]any) {
	if s == nil || a == nil {
		return
	}
	// No inherited logger fields, error strings, raw models, tokens, or token prefixes.
	record := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": event, "reason": reason, "process_ref": codexDiagnosticHash("turn-state-process"), "attempt": a.ID, "request_ref": a.RequestRef, "account_ref": codexDiagnosticHash(a.Owner), "session_ref": a.Session, "model": diagnosticModel(a.Model), "auto_enabled": a.Enabled, "injected": a.Injected != "", "original_length": len(a.Original), "sent_length": len(a.Sent), "state_ref": codexDiagnosticHash(a.Injected)}
	for k, v := range extra {
		record[k] = v
	}
	record["log_write_failures"] = s.turnStateLog.failures.Load()
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	l := &s.turnStateLog
	l.once.Do(func() {
		data := os.Getenv("DATA_DIR")
		if data == "" {
			data = "/app/data"
		}
		l.writer = &lumberjack.Logger{Filename: filepath.Join(data, "logs", "codex-turn-state.log"), MaxSize: 100, MaxBackups: 10, MaxAge: 7, Compress: true}
	})
	var written int
	if l.writeOverride != nil {
		written, err = l.writeOverride(payload)
	} else {
		written, err = l.writer.Write(payload)
	}
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	if err != nil {
		count := l.failures.Add(1)
		now := time.Now().Unix()
		last := l.lastWarning.Load()
		if now-last >= 30 && l.lastWarning.CompareAndSwap(last, now) {
			logger.L().Error("codex_turn_state_log_write_failed", zap.Uint64("failures", count), zap.String("reason", "independent_log_unavailable"))
		}
	}
}
