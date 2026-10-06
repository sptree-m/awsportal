package awsapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/http"
	"strings"
	"testing"
)

type auditHTTPClient struct {
	t                *testing.T
	payload          []byte
	key, contentType string
}

func (c auditHTTPClient) Do(r *http.Request) (*http.Response, error) {
	c.t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	digest := sha256.Sum256(c.payload)
	if r.Method != "PUT" || r.URL.Path != "/"+c.key || r.Header.Get("Content-Type") != c.contentType || r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(digest[:]) || !bytes.Equal(data, c.payload) {
		c.t.Fatalf("unexpected S3 request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}
func TestAuditJSONLS3Upload(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		payload := []byte("{\"action\":\"login\"}\n")
		key, contentType := "audit/file.jsonl", "application/x-ndjson"
		if compressed {
			var buf bytes.Buffer
			writer := gzip.NewWriter(&buf)
			if _, err := writer.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			payload = buf.Bytes()
			key += ".gz"
			contentType = "application/gzip"
		}
		cfg := sdk.Config{Region: "ap-northeast-1", HTTPClient: auditHTTPClient{t, payload, key, contentType}, Credentials: sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
			return sdk.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		})}
		sink, err := NewUsageS3(cfg, "audit-test-bucket")
		if err != nil {
			t.Fatal(err)
		}
		sink.Client = s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = sdk.String("https://s3.example.test") })
		if err = sink.Put(context.Background(), key, payload); err != nil {
			t.Fatal(err)
		}
	}
}
