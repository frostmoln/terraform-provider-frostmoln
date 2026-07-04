// Package webserver_deployment implements the frostmoln_webserver_deployment
// Terraform resource: an ADR-0091 content deploy to a managed webserver instance.
package webserver_deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// WebserverDeploymentModel is the Terraform state model for a content deploy.
//
// source_archive and source_hash are write-only from the API's perspective — the
// webserver service never echoes the uploaded archive back, so they are only ever
// preserved from plan/state, never populated from a read (mirrors the compute
// instance user_data / user_data_hash pattern). The hash is what drives change
// detection: a new archive (new bytes, hence a new hash) is a new deploy.
type WebserverDeploymentModel struct {
	ID            types.String `tfsdk:"id"`
	InstanceID    types.String `tfsdk:"instance_id"`
	SourceArchive types.String `tfsdk:"source_archive"`
	SourceHash    types.String `tfsdk:"source_hash"`
	DeployID      types.String `tfsdk:"deploy_id"`
	Status        types.String `tfsdk:"status"`
}

// Deploy status values (mirror webserver domain.DeployStatus).
const (
	deployStatusSucceeded = "succeeded"
	deployStatusFailed    = "failed"
)

// apiCreateDeployResponse mirrors webserver domain.CreateDeployResponse: the
// deploy id plus a presigned POST policy (URL + form fields) the client uploads
// the archive with.
type apiCreateDeployResponse struct {
	DeployID     string            `json:"deployId"`
	UploadURL    string            `json:"uploadUrl"`
	UploadFields map[string]string `json:"uploadFields"`
	ExpiresAt    string            `json:"expiresAt"`
	Status       string            `json:"status"`
}

// apiStartDeployRequest mirrors webserver domain.StartDeployRequest: the SHA-256
// of the uploaded archive, which the in-guest agent verifies before unpacking.
type apiStartDeployRequest struct {
	SHA256 string `json:"sha256"`
}

// apiDeploy mirrors webserver domain.Deploy.
type apiDeploy struct {
	ID           string `json:"id"`
	InstanceID   string `json:"instanceId"`
	TenantID     string `json:"tenantId"`
	Status       string `json:"status"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"sizeBytes"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// sha256Hex returns the lowercase hex SHA-256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashArchiveFile streams the file at path and returns its lowercase hex SHA-256
// digest. Streaming (rather than reading the whole archive into memory) keeps the
// plan-time hash cheap even for large site archives.
func hashArchiveFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fromAPI refreshes the deploy-tracking fields (deploy_id, status) from a deploy
// read. It deliberately never touches source_archive, source_hash, instance_id or
// id — the API omits the write-only + create-time attributes, so they are always
// preserved from plan/state. Overwriting them here would trigger Terraform's
// "provider produced inconsistent result after apply" error.
func (m *WebserverDeploymentModel) fromAPI(d *apiDeploy) {
	m.DeployID = types.StringValue(d.ID)
	if d.Status != "" {
		m.Status = types.StringValue(d.Status)
	} else {
		m.Status = types.StringNull()
	}
}
