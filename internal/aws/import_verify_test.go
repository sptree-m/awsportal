package awsapi

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
)

func TestImportEvidenceRejectsUnversionedObjectWithoutAWSCall(t *testing.T) {
	v := NewImportVerifier(sdk.Config{Region: "us-east-1"}, "approved-bucket")
	for _, version := range []string{"", "null"} {
		ref, _ := json.Marshal(ArtifactRef{Bucket: "approved-bucket", Key: ".awsportal-evidence/job/expected.json", VersionID: version, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
		_, err := v.artifact(context.Background(), string(ref), "job")
		if permanent, ok := err.(interface{ Permanent() bool }); !ok || !permanent.Permanent() {
			t.Fatal("unversioned evidence was accepted", version, err)
		}
	}
}
