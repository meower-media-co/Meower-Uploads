package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/discord/lilliput"
	"github.com/vmihailenco/msgpack/v5"
)

var SupportedImages = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

var (
	ErrUnsupportedFile    = errors.New("unsupported file")
	ErrFileBlocked        = errors.New("file blocked")
	ErrFileAlreadyClaimed = errors.New("file already claimed")
	ErrMismatchedBucket   = errors.New("mismatched bucket")
	ErrUnauthorized       = errors.New("unauthorized")
)

func generateId() (string, error) {
	// Generate bytes
	b := make([]byte, 18)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	// Construct ID
	id := base64.URLEncoding.EncodeToString(b)
	id = strings.ReplaceAll(id, "-", "a")
	id = strings.ReplaceAll(id, "_", "b")
	id = strings.ReplaceAll(id, "=", "c")
	return id, err
}

func cleanFilename(filename string) string {
	re := regexp.MustCompile(`[^A-Za-z0-9\.\-\_\+\!\(\)$]`)
	return re.ReplaceAllString(filename, "_")
}

func optimizeImage(imageBytes []byte, mime string, maxSize int) ([]byte, string, error) {
	// Get file extension
	fileExt := map[string]string{
		"image/png":  ".webp",
		"image/jpeg": ".webp",
		"image/webp": ".webp",
		"image/gif":  ".gif",
	}[mime]
	if fileExt == "" {
		return nil, "", ErrUnsupportedFile
	}

	// Get lilliput decoder
	lilliputDecoder, err := lilliput.NewDecoder(imageBytes)
	if err != nil {
		return nil, "", err
	}
	defer lilliputDecoder.Close()

	// Original image dimensions
	originalWidth, originalHeight, err := getMediaDimensions(lilliputDecoder)
	if err != nil {
		return nil, "", err
	}

	// Calculate aspect ratio of the original image
	aspectRatio := float64(originalWidth) / float64(originalHeight)

	// Calculate new dimensions based on max width constraint
	newWidth := maxSize
	newHeight := int(float64(maxSize) / aspectRatio)

	// Shrink height if still too high
	if newHeight > maxSize {
		aspectRatio = float64(newHeight) / float64(newWidth)
		newHeight = maxSize
		newWidth = int(float64(newWidth) / aspectRatio)
	}

	// Create lilliput options
	lilliputOpts := lilliput.ImageOptions{
		FileType:     fileExt,
		Width:        newWidth,
		Height:       newHeight,
		ResizeMethod: lilliput.ImageOpsResize,
	}

	// Create ops
	lilliputOps := lilliput.NewImageOps(8192)
	defer lilliputOps.Close()

	// Transform image
	newImageBytes, err := lilliputOps.Transform(lilliputDecoder, &lilliputOpts, make([]byte, len(imageBytes)*2))
	newMime := map[string]string{
		".webp": "image/webp",
		".gif":  "image/gif",
	}[fileExt]
	return newImageBytes, newMime, err
}

// returns width x height
func getMediaDimensions(lilliputDecoder lilliput.Decoder) (int, int, error) {
	// Get lilliput header
	lilliputHeader, err := lilliputDecoder.Header()
	if err != nil {
		return 0, 0, err
	}

	// Return width x height
	// Anything above orientation 4, we swap width and height.
	// Since anything above orientation 4 does rotations which swaps the width
	// and height for some reason.
	if lilliputHeader.Orientation() > 4 {
		return lilliputHeader.Height(), lilliputHeader.Width(), nil
	} else {
		return lilliputHeader.Width(), lilliputHeader.Height(), nil
	}
}

// Delete unclaimed files that are more than 10 minutes old
func cleanupFiles() error {
	// Get file IDs
	rows, err := db.Query("SELECT id FROM files WHERE claimed = false AND uploaded_at < $1", time.Now().Unix()-600)
	if err != nil {
		return err
	}
	defer rows.Close()
	fileIds := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		fileIds = append(fileIds, id)
	}

	// Delete files
	for _, id := range fileIds {
		// Get full file
		f, err := GetFile(id)
		if err != nil {
			return err
		}

		// Delete file
		if err = f.Delete(); err != nil {
			return err
		}
	}

	return nil
}

// Get the block status of a file by its hash.
// Returns whether it's blocked and whether to auto-ban the uploader.
func getBlockStatus(hashHex string) (bool, bool, error) {
	var autoBan bool
	err := db.QueryRow("SELECT auto_ban FROM blocked WHERE hash=$1", hashHex).Scan(&autoBan)
	if err == sql.ErrNoRows {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	} else {
		return true, autoBan, nil
	}
}

// Send a request to the main server to ban a user by their username for
// uploading a blocked file.
func banUser(username string, fileHash string) error {
	marshaledEvent, err := msgpack.Marshal(map[string]string{
		"op":     "ban_user",
		"user":   username,
		"state":  "perm_ban",
		"reason": "",
		"note":   "Automatically banned by the uploads server for uploading a file that was blocked and set to auto-ban the uploader.\nFile hash: " + fileHash,
	})
	if err != nil {
		return err
	}
	err = rdb.Publish(ctx, "admin", marshaledEvent).Err()
	return err
}
