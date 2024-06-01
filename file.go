package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"
	"fmt"

	"github.com/getsentry/sentry-go"
	"github.com/minio/minio-go/v7"
)

type File struct {
	Id         string `json:"id"`
	Hash       string `json:"hash"`
	Bucket     string `json:"bucket"`
	Filename   string `json:"filename,omitempty"`
	Width      int32  `json:"width,omitempty"`
	Height     int32  `json:"height,omitempty"`
	UploadedBy string `json:"uploaded_by"`
	UploadedAt int64  `json:"uploaded_at"`
	Claimed    bool   `json:"claimed"`
}

func GetFile(id string) (File, error) {
	var f File
	err := db.QueryRow(`SELECT
		id,
		hash,
		bucket,
		filename,
		width,
		height,
		uploaded_by,
		uploaded_at,
		claimed
	FROM files WHERE id=$1`, id).Scan(
		&f.Id,
		&f.Hash,
		&f.Bucket,
		&f.Filename,
		&f.Width,
		&f.Height,
		&f.UploadedBy,
		&f.UploadedAt,
		&f.Claimed,
	)
	return f, err
}

func CreateFile(bucket string, fileBytes []byte, filename string, mime string, uploadedBy string) (File, error) {
	var f File
	var err error

	// Get file hash
	h := sha256.New()
	if _, err = h.Write(fileBytes); err != nil {
		return f, err
	}
	hashHex := hex.EncodeToString(h.Sum(nil))

	// Check block status
	blocked, autoBan, err := getBlockStatus(hashHex)
	if err != nil {
		return f, err
	}
	if blocked {
		if autoBan {
			go banUser(uploadedBy, hashHex)
		}
		return f, ErrFileBlocked
	}

	// Create file ID
	id, err := generateId()
	if err != nil {
		return f, err
	}

	// Create file details
	f = File{
		Id:         id,
		Hash:       hashHex,
		Bucket:     bucket,
		Filename:   cleanFilename(filename),
		UploadedBy: uploadedBy,
		UploadedAt: time.Now().Unix(),
	}

	// Get media dimensions
	width, height, _ := getMediaDimensions(fileBytes)
	f.Width = int32(width)
	f.Height = int32(height)

	// Save file
	if _, err := s3.StatObject(ctx, f.Bucket, f.Hash, minio.GetObjectOptions{}); err != nil {
		// Optimize image if it's an icon
		if bucket == "icons" {
			fileBytes, mime, err = optimizeImage(fileBytes, mime, 256)
			if err != nil {
				return f, err
			}
		}

		// Put object
		if _, err = s3.PutObject(
			ctx,
			f.Bucket,
			f.Hash,
			bytes.NewReader(fileBytes),
			int64(len(fileBytes)),
			minio.PutObjectOptions{
				ContentType: mime,
			},
		); err != nil {
			return f, err
		}
	}

	// Create database row
	if _, err = db.Exec(`INSERT INTO files (
		id,
		hash,
		bucket,
		filename,
		width,
		height,
		uploaded_by,
		uploaded_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		f.Id,
		f.Hash,
		f.Bucket,
		f.Filename,
		f.Width,
		f.Height,
		f.UploadedBy,
		f.UploadedAt,
	); err != nil {
		return f, err
	}

	return f, nil
}

func (f *File) GetObject() (*minio.Object, *minio.ObjectInfo, error) {
	objInfo, err := s3.StatObject(ctx, f.Bucket, f.Hash, minio.StatObjectOptions{})
	if err != nil {
		return nil, nil, err
	}
	obj, err := s3.GetObject(ctx, f.Bucket, f.Hash, minio.GetObjectOptions{})
	if err != nil {
		return nil, nil, err
	}
	return obj, &objInfo, nil
}

func (f *File) GetPreviewObject() (*minio.Object, *minio.ObjectInfo, error) {
	// Get cached preview
	previewObjInfo, err := s3.StatObject(ctx, "attachment-previews", f.Hash, minio.StatObjectOptions{})
	if err == nil {
		previewObj, err := s3.GetObject(ctx, "attachment-previews", f.Hash, minio.GetObjectOptions{})
		return previewObj, &previewObjInfo, err
	} else {
		err = nil
	}

	// Get full object
	obj, objInfo, err := f.GetObject()
	if err != nil {
		return nil, nil, err
	}

	// Make sure the file is compatible
	if f.Bucket != "attachments" || !SupportedImages[objInfo.ContentType] {
		return obj, objInfo, nil // silent fail
	}

	// Optimize image
	imgBytes, err := io.ReadAll(obj)
	if err != nil {
		sentry.CaptureException(err)
		return obj, objInfo, nil // silent fail
	}
	startingWidth, startingHeight, _ := getMediaDimensions(imgBytes)
	sentry.CaptureMessage(fmt.Sprint("before optimizing ", f.Hash, ": ", startingWidth, " x ", startingHeight))
	optimizedImgBytes, newMime, err := optimizeImage(imgBytes, objInfo.ContentType, 1080)
	if err != nil {
		sentry.CaptureException(err)
		return obj, objInfo, nil // silent fail
	}
	endingWidth, endingHeight, _ := getMediaDimensions(optimizedImgBytes)
	sentry.CaptureMessage(fmt.Sprint("after optimizing ", f.Hash, ": ", endingWidth, " x ", endingHeight))

	// Make sure that the optimized image is actually better (sometimes it's not)
	if len(optimizedImgBytes) > len(imgBytes) {
		optimizedImgBytes = imgBytes
		sentry.CaptureMessage(fmt.Sprint("unable to optimize ", f.Hash, ": started with ", len(imgBytes), ", ended up with ", len(optimizedImgBytes)))
	}

	// Cache preview
	_, err = s3.PutObject(
		ctx,
		"attachment-previews",
		f.Hash,
		bytes.NewReader(optimizedImgBytes),
		int64(len(optimizedImgBytes)),
		minio.PutObjectOptions{
			ContentType: newMime,
		},
	)
	if err != nil {
		return nil, nil, err
	}

	// Recursion! (should now pull from cache)
	return f.GetPreviewObject()
}

func (f *File) Claim() error {
	if f.Claimed {
		return ErrFileAlreadyClaimed
	}
	_, err := db.Exec("UPDATE files SET claimed=$1 WHERE id=$2", true, f.Id)
	return err
}

func (f *File) Delete() error {
	// Delete database row
	_, err := db.Exec("DELETE FROM files WHERE id=$1", f.Id)
	if err != nil {
		return err
	}

	// Clean-up object if nothing else is referencing it
	var stillReferenced bool
	err = db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM files WHERE hash=$1 AND bucket=$2
	)`, f.Hash, f.Bucket).Scan(&stillReferenced)
	if err != nil {
		return err
	}
	if !stillReferenced {
		err = s3.RemoveObject(ctx, f.Bucket, f.Hash, minio.RemoveObjectOptions{})
		if err != nil {
			return err
		}

		if f.Bucket == "attachments" {
			s3.RemoveObject(ctx, "attachment-previews", f.Hash, minio.RemoveObjectOptions{})
		}
	}

	return nil
}
