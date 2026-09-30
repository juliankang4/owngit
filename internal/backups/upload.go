package backups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"owngit/internal/recovery"
)

// An owner can upload a backup archive (recovery.WriteArchive) to restore
// it on this computer. OwnGit keeps one uploaded backup, in the folder
// UploadsFolder of its state folder: a new upload replaces it, and it is
// removed UploadKept after it arrived and whenever OwnGit starts. Once
// unpacked, it is verified as a new backup is; one that fails is removed at
// once. It is never restored by OwnGit itself: owngit restore reads it
// while OwnGit is stopped.

const (
	// UploadsFolder is the folder in the state folder that holds the
	// uploaded backup.
	UploadsFolder = "backup-uploads"
	// UploadKept is how long an uploaded backup stays.
	UploadKept = 24 * time.Hour
	// UploadRoom is the free space an upload leaves on the disk of the
	// state folder beyond its own size.
	UploadRoom = 1 << 30
)

// Upload states.
const (
	UploadVerifying = "verifying"
	UploadPassed    = "passed"
	UploadFailed    = "failed"
)

// Upload is the uploaded backup as owners see it.
type Upload struct {
	// Name is the backup folder the archive held; Path is where it is,
	// empty once removed.
	Name string `json:"name"`
	Path string `json:"path"`
	// Size is the size of the archive.
	Size       int64     `json:"size"`
	Status     string    `json:"status"`
	Message    string    `json:"message,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	// RemovesAt is when OwnGit removes a verified upload.
	RemovesAt  *time.Time `json:"removes_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// ErrUploadRefused is an archive that is not a backup, ends early, or has
// no room; the error that wraps it says why. Nothing it held is kept.
var ErrUploadRefused = errors.New("the uploaded backup was refused")

// uploadsPath is the folder of the uploaded backup.
func (s *Service) uploadsPath() string {
	return filepath.Join(s.Store.Dir(), UploadsFolder)
}

// removeUploads removes the uploaded backup's folder, if there is one.
func (s *Service) removeUploads() error {
	err := os.RemoveAll(s.uploadsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ReceiveUpload unpacks the backup archive of size bytes read from body in
// place of the uploaded backup kept before, and starts its verification.
// It refuses before reading when the disk of the state folder lacks size
// plus UploadRoom, reads at most size bytes, and stops when ctx ends. An
// archive that is not a backup is refused with ErrUploadRefused and leaves
// nothing behind. It fails with state.ErrBackupRunning while a backup runs
// and with ErrBusy while a verification or another upload runs.
func (s *Service) ReceiveUpload(ctx context.Context, body io.Reader, size int64) (Upload, error) {
	if size <= 0 {
		return Upload{}, fmt.Errorf("%w: the upload declared no size", ErrUploadRefused)
	}
	s.mu.Lock()
	err := s.begin(ctx, "upload")
	s.mu.Unlock()
	if err != nil {
		return Upload{}, err
	}
	upload, err := s.unpack(ctx, body, size)
	if err != nil {
		if removeErr := s.removeUploads(); removeErr != nil {
			s.logf("the refused upload could not be removed from %s: %v", s.uploadsPath(), removeErr)
		}
		s.end()
		return Upload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upload = upload
	s.work.Add(1)
	go func(ctx context.Context) {
		defer s.work.Done()
		defer s.end()
		s.finishUpload(ctx, upload)
	}(s.ctx)
	return *upload, nil
}

// unpack replaces the uploaded backup with the archive read from body.
func (s *Service) unpack(ctx context.Context, body io.Reader, size int64) (*Upload, error) {
	dir := s.uploadsPath()
	if err := recovery.CheckFreeSpace(s.Store.Dir(), uint64(size)+UploadRoom); err != nil {
		return nil, fmt.Errorf("%w: %v (the upload leaves 1 GiB free)", ErrUploadRefused, err)
	}
	s.mu.Lock()
	if s.uploadTimer != nil {
		s.uploadTimer.Stop()
	}
	s.upload = nil
	s.mu.Unlock()
	if err := s.removeUploads(); err != nil {
		return nil, fmt.Errorf("remove the backup uploaded before: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	name, err := recovery.UnpackArchive(ctx, io.LimitReader(body, size), dir)
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%w: the upload stopped before it ended", ErrUploadRefused)
	case errors.Is(err, recovery.ErrNotABackupArchive):
		return nil, fmt.Errorf("%w: %v", ErrUploadRefused, err)
	case err != nil:
		return nil, err
	}
	return &Upload{Name: name, Path: filepath.Join(dir, name), Size: size, Status: UploadVerifying, ReceivedAt: s.now()}, nil
}

// finishUpload verifies upload, removes it when it fails, and otherwise
// removes it once UploadKept has passed.
func (s *Service) finishUpload(ctx context.Context, upload *Upload) {
	err := s.verify(ctx, upload.Path)
	if ctx.Err() != nil {
		err = errors.New("OwnGit stopped before the verification finished")
	}
	finished := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	upload.FinishedAt = &finished
	if err != nil {
		upload.Status, upload.Message = UploadFailed, "The uploaded backup did not pass verification, so it was removed: "+err.Error()
		upload.Path = ""
		if removeErr := s.removeUploads(); removeErr != nil {
			upload.Message += " It could not be removed: " + removeErr.Error()
		}
		s.logf("%s", upload.Message)
		return
	}
	removes := upload.ReceivedAt.Add(UploadKept)
	upload.Status, upload.RemovesAt = UploadPassed, &removes
	s.uploadTimer = time.AfterFunc(time.Until(removes), func() { s.expireUpload(upload) })
	s.logf("the uploaded backup %s passed verification", upload.Name)
}

// expireUpload removes upload when it is still the uploaded backup and no
// upload is being received.
func (s *Service) expireUpload(upload *Upload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upload != upload || s.task == "upload" {
		return
	}
	if err := s.removeUploads(); err != nil {
		s.logf("the uploaded backup could not be removed from %s: %v", s.uploadsPath(), err)
		return
	}
	s.upload = nil
}
