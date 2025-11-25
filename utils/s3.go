package utils

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// getS3Config returns AWS config for S3
func getS3Config() (aws.Config, error) {
	region := os.Getenv("S3_REGION")
	if region == "" {
		region = "ap-southeast-1" // default Singapore
	}

	accessKey := os.Getenv("S3_ACCESS_KEY")
	secretKey := os.Getenv("S3_SECRET_KEY")

	if accessKey == "" || secretKey == "" {
		return aws.Config{}, fmt.Errorf("S3_ACCESS_KEY or S3_SECRET_KEY missing")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cfg, nil
}

// UploadToS3 uploads a file to AWS S3
func UploadToS3(objectName string, file io.Reader, fileSize int64) error {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return fmt.Errorf("S3_BUCKET not set in environment")
	}

	cfg, err := getS3Config()
	if err != nil {
		return err
	}

	client := s3.NewFromConfig(cfg)

	contentType := mime.TypeByExtension(path.Ext(objectName))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(objectName),
		Body:        file,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("S3 upload failed: %w", err)
	}

	return nil
}

// GenerateSignedURL returns a presigned GET URL for the given object
func GenerateSignedURL(objectName string, expirySeconds int64) (string, error) {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return "", fmt.Errorf("S3_BUCKET not set in environment")
	}

	cfg, err := getS3Config()
	if err != nil {
		return "", err
	}

	client := s3.NewFromConfig(cfg)
	presigner := s3.NewPresignClient(client)

	presigned, err := presigner.PresignGetObject(context.TODO(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(objectName),
		},
		func(po *s3.PresignOptions) {
			po.Expires = time.Duration(expirySeconds) * time.Second
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to presign S3 URL: %w", err)
	}

	return presigned.URL, nil
}

// UploadToS3AndPresign uploads file and returns presigned URL
func UploadToS3AndPresign(objectName string, file io.ReadSeeker, fileSize int64, expirySeconds int64) (string, error) {
	// Upload to S3
	if err := UploadToS3(objectName, file, fileSize); err != nil {
		return "", err
	}

	// Generate presigned URL
	url, err := GenerateSignedURL(objectName, expirySeconds)
	if err != nil {
		return "", err
	}

	return url, nil
}

// DeleteFromS3 deletes a file from AWS S3
func DeleteFromS3(objectName string) error {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return fmt.Errorf("S3_BUCKET not set in environment")
	}

	cfg, err := getS3Config()
	if err != nil {
		return err
	}

	client := s3.NewFromConfig(cfg)

	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("S3 delete failed: %w", err)
	}

	return nil
}