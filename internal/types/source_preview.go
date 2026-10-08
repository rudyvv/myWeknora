package types

// SourceRepository is a branch resolved by the authenticated GitLab metadata API.
// Transport credentials are deliberately excluded from serialization.
type SourceRepository struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
	CloneURL  string `json:"-"`
	Token     string `json:"-"`
}

type SourcePreviewFile struct {
	Path      string `json:"path"`
	BlobSHA   string `json:"blob_sha"`
	Size      int64  `json:"size"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Encoding  string `json:"encoding,omitempty"`
	Generated bool   `json:"generated"`
}

type SourcePreviewCheck struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

// SourcePreview inventories a fixed commit without creating knowledge or cards.
type SourcePreview struct {
	Projects     []*SourcePreview     `json:"projects,omitempty"`
	ProjectID    string               `json:"project_id"`
	Branch       string               `json:"branch"`
	CommitSHA    string               `json:"commit_sha"`
	RulesVersion string               `json:"rules_version"`
	Files        []SourcePreviewFile  `json:"files"`
	Checks       []SourcePreviewCheck `json:"checks"`
	Warnings     []string             `json:"warnings"`
	CanSync      bool                 `json:"can_sync"`
}
