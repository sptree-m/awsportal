package awsapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	awsSDK "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type UsageS3 struct {
	Client *s3.Client
	Bucket string
}

func NewUsageS3(cfg awsSDK.Config, bucket string) (*UsageS3, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(bucket) {
		return nil, fmt.Errorf("invalid metrics bucket")
	}
	return &UsageS3{Client: s3.NewFromConfig(cfg), Bucket: bucket}, nil
}
func (s *UsageS3) Put(ctx context.Context, key string, payload []byte) error {
	contentType := "application/json"
	if strings.HasSuffix(key, ".parquet") {
		contentType = "application/vnd.apache.parquet"
	}
	digest := sha256.Sum256(payload)
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: awsSDK.String(s.Bucket), Key: awsSDK.String(key), Body: bytes.NewReader(payload), ContentType: awsSDK.String(contentType), ChecksumSHA256: awsSDK.String(base64.StdEncoding.EncodeToString(digest[:]))})
	return err
}
