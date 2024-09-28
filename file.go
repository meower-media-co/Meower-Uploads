package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/discord/lilliput"
	"github.com/getsentry/sentry-go"
	"github.com/minio/minio-go/v7"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type File struct {
	Id           string `bson:"_id" json:"id"`
	Hash         string `bson:"hash" json:"hash"`
	Bucket       string `bson:"bucket" json:"bucket"`
	Mime         string `bson:"mime" json:"mime"`
	Filename     string `bson:"filename,omitempty" json:"filename,omitempty"`
	Width        int    `bson:"width,omitempty" json:"width,omitempty"`
	Height       int    `bson:"height,omitempty" json:"height,omitempty"`
	UploadRegion string `bson:"upload_region" json:"upload_region"`
	UploadedBy   string `bson:"uploaded_by" json:"uploaded_by"`
	UploadedAt   int64  `bson:"uploaded_at" json:"uploaded_at"`
	Claimed      bool   `bson:"claimed,omitempty" json:"claimed"`
}

func GetFile(id string) (File, error) {
	var f File
	err := db.Collection("files").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&f)
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
	blocked, err := getBlockStatus(hashHex)
	if err != nil {
		return f, err
	}
	if blocked {
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

	// Create database item
	if _, err := db.Collection("files").InsertOne(context.TODO(), f); err != nil {
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
	_, err := db.Collection("files").UpdateOne(context.TODO(), bson.M{"_id": f.Id}, bson.M{"$set": bson.M{"claimed": true}})
	return err
}

func (f *File) Delete() error {
	// Delete database row
	if _, err := db.Collection("files").DeleteOne(context.TODO(), bson.M{"_id": f.Id}); err != nil {
		return err
	}

	// Clean-up object if nothing else is referencing it
	opts := options.Count()
	opts.SetLimit(1)
	referencedCount, err := db.Collection("files").CountDocuments(context.TODO(), bson.M{"hash": f.Hash}, opts)
	if err != nil {
		return err
	}
	if referencedCount == 0 {
		for _, s3Client := range s3Clients {
			go s3Client.RemoveObject(ctx, f.Bucket, f.Hash, minio.RemoveObjectOptions{})
			if f.Bucket == "attachments" {
				go s3Client.RemoveObject(ctx, "attachment-previews", f.Hash, minio.RemoveObjectOptions{})
			}
		}
	}

	return nil
}
