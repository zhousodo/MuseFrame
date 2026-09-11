package httpapi

import "bytes"

type bytesReader struct{ *bytes.Reader }

func newBytesReader(b []byte) *bytesReader { return &bytesReader{bytes.NewReader(b)} }
