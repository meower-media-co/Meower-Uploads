package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/getsentry/sentry-go"
	pb "github.com/meower-media-co/Meower-Uploads/grpc_uploads"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type grpcUploadsServer struct {
	pb.UnimplementedUploadsServer
}

func (s grpcUploadsServer) ClaimFile(ctx context.Context, req *pb.ClaimFileReq) (*pb.ClaimFileResp, error) {
	// Check token
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-token")) == 0 || md.Get("x-token")[0] != os.Getenv("GRPC_UPLOADS_TOKEN") {
		return nil, ErrUnauthorized
	}

	// Get file
	f, err := GetFile(req.Id)
	if err != nil {
		if err != sql.ErrNoRows {
			sentry.CaptureException(err)
		}
		return nil, err
	}

	// Check bucket
	if f.Bucket != req.Bucket {
		return nil, ErrMismatchedBucket
	}

	// Claim file
	err = f.Claim()
	if err != nil {
		if err != ErrFileAlreadyClaimed {
			sentry.CaptureException(err)
		}
		return nil, err
	}

	// Get object info
	_, objInfo, err := f.GetObject()
	if err != nil {
		sentry.CaptureException(err)
		return nil, err
	}

	// Return file details
	return &pb.ClaimFileResp{
		Id:       f.Id,
		Mime:     objInfo.ContentType,
		Filename: f.Filename,
		Size:     objInfo.Size,
		Width:    f.Width,
		Height:   f.Height,
	}, nil
}

func (s grpcUploadsServer) DeleteFile(ctx context.Context, req *pb.DeleteFileReq) (*emptypb.Empty, error) {
	// Check token
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-token")) == 0 || md.Get("x-token")[0] != os.Getenv("GRPC_UPLOADS_TOKEN") {
		return nil, ErrUnauthorized
	}

	// Get file
	f, err := GetFile(req.Id)
	if err != nil {
		if err != sql.ErrNoRows {
			sentry.CaptureException(err)
		}
		return nil, err
	}

	// Delete file
	if err := f.Delete(); err != nil {
		sentry.CaptureException(err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (s grpcUploadsServer) ClearFiles(ctx context.Context, req *pb.ClearFilesReq) (*emptypb.Empty, error) {
	// Check token
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-token")) == 0 || md.Get("x-token")[0] != os.Getenv("GRPC_UPLOADS_TOKEN") {
		return nil, ErrUnauthorized
	}

	// Mark files as unclaimed
	// Files will be deleted when the cleanup thread runs
	_, err := db.Exec("UPDATE files SET claimed=false WHERE uploaded_by=$1", req.UserId)
	return &emptypb.Empty{}, err
}
