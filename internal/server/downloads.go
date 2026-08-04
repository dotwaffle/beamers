package server

import (
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	// archiveWriteTimeout bounds one chunk write to a downloading client.
	archiveWriteTimeout = 30 * time.Second
	// archiveChunkSize bounds how much is written under one deadline.
	archiveChunkSize = 256 * 1024
)

// writeArchiveDownload streams an archive to the client under a write deadline
// that is refreshed for every chunk. A client that stops reading then fails its
// own download instead of pinning the staging file and the connection for as
// long as it likes. Response writers that cannot carry deadlines still receive
// the archive, unbounded as before.
func writeArchiveDownload(
	response http.ResponseWriter,
	archive io.Reader,
) (err error) {
	controller := http.NewResponseController(response)
	defer func() {
		err = errors.Join(err, clearWriteDeadline(controller))
	}()
	buffer := make([]byte, archiveChunkSize)
	for {
		read, readErr := archive.Read(buffer)
		if read > 0 {
			if deadlineErr := controller.SetWriteDeadline(
				time.Now().Add(archiveWriteTimeout),
			); deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
				return deadlineErr
			}
			if _, writeErr := response.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func clearWriteDeadline(controller *http.ResponseController) error {
	if err := controller.SetWriteDeadline(time.Time{}); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
