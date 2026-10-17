package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
)

// ReadFileTool reads content from the sandbox
type ReadFileTool struct {
	box *sandbox.Sandbox
}

func NewReadFileTool(box *sandbox.Sandbox) *ReadFileTool {
	return &ReadFileTool{box: box}
}

func (r *ReadFileTool) Name() string { return "read_file" }
func (r *ReadFileTool) Description() string {
	return "Reads the entire content of a file within the sandbox directory."
}
func (r *ReadFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Relative path to file inside sandbox",
			},
		},
		"required": []string{"path"},
	}
}

type ReadFileArgs struct {
	Path string `json:"path"`
}

func (r *ReadFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args ReadFileArgs
	if err := ParseArgs(argsJSON, &args); err != nil {
		return "", err
	}
	safePath, err := r.box.ValidatePath(args.Path)
	if err != nil {
		return "", err
	}
	bytes, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return string(bytes), nil
}

// WriteFileTool writes content into a file within the sandbox
type WriteFileTool struct {
	box *sandbox.Sandbox
}

func NewWriteFileTool(box *sandbox.Sandbox) *WriteFileTool {
	return &WriteFileTool{box: box}
}

func (w *WriteFileTool) Name() string { return "write_file" }
func (w *WriteFileTool) Description() string {
	return "Writes or overwrites text into a file within the sandbox directory."
}
func (w *WriteFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Relative path to file inside sandbox",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Text content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

type WriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (w *WriteFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args WriteFileArgs
	if err := ParseArgs(argsJSON, &args); err != nil {
		return "", err
	}
	safePath, err := w.box.ValidatePath(args.Path)
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(filepath.Dir(safePath), 0755)
	if err := os.WriteFile(safePath, []byte(args.Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path), nil
}
