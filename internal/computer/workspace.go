package computer

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/abcdlsj/sumi/internal/domain"
)

const (
	maxWorkspaceTextBytes  = 1 << 20 // 1 MiB
	maxWorkspaceImageBytes = 5 << 20 // 5 MiB
)

var alwaysHiddenDirs = map[string]struct{}{
	".aws":   {},
	".gnupg": {},
	".ssh":   {},
	".sumi":  {},
}

var ignoredWorkspaceDirs = map[string]struct{}{
	"node_modules": {},
}

var workspaceImageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
}

func isSecretWorkspaceFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return true
	}
	if strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "token") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") {
		return true
	}
	return false
}

// normalizeWorkspacePath validates and splits a relative workspace path.
// Rejects NUL bytes, absolute paths, backslashes, parent traversal, and sensitive hidden dirs.
func normalizeWorkspacePath(raw string) ([]string, string, error) {
	if strings.ContainsRune(raw, 0) {
		return nil, "", domain.NewError(domain.CodeInvalidArgument, "invalid path")
	}
	if strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") {
		return nil, "", domain.NewError(domain.CodeInvalidArgument, "path must be relative to workspace")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." {
		return nil, "", nil
	}
	parts := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return nil, "", domain.NewError(domain.CodeInvalidArgument, "path traversal is not allowed")
		}
		if _, blocked := alwaysHiddenDirs[strings.ToLower(seg)]; blocked {
			return nil, "", domain.NewError(domain.CodePermissionDenied, "restricted directory cannot be accessed")
		}
		segments = append(segments, seg)
	}
	return segments, strings.Join(segments, "/"), nil
}

// resolveWorkspaceRoot returns the canonical existing workspace directory path.
func resolveWorkspaceRoot(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", domain.WrapError(domain.CodeInternal, "resolve workspace root", err)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", domain.NewError(domain.CodeNotFound, "workspace directory does not exist")
		}
		return "", domain.WrapError(domain.CodeInternal, "resolve workspace root", err)
	}
	info, err := os.Lstat(realRoot)
	if err != nil || !info.IsDir() {
		return "", domain.NewError(domain.CodeNotFound, "workspace root is not a directory")
	}
	return realRoot, nil
}

// walkNoSymlink traverses segments starting from rootReal using Lstat on every component
// so symlinks inside the workspace are never followed.
func walkNoSymlink(rootReal string, segments []string) (string, os.FileInfo, error) {
	cur := rootReal
	info, err := os.Lstat(cur)
	if err != nil {
		return "", nil, domain.NewError(domain.CodeNotFound, "workspace directory not found")
	}
	for _, seg := range segments {
		cur = filepath.Join(cur, seg)
		info, err = os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil, domain.NewError(domain.CodeNotFound, "path not found")
			}
			return "", nil, domain.WrapError(domain.CodeInternal, "stat workspace path", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, domain.NewError(domain.CodePermissionDenied, "symlinks cannot be traversed")
		}
	}
	return cur, info, nil
}

func listWorkspaceDir(workDir, relPath string, includeHidden bool) (*domain.WorkspaceListing, error) {
	segments, cleanRel, err := normalizeWorkspacePath(relPath)
	if err != nil {
		return nil, err
	}
	rootReal, err := resolveWorkspaceRoot(workDir)
	if err != nil {
		// If the agent has not created files yet and the workspace root is missing, report an empty root.
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodeNotFound && cleanRel == "" {
			return &domain.WorkspaceListing{
				RootPath: workDir,
				Path:     "",
				Entries:  []domain.WorkspaceFileNode{},
			}, nil
		}
		return nil, err
	}
	targetPath, preInfo, err := walkNoSymlink(rootReal, segments)
	if err != nil {
		return nil, err
	}
	if !preInfo.IsDir() {
		return nil, domain.NewError(domain.CodeInvalidArgument, "path is not a directory")
	}

	dirEntries, err := os.ReadDir(targetPath)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "read workspace directory", err)
	}

	postInfo, err := os.Lstat(targetPath)
	if err != nil || !os.SameFile(preInfo, postInfo) {
		return nil, domain.NewError(domain.CodeConflict, "workspace directory changed during read")
	}

	entries := make([]domain.WorkspaceFileNode, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		lower := strings.ToLower(name)
		if _, skip := ignoredWorkspaceDirs[lower]; skip {
			continue
		}
		if _, blocked := alwaysHiddenDirs[lower]; blocked {
			continue
		}
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		childAbs := filepath.Join(targetPath, name)
		childInfo, err := os.Lstat(childAbs)
		if err != nil {
			continue
		}
		childRel := name
		if cleanRel != "" {
			childRel = path.Join(cleanRel, name)
		}
		if childInfo.Mode()&os.ModeSymlink != 0 {
			entries = append(entries, domain.WorkspaceFileNode{
				Name:      name,
				Path:      childRel,
				IsDir:     false,
				IsSymlink: true,
				Size:      0,
				MtimeMs:   childInfo.ModTime().UnixMilli(),
			})
			continue
		}
		size := childInfo.Size()
		if childInfo.IsDir() {
			size = 0
		}
		entries = append(entries, domain.WorkspaceFileNode{
			Name:    name,
			Path:    childRel,
			IsDir:   childInfo.IsDir(),
			Size:    size,
			MtimeMs: childInfo.ModTime().UnixMilli(),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		li, lj := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if li != lj {
			return li < lj
		}
		return entries[i].Name < entries[j].Name
	})

	return &domain.WorkspaceListing{
		RootPath: rootReal,
		Path:     cleanRel,
		Entries:  entries,
	}, nil
}

func readWorkspaceFile(workDir, relPath string) (*domain.WorkspaceFileContent, error) {
	segments, cleanRel, err := normalizeWorkspacePath(relPath)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, domain.NewError(domain.CodeInvalidArgument, "file path is required")
	}
	baseName := segments[len(segments)-1]
	if isSecretWorkspaceFile(baseName) {
		return nil, domain.NewError(domain.CodePermissionDenied, "sensitive files cannot be viewed")
	}

	rootReal, err := resolveWorkspaceRoot(workDir)
	if err != nil {
		return nil, err
	}
	targetPath, preInfo, err := walkNoSymlink(rootReal, segments)
	if err != nil {
		return nil, err
	}
	if preInfo.IsDir() {
		return nil, domain.NewError(domain.CodeInvalidArgument, "cannot read a directory")
	}
	if !preInfo.Mode().IsRegular() {
		return nil, domain.NewError(domain.CodePermissionDenied, "only regular files can be viewed")
	}

	// Open with O_NOFOLLOW | O_NONBLOCK to prevent symlink races and FIFO blocking.
	fd, err := syscall.Open(targetPath, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, domain.NewError(domain.CodePermissionDenied, "symlinks cannot be read")
		}
		if errors.Is(err, syscall.ENOENT) {
			return nil, domain.NewError(domain.CodeNotFound, "file not found")
		}
		return nil, domain.WrapError(domain.CodeInternal, "open workspace file", err)
	}
	f := os.NewFile(uintptr(fd), targetPath)
	defer f.Close()

	fInfo, err := f.Stat()
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "stat open workspace file", err)
	}
	if !fInfo.Mode().IsRegular() || !os.SameFile(preInfo, fInfo) {
		return nil, domain.NewError(domain.CodePermissionDenied, "file changed or is not a regular file")
	}
	// Reject hard links to prevent exposing files linked from outside the workspace.
	if st, ok := fInfo.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return nil, domain.NewError(domain.CodePermissionDenied, "hard-linked files cannot be viewed")
	}

	ext := strings.ToLower(path.Ext(baseName))
	mimeType, isImage := workspaceImageMIME[ext]
	limit := int64(maxWorkspaceTextBytes)
	if isImage {
		limit = int64(maxWorkspaceImageBytes)
	}
	if fInfo.Size() > limit {
		return nil, domain.NewError(domain.CodeInvalidArgument, "file exceeds viewer size limit")
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "read workspace file", err)
	}
	if int64(len(data)) > limit {
		return nil, domain.NewError(domain.CodeInvalidArgument, "file exceeds viewer size limit")
	}

	postInfo, err := os.Lstat(targetPath)
	if err != nil || !os.SameFile(fInfo, postInfo) {
		return nil, domain.NewError(domain.CodeConflict, "file changed during read")
	}

	res := &domain.WorkspaceFileContent{
		Path:    cleanRel,
		Size:    int64(len(data)),
		MtimeMs: fInfo.ModTime().UnixMilli(),
	}
	if isImage {
		res.Binary = false
		res.MimeType = mimeType
		res.Encoding = "base64"
		res.Content = base64.StdEncoding.EncodeToString(data)
		return res, nil
	}
	sampleLen := len(data)
	if sampleLen > 8192 {
		sampleLen = 8192
	}
	if bytes.IndexByte(data[:sampleLen], 0) >= 0 || !utf8.Valid(data) {
		res.Binary = true
		res.Content = ""
		return res, nil
	}
	res.Binary = false
	res.Encoding = "utf8"
	res.Content = string(data)
	return res, nil
}
