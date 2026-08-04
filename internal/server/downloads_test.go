package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct {
	body      bytes.Buffer
	deadlines []time.Time
	writeErr  error
	setErr    error
}

func (recorder *deadlineRecorder) Header() http.Header { return http.Header{} }

func (recorder *deadlineRecorder) WriteHeader(int) {}

func (recorder *deadlineRecorder) Write(data []byte) (int, error) {
	if recorder.writeErr != nil {
		return 0, recorder.writeErr
	}
	return recorder.body.Write(data)
}

func (recorder *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	if recorder.setErr != nil {
		return recorder.setErr
	}
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

func TestWriteArchiveDownloadRefreshesDeadlinePerChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		size      int
		wantWrite int
	}{
		{name: "empty archive", size: 0, wantWrite: 0},
		{name: "single partial chunk", size: 1024, wantWrite: 1},
		{name: "exactly one chunk", size: archiveChunkSize, wantWrite: 1},
		{name: "several chunks", size: 2*archiveChunkSize + 7, wantWrite: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archive := bytes.Repeat([]byte("beamers!"), test.size/8+1)[:test.size]
			recorder := &deadlineRecorder{}
			if err := writeArchiveDownload(recorder, bytes.NewReader(archive)); err != nil {
				t.Fatalf("write archive download: %v", err)
			}
			if !bytes.Equal(recorder.body.Bytes(), archive) {
				t.Fatalf("streamed %d bytes, want %d", recorder.body.Len(), len(archive))
			}
			if len(recorder.deadlines) != test.wantWrite+1 {
				t.Fatalf(
					"deadline calls = %d, want %d refreshes and one clear",
					len(recorder.deadlines),
					test.wantWrite,
				)
			}
			for index, deadline := range recorder.deadlines[:test.wantWrite] {
				if deadline.IsZero() {
					t.Errorf("chunk %d was written without a deadline", index)
				}
			}
			if !recorder.deadlines[test.wantWrite].IsZero() {
				t.Error("write deadline was left set after the download")
			}
		})
	}
}

func TestWriteArchiveDownloadReportsFailures(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("client went away")
	setErr := errors.New("deadlines unsupported")
	tests := []struct {
		name     string
		recorder *deadlineRecorder
		archive  io.Reader
		want     error
	}{
		{
			name:     "client stopped reading",
			recorder: &deadlineRecorder{writeErr: writeErr},
			archive:  strings.NewReader("archive"),
			want:     writeErr,
		},
		{
			name:     "deadline cannot be applied",
			recorder: &deadlineRecorder{setErr: setErr},
			archive:  strings.NewReader("archive"),
			want:     setErr,
		},
		{
			name:     "archive could not be read",
			recorder: &deadlineRecorder{},
			archive:  io.MultiReader(strings.NewReader("head"), errorReader{}),
			want:     errReadArchive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := writeArchiveDownload(test.recorder, test.archive)
			if !errors.Is(err, test.want) {
				t.Fatalf("write archive download error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWriteArchiveDownloadStreamsWithoutDeadlineSupport(t *testing.T) {
	t.Parallel()
	recorder := &deadlineRecorder{setErr: http.ErrNotSupported}
	if err := writeArchiveDownload(recorder, strings.NewReader("archive")); err != nil {
		t.Fatalf("write archive download: %v", err)
	}
	if recorder.body.String() != "archive" {
		t.Fatalf("streamed %q, want the whole archive", recorder.body.String())
	}
}

func TestWriteArchiveDownloadReachesDeadlinesThroughWrappers(t *testing.T) {
	t.Parallel()
	recorder := &deadlineRecorder{}
	wrapped := []http.ResponseWriter{
		&statusResponse{ResponseWriter: recorder},
		&browserRecoveryResponse{ResponseWriter: recorder},
		&crewWarningResponse{ResponseWriter: recorder},
	}
	for _, response := range wrapped {
		recorder.deadlines = nil
		if err := writeArchiveDownload(response, strings.NewReader("archive")); err != nil {
			t.Fatalf("%T: write archive download: %v", response, err)
		}
		if len(recorder.deadlines) != 2 || recorder.deadlines[0].IsZero() {
			t.Fatalf("%T: deadline calls = %v", response, recorder.deadlines)
		}
	}
}

var errReadArchive = errors.New("staging archive unreadable")

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errReadArchive }
