package snapshot

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"aws-config-graph/internal/model"
)

// Open opens a snapshot file, transparently handling .gz files via the
// gzip magic bytes (so paths without a .gz suffix still work).
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		f.Close()
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &gzipReadCloser{gz: gz, f: f}, nil
	}
	return &bufReadCloser{Reader: br, f: f}, nil
}

type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzipReadCloser) Close() error {
	g.gz.Close()
	return g.f.Close()
}

type bufReadCloser struct {
	io.Reader
	f *os.File
}

func (b *bufReadCloser) Close() error { return b.f.Close() }

// Stream walks configurationItems and invokes fn for each. It uses a
// json.Decoder so that we don't have to buffer the whole snapshot in memory.
func Stream(r io.Reader, fn func(item model.ConfigItem) error) error {
	dec := json.NewDecoder(r)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return fmt.Errorf("configurationItems not found in snapshot")
		}
		if err != nil {
			return fmt.Errorf("scan tokens: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			continue
		}
		if !strings.EqualFold(key, "configurationItems") {
			// Skip the value attached to this key so we don't recurse into it.
			if err := skipValue(dec); err != nil {
				return fmt.Errorf("skip %s: %w", key, err)
			}
			continue
		}

		// Expect '['
		tok, err = dec.Token()
		if err != nil {
			return fmt.Errorf("expected array after configurationItems: %w", err)
		}
		d, ok := tok.(json.Delim)
		if !ok || d != '[' {
			return fmt.Errorf("expected array after configurationItems, got %v", tok)
		}

		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return fmt.Errorf("decode item: %w", err)
			}
			var item model.ConfigItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("unmarshal item: %w", err)
			}
			item.Raw = raw
			if err := fn(item); err != nil {
				return err
			}
		}
		return nil
	}
}

// skipValue advances the decoder past the next JSON value.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		for dec.More() {
			// key
			if _, err := dec.Token(); err != nil {
				return err
			}
			// value
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		// closing '}'
		_, err := dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	}
	return nil
}

// ReadAll is a convenience wrapper that collects all items in memory.
// Useful in tests; production callers should prefer Stream.
func ReadAll(r io.Reader) ([]model.ConfigItem, error) {
	var items []model.ConfigItem
	err := Stream(r, func(item model.ConfigItem) error {
		items = append(items, item)
		return nil
	})
	return items, err
}

// ReadAllFromBytes is a tiny helper used in tests.
func ReadAllFromBytes(b []byte) ([]model.ConfigItem, error) {
	return ReadAll(bytes.NewReader(b))
}
