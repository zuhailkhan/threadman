package antigravity

import (
	"context"
	"errors"
	"os/exec"

	"github.com/zuhailkhan/threadman/internal/domain"
)

var errReadOnly = errors.New("antigravity conversations are read-only: their storage format is undocumented")

// Writer only knows how to reopen a conversation in the Antigravity CLI.
// threadman cannot create or delete Antigravity conversations.
type Writer struct{}

func NewWriter() *Writer { return &Writer{} }

func (w *Writer) Name() string { return providerName }

// ReadOnly keeps Antigravity out of the handoff targets.
func (w *Writer) ReadOnly() bool { return true }

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	return "", errReadOnly
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("agy", "--conversation", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return errReadOnly
}
