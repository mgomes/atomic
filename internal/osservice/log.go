package osservice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	service "github.com/kardianos/service"
)

type systemHandler struct {
	logger service.Logger
	attrs  []slog.Attr
	groups []string
}

func (h *systemHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *systemHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Message)
	for _, attr := range h.attrs {
		appendAttribute(&line, h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		appendAttribute(&line, h.groups, attr)
		return true
	})
	switch {
	case record.Level >= slog.LevelError:
		return h.logger.Error(line.String())
	case record.Level >= slog.LevelWarn:
		return h.logger.Warning(line.String())
	default:
		return h.logger.Info(line.String())
	}
}

func (h *systemHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

func (h *systemHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func appendAttribute(line *strings.Builder, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	line.WriteByte(' ')
	if len(groups) > 0 {
		line.WriteString(strings.Join(groups, "."))
		line.WriteByte('.')
	}
	line.WriteString(attr.Key)
	line.WriteByte('=')
	line.WriteString(fmt.Sprint(attr.Value.Any()))
}
