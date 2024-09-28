package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/getsentry/sentry-go"
	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
)

func uploadFile(w http.ResponseWriter, r *http.Request) {
	// Get authed user
	user, err := getUserByToken(r.Header.Get("Authorization"))
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Invalid or missing token", http.StatusUnauthorized)
		return
	}

	// Get file from request body
	file, header, err := r.FormFile("file")
	if err != nil {
		if err != http.ErrMissingFile {
			sentry.CaptureException(err)
		}
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Make sure file doesn't exceeed maximum size
	maxIconSizeMib, _ := strconv.ParseInt(os.Getenv("MAX_ICON_SIZE_MIB"), 10, 32)
	maxEmojiSizeMib, _ := strconv.ParseInt(os.Getenv("MAX_EMOJI_SIZE_MIB"), 10, 32)
	maxStickerSizeMib, _ := strconv.ParseInt(os.Getenv("MAX_STICKER_SIZE_MIB"), 10, 32)
	maxAttachmentSizeMib, _ := strconv.ParseInt(os.Getenv("MAX_ATTACHMENT_SIZE_MIB"), 10, 32)
	if header.Size > map[string]int64{
		"icons":       (maxIconSizeMib << 20),
		"emojis":      (maxEmojiSizeMib << 20),
		"stickers":    (maxStickerSizeMib << 20),
		"attachments": (maxAttachmentSizeMib << 20),
	}[chi.URLParam(r, "bucket")] {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Read file
	fileBytes, err := io.ReadAll(file)
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	// Create file
	f, err := CreateFile(chi.URLParam(r, "bucket"), fileBytes, header.Filename, header.Header.Get("Content-Type"), user.Username)
	if err != nil {
		if err == ErrFileBlocked {
			http.Error(w, "File blocked", http.StatusForbidden)
		} else {
			sentry.CaptureException(err)
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
		}
		return
	}

	// Return file details
	encoded, err := json.Marshal(f)
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to send file details", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

func downloadFile(w http.ResponseWriter, r *http.Request) {
	// Get file
	f, err := GetFile(chi.URLParam(r, "id"))
	if err != nil || f.Bucket != chi.URLParam(r, "bucket") {
		if err != nil && err != sql.ErrNoRows {
			sentry.CaptureException(err)
		}
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Caching
	if r.Header.Get("ETag") == f.Id || r.Header.Get("If-None-Match") == f.Id {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Get object
	var obj *minio.Object
	var objInfo *minio.ObjectInfo
	if r.URL.Query().Has("preview") && f.Bucket == "attachments" {
		obj, objInfo, err = f.GetPreviewObject()
	} else {
		obj, objInfo, err = f.GetObject()
	}
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to get object", http.StatusInternalServerError)
		return
	} else {
		obj.Seek(0, 0)
	}

	// Set response headers
	w.Header().Set("Content-Type", objInfo.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(objInfo.Size, 10))
	w.Header().Set("ETag", f.Id)
	w.Header().Set("Cache-Control", "pbulic, max-age=31536000") // 1 year cache (files should never change)
	filename := chi.URLParam(r, "*")
	if filename == "" {
		filename = f.Id
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%s`, filename))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%s`, filename))
	}

	// Copy the object data into the response body
	_, err = io.Copy(w, obj)
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to send object", http.StatusInternalServerError)
		return
	}
}

func downloadDataExport(w http.ResponseWriter, r *http.Request) {
	// Get object info
	objInfo, err := s3Clients[s3RegionOrder[0]].StatObject(ctx, "data-exports", chi.URLParam(r, "id"), minio.StatObjectOptions{})
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Get & check token details
	user, err := getUserByToken(r.URL.Query().Get("t"))
	if err != nil || user.Username != objInfo.UserMetadata["User-Id"] {
		sentry.CaptureException(err)
		http.Error(w, "Invalid or missing token", http.StatusUnauthorized)
		return
	}

	// Get object
	obj, err := s3Clients[s3RegionOrder[0]].GetObject(ctx, "data-exports", chi.URLParam(r, "id"), minio.GetObjectOptions{})
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to get object", http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", objInfo.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(objInfo.Size, 10))
	w.Header().Set("Cache-Control", "none") // do not cache
	w.Header().Set("Content-Disposition", "attachment; filename=meower_export.zip")

	// Copy the object data into the response body
	_, err = io.Copy(w, obj)
	if err != nil {
		sentry.CaptureException(err)
		http.Error(w, "Failed to send object", http.StatusInternalServerError)
		return
	}
}
