package observability

import (
	"context"
	"log/slog"
)

// auditMarker lets log pipelines separate security events from ordinary
// application logs without a second logging stack.
const auditMarker = "audit"

// Auditor emits security-relevant events. Callers pass only identifiers and
// outcomes; passwords, tokens, idempotency keys and payloads never belong here.
type Auditor struct {
	logger *slog.Logger
}

func NewAuditor(logger *slog.Logger) *Auditor {
	if logger == nil {
		return nil
	}
	return &Auditor{logger: logger.With(slog.Bool(auditMarker, true))}
}

// Event records one audit event. A nil auditor is a no-op so callers do not
// need guards of their own.
func (a *Auditor) Event(ctx context.Context, event string, attrs ...slog.Attr) {
	if a == nil {
		return
	}

	attributes := make([]slog.Attr, 0, len(attrs)+1)
	attributes = append(attributes, slog.String("event", event))
	attributes = append(attributes, attrs...)
	a.logger.LogAttrs(ctx, slog.LevelInfo, "audit event", attributes...)
}
