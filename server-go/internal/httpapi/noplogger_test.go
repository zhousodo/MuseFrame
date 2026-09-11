package httpapi

import "museframe-api/internal/logx"

func newNopLogger() *logx.Logger { return logx.NewWith(discardWriter{}, nil) }
