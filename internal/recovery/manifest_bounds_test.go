package recovery

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestManifestReadAndWriteUseTheSameHardLimit(t *testing.T) {
	manifest := Manifest{Format: backupFormat, Version: backupVersion, AccessMode: "open", AdminHash: "synthetic-hash"}
	var complete bytes.Buffer
	noErr(t, writeManifest(&complete, manifest, 1<<20))
	maximum := int64(complete.Len())
	var exact bytes.Buffer
	if err := writeManifest(&exact, manifest, maximum); err != nil || int64(exact.Len()) != maximum {
		t.Fatalf("exact-limit write bytes=%d err=%v", exact.Len(), err)
	}
	var oversized bytes.Buffer
	if err := writeManifest(&oversized, manifest, maximum-1); !errors.Is(err, errManifestTooLarge) || int64(oversized.Len()) > maximum-1 {
		t.Fatalf("oversized write bytes=%d err=%v", oversized.Len(), err)
	}

	if content, err := readManifestContent(bytes.NewReader(exact.Bytes()), maximum); err != nil || !bytes.Equal(content, exact.Bytes()) {
		t.Fatalf("exact-limit read bytes=%d err=%v", len(content), err)
	}
	counter := &countingManifestReader{reader: bytes.NewReader(append(exact.Bytes(), 'x'))}
	if _, err := readManifestContent(counter, maximum); !errors.Is(err, errManifestTooLarge) || counter.read != maximum+1 {
		t.Fatalf("oversized read consumed=%d want=%d err=%v", counter.read, maximum+1, err)
	}
}

func TestManifestBoundsPreserveUnderlyingIOErrors(t *testing.T) {
	sentinel := errors.New("synthetic manifest I/O failure")
	manifest := Manifest{Format: backupFormat, Version: backupVersion}
	if err := writeManifest(errorManifestWriter{err: sentinel}, manifest, maximumManifest); !errors.Is(err, sentinel) {
		t.Fatalf("write error=%v", err)
	}
	if _, err := readManifestContent(errorManifestReader{err: sentinel}, maximumManifest); !errors.Is(err, sentinel) {
		t.Fatalf("read error=%v", err)
	}
}

type countingManifestReader struct {
	reader io.Reader
	read   int64
}

func (reader *countingManifestReader) Read(content []byte) (int, error) {
	count, err := reader.reader.Read(content)
	reader.read += int64(count)
	return count, err
}

type errorManifestWriter struct{ err error }

func (writer errorManifestWriter) Write([]byte) (int, error) { return 0, writer.err }

type errorManifestReader struct{ err error }

func (reader errorManifestReader) Read([]byte) (int, error) { return 0, reader.err }
