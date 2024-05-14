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
	grpcAuth "github.com/meower-media-co/Meower-Uploads/grpc_auth"
	"github.com/minio/minio-go/v7"
	"google.golang.org/grpc/metadata"
)

func router(r chi.Router) {
	r.Get("/icons/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Get file
		f, err := GetFile(chi.URLParam(r, "id"))
		if err != nil || f.Bucket != "icons" {
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
		obj, objInfo, err := f.GetObject()
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to get object", http.StatusInternalServerError)
			return
		}

		// Set response headers
		w.Header().Set("Content-Type", objInfo.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(objInfo.Size, 10))
		w.Header().Set("ETag", f.Id)
		w.Header().Set("Cache-Control", "pbulic, max-age=31536000") // 1 year cache (files should never change)
		if r.URL.Query().Has("download") {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%s`, f.Id))
		} else {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%s`, f.Id))
		}

		// Copy the object data into the response body
		_, err = io.Copy(w, obj)
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to send object", http.StatusInternalServerError)
			return
		}
	})

	r.Get("/attachments/{id}/{filename}", func(w http.ResponseWriter, r *http.Request) {
		// Get file
		f, err := GetFile(chi.URLParam(r, "id"))
		if err != nil || f.Bucket != "attachments" || f.Filename != chi.URLParam(r, "filename") {
			if err != nil && err != sql.ErrNoRows && f.Filename == chi.URLParam(r, "filename") {
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
		if r.URL.Query().Has("preview") {
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
		if r.URL.Query().Has("download") {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%s`, f.Filename))
		} else {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%s`, f.Filename))
		}

		// Copy the object data into the response body
		_, err = io.Copy(w, obj)
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to send object", http.StatusInternalServerError)
			return
		}
	})

	r.Get("/data-exports/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Get object info
		objInfo, err := s3.StatObject(ctx, "data-exports", chi.URLParam(r, "id"), minio.StatObjectOptions{})
		if err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Get & check token details
		ctx := metadata.AppendToOutgoingContext(ctx, "x-token", os.Getenv("GRPC_AUTH_TOKEN"))
		tokenDetails, err := grpcAuthClient.CheckToken(ctx, &grpcAuth.CheckTokenReq{
			Token: r.URL.Query().Get("t"),
		})
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to check token", http.StatusInternalServerError)
			return
		}
		if !tokenDetails.Valid || tokenDetails.UserId != objInfo.UserMetadata["user-id"] {
			http.Error(w, "Invalid or missing token", http.StatusUnauthorized)
			return
		}

		// Get object
		obj, err := s3.GetObject(ctx, "data-exports", chi.URLParam(r, "id"), minio.GetObjectOptions{})
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
	})

	r.Post("/{bucket}", func(w http.ResponseWriter, r *http.Request) {
		// Get token details
		ctx := metadata.AppendToOutgoingContext(ctx, "x-token", os.Getenv("GRPC_AUTH_TOKEN"))
		tokenDetails, err := grpcAuthClient.CheckToken(ctx, &grpcAuth.CheckTokenReq{
			Token: r.Header.Get("Authorization"),
		})
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to check token", http.StatusInternalServerError)
			return
		}
		if !tokenDetails.Valid {
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
		if header.Size > map[string]int64{
			"icons":       (5 << 20),
			"attachments": (25 << 20),
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
		f, err := CreateFile(chi.URLParam(r, "bucket"), fileBytes, header.Filename, header.Header.Get("Content-Type"), tokenDetails.UserId)
		if err != nil {
			sentry.CaptureException(err)
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
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
	})
}
