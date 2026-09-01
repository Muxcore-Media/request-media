package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	musicv1 "github.com/Muxcore-Media/media-music/proto/gen/muxcore/music/v1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func approvalTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("REQUEST_ALLOW_ANONYMOUS", "true")
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
}

func TestNeedsApproval(t *testing.T) {
	m := NewModule(Config{})
	if m.needsApproval("", nil) {
		t.Fatal("empty requestor with require=false should not need approval")
	}
	if m.needsApproval("alice", []string{"admin"}) {
		t.Fatal("admin should not need approval when require=false")
	}
	if !m.needsApproval("alice", []string{"user"}) {
		t.Fatal("non-privileged user should need approval")
	}
	if m.needsApproval("bob", []string{"manager"}) {
		t.Fatal("manager should auto-approve by default")
	}
	trueVal := true
	m2 := NewModule(Config{RequireApproval: &trueVal})
	if !m2.needsApproval("", []string{"admin"}) {
		t.Fatal("require_approval=true should always need approval")
	}
}

func TestRequestMovie_PendingDoesNotQueueAutomation(t *testing.T) {
	approvalTestEnv(t)
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
	if autoStub.lastAdd() != nil {
		t.Fatalf("AddToQueue called while pending: %+v", autoStub.lastAdd())
	}
	if moviesStub.last != nil {
		t.Fatalf("AddMovie called while pending: %+v", moviesStub.last)
	}
}

func TestFulfillMovie_PassesPosterAndRefreshesMetadata(t *testing.T) {
	approvalTestEnv(t)
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
	approvalTestEnv(t)
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
	approve, err := m.ApproveRequest(withRoles(context.Background(), []string{"admin"}), &requestmedia.ApproveRequestRequest{
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
	for autoStub.lastAdd() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.lastAdd() == nil {
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
	approvalTestEnv(t)
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
	deny, err := m.DenyRequest(withRoles(context.Background(), []string{"admin"}), &requestmedia.DenyRequestRequest{
		RequestId: resp.GetRequestId(), DeniedBy: "admin1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deny.GetStatus() != "denied" {
		t.Fatalf("status = %q", deny.GetStatus())
	}
	time.Sleep(50 * time.Millisecond)
	if autoStub.lastAdd() != nil {
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
	approvalTestEnv(t)
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

type stubMusic struct {
	musicv1.UnimplementedMusicManagementServiceServer
}

func (stubMusic) AddArtist(context.Context, *musicv1.AddArtistRequest) (*musicv1.AddArtistResponse, error) {
	return &musicv1.AddArtistResponse{Artist: &musicv1.Artist{Id: "artist-1"}}, nil
}

func (stubMusic) AddAlbum(context.Context, *musicv1.AddAlbumRequest) (*musicv1.AddAlbumResponse, error) {
	return &musicv1.AddAlbumResponse{Album: &musicv1.Album{Id: "album-1"}}, nil
}

func TestCancelRequest_PendingRetract(t *testing.T) {
	approvalTestEnv(t)
	trueVal := true
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: t.TempDir(), RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 42, Title: "Pending", Year: 2020, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := m.CancelRequest(withRoles(context.Background(), []string{"user"}), &requestmedia.CancelRequestRequest{
		RequestId: resp.GetRequestId(), CancelledBy: "bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancel.GetStatus() != "cancelled" || cancel.GetError() != "" {
		t.Fatalf("cancel=%+v", cancel)
	}
	if _, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()}); err == nil {
		t.Fatal("cancelled request should be gone")
	}
}

func TestApproveRequest_PendingAlbumPreservesMBIDsAfterRestart(t *testing.T) {
	approvalTestEnv(t)
	trueVal := true
	dataDir := t.TempDir()
	m := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: dataDir, RequireApproval: &trueVal,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	musicAddr := startGRPC(t, func(s *grpc.Server) {
		musicv1.RegisterMusicManagementServiceServer(s, stubMusic{})
	})
	autoStub := &stubAutomation{}
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	m.findAddr = func(_ context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.music", "media.library":
			return musicAddr, nil
		case "media.automation":
			return autoAddr, nil
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}

	resp, err := m.RequestAlbum(context.Background(), &requestmedia.RequestAlbumRequest{
		ReleaseGroupId: "mbid-rg-keep", ArtistMusicbrainzId: "mbid-artist-keep",
		ArtistName: "Fixture Artist", Title: "Fixture Album", Year: 2020, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != "pending" {
		t.Fatalf("status=%q", resp.GetStatus())
	}
	_ = m.Stop(context.Background())

	m2 := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		DataDir: dataDir, RequireApproval: &trueVal,
	})
	if err := m2.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m2.Stop(context.Background()) })
	m2.findAddr = m.findAddr

	m2.mu.RLock()
	rec := m2.requests[resp.GetRequestId()]
	m2.mu.RUnlock()
	if rec == nil || rec.ReleaseGroupID != "mbid-rg-keep" || rec.MusicBrainzID != "mbid-artist-keep" {
		t.Fatalf("persisted album grain=%+v", rec)
	}

	approve, err := m2.ApproveRequest(withRoles(context.Background(), []string{"admin"}), &requestmedia.ApproveRequestRequest{
		RequestId: resp.GetRequestId(), ApprovedBy: "admin1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if approve.GetError() != "" || approve.GetStatus() != "added" {
		t.Fatalf("approve=%+v", approve)
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.lastAdd() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.lastAdd() == nil {
		t.Fatal("AddToQueue not called after album approve")
	}
}
