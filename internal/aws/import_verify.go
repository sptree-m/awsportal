package awsapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sptree-m/awsportal/internal/store"
	"io"
	"path"
	"strings"
)

type ArtifactRef struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}
type ImportedFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	VersionID string `json:"version_id"`
}
type ImportManifest struct {
	Version int            `json:"version"`
	Files   []ImportedFile `json:"files"`
}
type ImportValidation struct {
	Version    int            `json:"version"`
	ExitCode   int            `json:"exit_code"`
	ReadErrors int64          `json:"read_errors"`
	Mismatches int64          `json:"mismatches"`
	Files      []ImportedFile `json:"files"`
}
type ImportVerifier struct {
	client *s3.Client
	bucket string
}

func NewImportVerifier(cfg sdk.Config, bucket string) *ImportVerifier {
	return &ImportVerifier{s3.NewFromConfig(cfg), bucket}
}
func (v *ImportVerifier) artifact(ctx context.Context, raw, job string) ([]byte, error) {
	var ref ArtifactRef
	if json.Unmarshal([]byte(raw), &ref) != nil || ref.Bucket != v.bucket || !strings.HasPrefix(ref.Key, ".awsportal-evidence/"+job+"/") || ref.VersionID == "" || len(ref.SHA256) != 64 {
		return nil, invalidEvidence("immutable approved evidence required")
	}
	r, err := v.client.GetObject(ctx, &s3.GetObjectInput{Bucket: sdk.String(ref.Bucket), Key: sdk.String(ref.Key), VersionId: sdk.String(ref.VersionID)})
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024*1024 {
		return nil, invalidEvidence("manifest too large")
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != ref.SHA256 {
		return nil, invalidEvidence("evidence digest mismatch")
	}
	return data, nil
}
func (v *ImportVerifier) Verify(ctx context.Context, j store.ImportJob) (store.ImportEvidence, error) {
	e := store.ImportEvidence{ExpectedManifest: j.ExpectedManifest, Validation: j.Validation, Logs: j.Logs}
	if j.Bucket != v.bucket {
		return e, invalidEvidence("approved import bucket required")
	}
	source, err := v.artifact(ctx, j.ExpectedManifest, j.ID)
	if err != nil {
		return e, err
	}
	validation, err := v.artifact(ctx, j.Validation, j.ID)
	if err != nil {
		return e, err
	}
	if _, err = v.artifact(ctx, j.Logs, j.ID); err != nil {
		return e, err
	}
	var m ImportManifest
	var r ImportValidation
	if json.Unmarshal(source, &m) != nil || json.Unmarshal(validation, &r) != nil || m.Version != 1 || r.Version != 1 {
		return e, invalidEvidence("invalid manifests")
	}
	if len(m.Files) == 0 || len(m.Files) != len(r.Files) || r.ExitCode != 0 || r.ReadErrors != 0 || r.Mismatches != 0 {
		return e, invalidEvidence("source/upload errors")
	}
	expected := map[string]ImportedFile{}
	for _, f := range m.Files {
		if f.Path == "" || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "\\") || strings.HasPrefix(f.Path, "../") || f.Size < 0 || len(f.SHA256) != 64 {
			return e, invalidEvidence("invalid expected path/hash")
		}
		if _, dup := expected[f.Path]; dup {
			return e, invalidEvidence("duplicate expected file")
		}
		expected[f.Path] = f
		e.ExpectedBytes += f.Size
	}
	seen := map[string]bool{}
	for _, f := range r.Files {
		want, ok := expected[f.Path]
		if !ok || seen[f.Path] || f.Size != want.Size || f.SHA256 != want.SHA256 || f.VersionID == "" {
			return e, invalidEvidence("uploaded manifest mismatch")
		}
		seen[f.Path] = true
		object, err := v.client.GetObject(ctx, &s3.GetObjectInput{Bucket: sdk.String(j.Bucket), Key: sdk.String(j.Prefix + f.Path), VersionId: sdk.String(f.VersionID)})
		if err != nil {
			return e, err
		}
		h := sha256.New()
		n, err := io.CopyBuffer(h, object.Body, make([]byte, 4*1024*1024))
		object.Body.Close()
		if err != nil {
			return e, err
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			return e, invalidEvidence("uploaded bytes checksum mismatch")
		}
		e.UploadedBytes += n
	}
	count := 0
	paginator := s3.NewListObjectsV2Paginator(v.client, &s3.ListObjectsV2Input{Bucket: sdk.String(j.Bucket), Prefix: sdk.String(j.Prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return e, err
		}
		for _, object := range page.Contents {
			relative := strings.TrimPrefix(sdk.ToString(object.Key), j.Prefix)
			if _, ok := expected[relative]; !ok {
				return e, invalidEvidence("unexpected destination object")
			}
			count++
		}
	}
	if count != len(expected) {
		return e, invalidEvidence("destination file count mismatch")
	}
	e.ExpectedFiles = int64(len(m.Files))
	e.UploadedFiles = int64(len(r.Files))
	e.ChecksumsVerified = true
	return e, nil
}

type evidenceError struct{ message string }

func (e evidenceError) Error() string   { return e.message }
func (e evidenceError) Permanent() bool { return true }
func invalidEvidence(format string, args ...any) error {
	return evidenceError{fmt.Sprintf(format, args...)}
}
