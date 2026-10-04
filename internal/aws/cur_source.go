package awsapi

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"reflect"
	"strings"
)

type CURSource struct {
	Version int `json:"version"`
	Files   []struct {
		Key       string `json:"key"`
		VersionID string `json:"version_id"`
	} `json:"files"`
}
type CURReader struct {
	client         *s3.Client
	bucket, prefix string
}

func NewCURReader(cfg sdk.Config, bucket, prefix string) *CURReader {
	return &CURReader{s3.NewFromConfig(cfg), bucket, prefix}
}

// The source manifest freezes all CUR export chunks and their S3 version IDs.
// Source updates require another manifest version; the original run is immutable.
func (c *CURReader) Open(ctx context.Context, key, version string) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(key, c.prefix) || version == "" || version == "null" {
		return nil, "", fmt.Errorf("approved versioned CUR manifest required")
	}
	obj, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: sdk.String(c.bucket), Key: sdk.String(key), VersionId: sdk.String(version)})
	if err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, 1024*1024+1))
	obj.Body.Close()
	if err != nil {
		return nil, "", err
	}
	if len(data) > 1024*1024 {
		return nil, "", fmt.Errorf("CUR manifest too large")
	}
	var source CURSource
	if json.Unmarshal(data, &source) != nil || source.Version != 1 || len(source.Files) == 0 {
		return nil, "", fmt.Errorf("CUR source manifest invalid")
	}
	seen := map[string]bool{}
	for _, f := range source.Files {
		if !strings.HasPrefix(f.Key, c.prefix) || f.VersionID == "" || f.VersionID == "null" || seen[f.Key] {
			return nil, "", fmt.Errorf("CUR chunk version/prefix invalid")
		}
		seen[f.Key] = true
	}
	digest := sha256.Sum256(data)
	identity := c.bucket + "/" + key + "@" + version + "#" + hex.EncodeToString(digest[:])
	reader, writer := io.Pipe()
	go func() {
		var failure error
		defer func() { writer.CloseWithError(failure) }()
		out := csv.NewWriter(writer)
		var header []string
		for _, f := range source.Files {
			object, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: sdk.String(c.bucket), Key: sdk.String(f.Key), VersionId: sdk.String(f.VersionID)})
			if err != nil {
				failure = err
				return
			}
			var input io.Reader = object.Body
			var gz *gzip.Reader
			if strings.HasSuffix(f.Key, ".gz") {
				gz, err = gzip.NewReader(input)
				if err != nil {
					object.Body.Close()
					failure = err
					return
				}
				input = gz
			}
			rows := csv.NewReader(input)
			h, err := rows.Read()
			if err == nil {
				if header == nil {
					header = h
					err = out.Write(h)
				} else if !reflect.DeepEqual(header, h) {
					err = fmt.Errorf("inconsistent CUR shard columns")
				}
			}
			if err != nil {
				object.Body.Close()
				failure = err
				return
			}
			for {
				row, err := rows.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					failure = err
					break
				}
				if err = out.Write(row); err != nil {
					failure = err
					break
				}
				out.Flush()
				if err = out.Error(); err != nil {
					failure = err
					break
				}
			}
			if gz != nil {
				gz.Close()
			}
			object.Body.Close()
			if failure != nil {
				return
			}
		}
		out.Flush()
		failure = out.Error()
	}()
	return reader, identity, nil
}
