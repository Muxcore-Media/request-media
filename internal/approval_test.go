package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"

	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func TestNeedsApproval(t *testing.T) {
	m := NewModule(Config{})
	if m.needsApproval("", false) {
		t.Fatal("empty requestor with require=false should not need approval")
	}
	if m.needsApproval("alice", true) {
		t.Fatal("admin should not need approval when require=false")
	}
	if !m.needsApproval("alice", false) {
		t.Fatal("non-admin should need approval")
	}
	trueVal := true
	m2 := NewModule(Config{RequireApproval: &trueVal})
	if !m2.needsApproval("", true) {
		t.Fatal("require_approval=true should always need approval")
	}
}

func TestRequestMovie_PendingDoesNotQueueAutomation(t *testing.T) {
	trueVal := true
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(), RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	autoStub := &stubAutomation{}
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	moviesStub := &stubMovies{}
	moviesAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, moviesStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.movie":
			return moviesAddr, nil
		case "media.automation":
			return autoAddr, nil
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}

	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "pending" {
		t.Fatalf("status = %q, want pending", resp.GetStatus())
	}
	time.Sleep(50 * time.Millisecond)
	if autoStub.last != nil {
		t.Fatalf("AddToQueue called while pending: %+v", autoStub.last)
	}
	if moviesStub.last != nil {
		t.Fatalf("AddMovie called while pending: %+v", moviesStub.last)
	}
}

func TestFulfillMovie_PassesPosterAndRefreshesMetadata(t *testing.T) {
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(),
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	autoStub := &stubAutomation{}
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	moviesStub := &stubMovies{refreshIDs: []string{}}
	moviesAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, moviesStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.movie", "media.library.movies", "media.library":
			return moviesAddr, nil
		case "media.automation":
			return autoAddr, nil
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}

	_, err := m.RequestMovie(withRequestPoster(context.Background(), "/fight-club.jpg"), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, Overview: "soap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if moviesStub.last == nil {
		t.Fatal("AddMovie not called")
	}
	if moviesStub.last.GetPosterPath() != "/fight-club.jpg" {
		t.Fatalf("poster_path = %q", moviesStub.last.GetPosterPath())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(moviesStub.refreshIDs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(moviesStub.refreshIDs) == 0 {
		t.Fatal("RefreshMetadata not scheduled after request")
	}
}

func TestApproveRequest_QueuesAutomation(t *testing.T) {
	trueVal := true
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(), RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	autoStub := &stubAutomation{}
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	moviesStub := &stubMovies{}
	moviesAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, moviesStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.movie":
			return moviesAddr, nil
		case "media.automation":
			return autoAddr, nil
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}

	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	approve, err := m.ApproveRequest(context.Background(), &requestmedia.ApproveRequestRequest{
		RequestId: resp.GetRequestId(), ApprovedBy: "admin1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if approve.GetError() != "" {
		t.Fatalf("approve error: %s", approve.GetError())
	}
	if approve.GetStatus() != "added" {
		t.Fatalf("status = %q, want added", approve.GetStatus())
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.last == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.last == nil {
		t.Fatal("AddToQueue not called after approve")
	}
	st, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatal(err)
	}
	if st.GetApprovedBy() != "admin1" || st.GetRequestedBy() != "bob" {
		t.Fatalf("approval fields: %+v", st)
	}
}

func TestDenyRequest_StaysDenied(t *testing.T) {
	trueVal := true
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(), RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	autoStub := &stubAutomation{}
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		if capability == "media.automation" {
			return autoAddr, nil
		}
		return "", fmt.Errorf("no %s", capability)
	}

	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	deny, err := m.DenyRequest(context.Background(), &requestmedia.DenyRequestRequest{
		RequestId: resp.GetRequestId(), DeniedBy: "admin1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deny.GetStatus() != "denied" {
		t.Fatalf("status = %q", deny.GetStatus())
	}
	time.Sleep(50 * time.Millisecond)
	if autoStub.last != nil {
		t.Fatal("AddToQueue called after deny")
	}
	st, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatal(err)
	}
	if st.GetStatus() != "denied" {
		t.Fatalf("persisted status = %q", st.GetStatus())
	}
}

func TestListRequests_FilterPending(t *testing.T) {
	trueVal := true
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(), RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	_, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 1, Title: "A", Year: 2000, RequestedBy: "u",
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.ListRequests(context.Background(), &requestmedia.ListRequestsRequest{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetRequests()) != 1 {
		t.Fatalf("len=%d", len(list.GetRequests()))
	}
}
