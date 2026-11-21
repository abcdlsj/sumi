package domain

// WorkspaceFileNode describes a single file, directory, or symlink entry inside an agent's workspace.
type WorkspaceFileNode struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
	Size      int64  `json:"size"`
	MtimeMs   int64  `json:"mtime_ms"`
}

// WorkspaceListing is a single directory listing inside an agent's workspace.
type WorkspaceListing struct {
	RootPath string              `json:"root_path"`
	Path     string              `json:"path"`
	Entries  []WorkspaceFileNode `json:"entries"`
}

// WorkspaceFileContent is the read-only content of a file inside an agent's workspace.
type WorkspaceFileContent struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MtimeMs  int64  `json:"mtime_ms"`
	Binary   bool   `json:"binary"`
	MimeType string `json:"mime_type,omitempty"`
	Encoding string `json:"encoding,omitempty"` // "utf8" | "base64"
	Content  string `json:"content"`
}
