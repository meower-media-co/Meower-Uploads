package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/discord/lilliput"
	"github.com/getsentry/sentry-go"
	"github.com/minio/minio-go/v7"
)

type File struct {
	Id           string `json:"id"`
	Hash         string `json:"hash"`
	Bucket       string `json:"bucket"`
	Mime         string `json:"mime"`
	Filename     string `json:"filename,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	UploadRegion string `json:"upload_region"`
	UploadedBy   string `json:"uploaded_by"`
	UploadedAt   int64  `json:"uploaded_at"`
	Claimed      bool   `json:"claimed"`
}

func GetFile(id string) (File, error) {
	var f File
	err := db.QueryRow(`SELECT
		id,
		hash,
		bucket,
		mime,
		filename,
		width,
		height,
		upload_region,
		uploaded_by,
		uploaded_at,
		claimed
	FROM files WHERE id=$1`, id).Scan(
		&f.Id,
		&f.Hash,
		&f.Bucket,
		&f.Mime,
		&f.Filename,
		&f.Width,
		&f.Height,
		&f.UploadRegion,
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
	if _, err = h.Write([]byte(mime)); err != nil {
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
		Id:           id,
		Hash:         hashHex,
		Bucket:       bucket,
		Mime:         mime,
		Filename:     cleanFilename(filename),
		UploadRegion: s3RegionOrder[0],
		UploadedBy:   uploadedBy,
		UploadedAt:   time.Now().Unix(),
	}

	// Get media dimensions
	lilliputDecoder, err := lilliput.NewDecoder(fileBytes)
	if err == nil {
		f.Width, f.Height, _ = getMediaDimensions(lilliputDecoder)
	}

	// Save file
	if _, err := s3Clients[s3RegionOrder[0]].StatObject(ctx, f.Bucket, f.Hash, minio.GetObjectOptions{}); err != nil {
		// Optimization
		if bucket == "icons" {
			fileBytes, mime, err = optimizeImage(fileBytes, mime, 256)
			if err != nil {
				return f, err
			}
		} else if bucket == "emojis" {
			fileBytes, mime, err = optimizeImage(fileBytes, mime, 128)
			if err != nil {
				return f, err
			}
		} else if bucket == "stickers" {
			fileBytes, mime, err = optimizeImage(fileBytes, mime, 384)
			if err != nil {
				return f, err
			}
		}

		// Put object
		if _, err = s3Clients[s3RegionOrder[0]].PutObject(
			ctx,
			f.Bucket,
			f.Hash,
			bytes.NewReader(fileBytes),
			int64(len(fileBytes)),
			minio.PutObjectOptions{
				ContentType: mime,
			},
		); err != nil {
			log.Println(err)
			return f, err
		}
	}

	// Start loading preview
	go f.GetPreviewObject()

	// Create database row
	if _, err = db.Exec(`INSERT INTO files (
		id,
		hash,
		bucket,
		mime,
		filename,
		width,
		height,
		upload_region,
		uploaded_by,
		uploaded_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		f.Id,
		f.Hash,
		f.Bucket,
		f.Mime,
		f.Filename,
		f.Width,
		f.Height,
		f.UploadRegion,
		f.UploadedBy,
		f.UploadedAt,
	); err != nil {
		return f, err
	}

	sentry.CaptureMessage(fmt.Sprintf("Uploaded file %s with hash %s to %s region", f.Id, f.Hash, f.UploadRegion))

	return f, nil
}

func (f *File) GetObject() (*minio.Object, *minio.ObjectInfo, error) {
	var objInfo minio.ObjectInfo
	var err error

	// Attempt getting object locally
	objInfo, err = s3Clients[s3RegionOrder[0]].StatObject(ctx, f.Bucket, f.Hash, minio.StatObjectOptions{})
	if err == nil {
		obj, err := s3Clients[s3RegionOrder[0]].GetObject(ctx, f.Bucket, f.Hash, minio.GetObjectOptions{})
		if err != nil {
			return nil, nil, err
		}
		sentry.CaptureMessage(fmt.Sprintf("Got file %s locally within region %s", f.Id, s3RegionOrder[0]))
		return obj, &objInfo, nil
	}

	// Otherwise, go to the upload region
	objInfo, err = s3Clients[f.UploadRegion].StatObject(ctx, f.Bucket, f.Hash, minio.StatObjectOptions{})
	if err == nil {
		obj, err := s3Clients[f.UploadRegion].GetObject(ctx, f.Bucket, f.Hash, minio.GetObjectOptions{})
		if err != nil {
			return nil, nil, err
		}
		sentry.CaptureMessage(fmt.Sprintf("Got file %s remotely from region %s", f.Id, f.UploadRegion))
		return obj, &objInfo, nil
	}

	return nil, nil, err
}

func (f *File) GetPreviewObject() (*minio.Object, *minio.ObjectInfo, error) {
	// Get cached preview
	previewObjInfo, err := s3Clients[s3RegionOrder[0]].StatObject(ctx, "attachment-previews", f.Hash, minio.StatObjectOptions{})
	if err == nil {
		previewObj, err := s3Clients[s3RegionOrder[0]].GetObject(ctx, "attachment-previews", f.Hash, minio.GetObjectOptions{})
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
	if f.Bucket != "attachments" || !SupportedImages[objInfo.ContentType] || objInfo.Size > 10<<20 {
		return obj, objInfo, nil // silent fail
	}

	// Optimize image
	imgBytes, err := io.ReadAll(obj)
	if err != nil {
		sentry.CaptureException(err)
		return obj, objInfo, nil // silent fail
	}
	optimizedImgBytes, newMime, err := optimizeImage(imgBytes, objInfo.ContentType, 720)
	if err != nil {
		sentry.CaptureException(err)
		return obj, objInfo, nil // silent fail
	}

	// Make sure that the optimized image is actually better (sometimes it's not)
	if len(optimizedImgBytes) > len(imgBytes) {
		optimizedImgBytes = imgBytes
	}

	// Cache preview
	_, err = s3Clients[s3RegionOrder[0]].PutObject(
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
		for _, s3Client := range s3Clients {
			go s3Client.RemoveObject(ctx, f.Bucket, f.Hash, minio.RemoveObjectOptions{})
			if f.Bucket == "attachments" {
				go s3Client.RemoveObject(ctx, "attachment-previews", f.Hash, minio.RemoveObjectOptions{})
			}
		}
	}

	return nil
}
