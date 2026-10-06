package webui

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// AcceptsGzip reports whether the client accepts a gzip response.
func AcceptsGzip(header http.Header) bool {
	for _, field := range header.Values("Accept-Encoding") {
		for _, item := range strings.Split(field, ",") {
			coding, parameters, _ := strings.Cut(item, ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
				continue
			}
			parameters = strings.ReplaceAll(strings.ToLower(parameters), " ", "")
			if quality, ok := strings.CutPrefix(parameters, "q="); ok {
				value, err := strconv.ParseFloat(quality, 64)
				return err != nil || value > 0
			}
			return true
		}
	}
	return false
}

// Gzip compresses content.
func Gzip(content []byte) []byte {
	var output bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&output, gzip.DefaultCompression)
	_, _ = writer.Write(content)
	_ = writer.Close()
	return output.Bytes()
}
