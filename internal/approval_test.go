package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	"github.com/Muxcore-Media/request-media/internal/authz"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func allowAllAuthz() *authz.Checker {
	return authz.New(authz.Config{
		Can: func(context.Context, string, string, string) (bool, error) { return true, nil },
	})
}

// adminOnlyAuthz allows everything, but only admin-1 may approve; ordinary
// requesters therefore go through the pending queue.
func adminOnlyAuthz() *authz.Checker {
	return authz.New(authz.Config{
		Can: func(_ context.Context, userID, action, _ string) (bool, error) {
			if action == authz.ActionApprove {
				return userID == "admin-1", nil
			}
			return true, nil
		},
	})
}

func denyApproveAuthz() *authz.Checker {
	return authz.New(authz.Config{
		Can: func(_ context.Context, _ string, action, _ string) (bool, error) {
			switch action {
			case authz.ActionCreate, authz.ActionList, authz.ActionDeny, authz.ActionWatchlist:
				return true, nil
			case authz.ActionApprove:
				return false, nil
			default:
				return false, nil
			}
		},
	})
}

func authCtx(userID string) context.Context {
	return authz.WithCallerID(context.Background(), userID)
}

func testModuleLegacy(t *testing.T) *Module {
	t.Helper()
	noApproval := false
	m := NewModule(Config{
		ID:              "request-media-test",
		GRPCAddr:        "127.0.0.1:0",
		HTTPAddr:        "127.0.0.1:0",
		DataDir:         t.TempDir(),
		RequireApproval: &noApproval,
		Authz:           allowAllAuthz(),
		Identity:        testResolver{},
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

func testModuleWithApproval(t *testing.T, checker *authz.Checker) *Module {
	t.Helper()
	requireApproval := true
	m := NewModule(Config{
		ID:              "request-media-test",
		GRPCAddr:        "127.0.0.1:0",
		HTTPAddr:        "127.0.0.1:0",
		DataDir:         t.TempDir(),
		RequireApproval: &requireApproval,
		Authz:           checker,
		Identity:        testResolver{},
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

func TestRequestMovie_PendingWhenApprovalRequired(t *testing.T) {
	m := testModuleWithApproval(t, denyApproveAuthz())
	resp, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != StatusPending {
		t.Fatalf("status = %q, want pending", resp.GetStatus())
	}
	st, err := m.GetStatus(authCtx("user-1"), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetRequestedBy() != "user-1" || st.GetStatus() != StatusPending {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestApproveRequest_QueuesAutomation(t *testing.T) {
	m := testModuleWithApproval(t, adminOnlyAuthz())
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

	resp, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}

	approveResp, err := m.ApproveRequest(authCtx("admin-1"), &requestmedia.ApproveRequestRequest{
		RequestId: resp.GetRequestId(),
	})
	if err != nil {
		t.Fatalf("ApproveRequest: %v", err)
	}
	if approveResp.GetStatus() != StatusRequested {
		t.Fatalf("approve status = %q, want requested", approveResp.GetStatus())
	}

	deadline := time.Now().Add(2 * time.Second)
	for autoStub.lastReq() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := autoStub.lastReq()
	if got == nil {
		t.Fatal("AddToQueue not called after approve")
	}
	if got.GetItemId() != "tmdb_218" {
		t.Fatalf("AddToQueue = %+v", got)
	}
}

func TestDenyRequest_SetsReason(t *testing.T) {
	m := testModuleWithApproval(t, adminOnlyAuthz())
	resp, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	denyResp, err := m.DenyRequest(authCtx("admin-1"), &requestmedia.DenyRequestRequest{
		RequestId: resp.GetRequestId(),
		Reason:    "already in library",
	})
	if err != nil {
		t.Fatalf("DenyRequest: %v", err)
	}
	if denyResp.GetStatus() != StatusDenied || denyResp.GetDenyReason() != "already in library" {
		t.Fatalf("deny resp = %+v", denyResp)
	}
	st, err := m.GetStatus(authCtx("admin-1"), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetStatus() != StatusDenied || st.GetDenyReason() != "already in library" {
		t.Fatalf("status after deny = %+v", st)
	}
}

func TestApproveRequest_PermissionDenied(t *testing.T) {
	m := testModuleWithApproval(t, denyApproveAuthz())
	resp, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	_, err = m.ApproveRequest(authCtx("user-2"), &requestmedia.ApproveRequestRequest{
		RequestId: resp.GetRequestId(),
	})
	if err == nil {
		t.Fatal("expected permission error")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}

func TestListRequests_FilterPending(t *testing.T) {
	m := testModuleWithApproval(t, adminOnlyAuthz())
	if _, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 1, Title: "A", Year: 2000,
	}); err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	resp, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 2, Title: "B", Year: 2001,
	})
	if err != nil {
		t.Fatalf("RequestMovie 2: %v", err)
	}
	if _, err := m.DenyRequest(authCtx("admin-1"), &requestmedia.DenyRequestRequest{
		RequestId: resp.GetRequestId(), Reason: "no",
	}); err != nil {
		t.Fatalf("DenyRequest: %v", err)
	}
	list, err := m.ListRequests(authCtx("admin-1"), &requestmedia.ListRequestsRequest{Status: StatusPending})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(list.GetRequests()) != 1 || list.GetRequests()[0].GetTitle() != "A" {
		t.Fatalf("pending list = %+v", list.GetRequests())
	}
}

func TestAddToWatchlist(t *testing.T) {
	m := testModuleWithApproval(t, allowAllAuthz())
	resp, err := m.AddToWatchlist(authCtx("user-1"), &requestmedia.AddToWatchlistRequest{
		ItemType: "movie", TmdbId: 99, Title: "Saved", Year: 2024,
	})
	if err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}
	if resp.GetStatus() != StatusWatchlisted {
		t.Fatalf("status = %q", resp.GetStatus())
	}
	st, err := m.GetStatus(authCtx("user-1"), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetStatus() != StatusWatchlisted {
		t.Fatalf("stored status = %q", st.GetStatus())
	}
}

func TestApproveRequest_AddMovieViaPreferredCap(t *testing.T) {
	m := testModuleWithApproval(t, adminOnlyAuthz())
	stub := &stubMovies{}
	addr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, stub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		if capability == "media.library.movie" {
			return addr, nil
		}
		return "", fmt.Errorf("no %s", capability)
	}
	pending, err := m.RequestMovie(authCtx("user-1"), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999,
	})
	if err != nil || pending.GetStatus() != StatusPending {
		t.Fatalf("pending request: status=%q err=%v", pending.GetStatus(), err)
	}
	approved, err := m.ApproveRequest(authCtx("admin-1"), &requestmedia.ApproveRequestRequest{
		RequestId: pending.GetRequestId(),
	})
	if err != nil {
		t.Fatalf("ApproveRequest: %v", err)
	}
	if approved.GetStatus() != StatusAdded || approved.GetItemId() != "movie-1" {
		t.Fatalf("approved = %+v", approved)
	}
	if stub.last == nil || stub.last.GetTitle() != "Fight Club" {
		t.Fatalf("AddMovie not called: %+v", stub.last)
	}
}
