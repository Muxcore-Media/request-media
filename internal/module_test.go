package internal

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func testModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		ID:       "request-media-test",
		GRPCAddr: "127.0.0.1:0",
		HTTPAddr: "127.0.0.1:0",
		DataDir:  t.TempDir(),
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

func TestRequestMovie_PersistsWhenLibraryUnavailable(t *testing.T) {
	m := testModule(t)
	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "requested" {
		t.Fatalf("status = %q, want requested", resp.GetStatus())
	}
	st, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetTitle() != "The Terminator" || st.GetItemType() != "movie" {
		t.Fatalf("unexpected status: %+v", st)
	}

	// Reload from disk.
	m2 := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: m.dataDir,
	})
	if err := m2.Init(context.Background()); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer func() { _ = m2.Stop(context.Background()) }()
	st2, err := m2.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus after reload: %v", err)
	}
	if st.GetTitle() != "The Terminator" {
		t.Fatalf("persisted title = %q", st2.GetTitle())
	}
}

func TestRequestMovie_ReusesExistingTMDB(t *testing.T) {
	m := testModule(t)
	first, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.GetRequestId() == "" || first.GetRequestId() != second.GetRequestId() {
		t.Fatalf("expected same request id, got %q then %q", first.GetRequestId(), second.GetRequestId())
	}
	m.mu.RLock()
	n := len(uniqueRequestList(func() []*requestRecord {
		out := make([]*requestRecord, 0, len(m.requests))
		for _, rec := range m.requests {
			out = append(out, rec)
		}
		return out
	}()))
	m.mu.RUnlock()
	if n != 1 {
		t.Fatalf("request map len = %d, want 1", n)
	}
}

func TestRequestSameNameMovieAndTVStaySeparate(t *testing.T) {
	m := testModule(t)
	mv, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 11688, Title: "The Adventures of Paddington Bear", Year: 2014,
	})
	if err != nil {
		t.Fatal(err)
	}
	tv, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: 65334, Title: "The Adventures of Paddington Bear", Year: 1997,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mv.GetRequestId() == tv.GetRequestId() {
		t.Fatal("movie and TV must be separate rows")
	}
	m.mu.RLock()
	list := uniqueRequestList([]*requestRecord{
		m.requests[mv.GetRequestId()],
		m.requests[tv.GetRequestId()],
	})
	m.mu.RUnlock()
	if len(list) != 2 {
		t.Fatalf("unique list len = %d, want 2", len(list))
	}
}

func TestRequestTV_PersistsWhenLibraryUnavailable(t *testing.T) {
	m := testModule(t)
	resp, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: 1396, Title: "Breaking Bad", Year: 2008, SeasonNumber: 1,
	})
	if err != nil {
		t.Fatalf("RequestTV: %v", err)
	}
	if resp.GetStatus() != "requested" {
		t.Fatalf("status = %q, want requested", resp.GetStatus())
	}
	st, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetItemType() != "tv" {
		t.Fatalf("itemType = %q, want tv", st.GetItemType())
	}
}

func TestFindModuleAddrPrefer(t *testing.T) {
	m := testModule(t)
	var tried []string
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		tried = append(tried, capability)
		if capability == "media.library.movies" {
			return "127.0.0.1:1", nil
		}
		return "", fmt.Errorf("missing %s", capability)
	}
	addr, err := m.findModuleAddrPrefer(context.Background(), "media.library.movie", "media.library.movies", "media.library")
	if err != nil {
		t.Fatalf("prefer: %v", err)
	}
	if addr != "127.0.0.1:1" {
		t.Fatalf("addr = %q", addr)
	}
	if len(tried) < 2 || tried[0] != "media.library.movie" || tried[1] != "media.library.movies" {
		t.Fatalf("tried = %v", tried)
	}
}

type stubMovies struct {
	mgmntv1.UnimplementedMovieManagementServiceServer
	last       *mgmntv1.AddMovieRequest
	refreshIDs []string
}

func (s *stubMovies) AddMovie(ctx context.Context, req *mgmntv1.AddMovieRequest) (*mgmntv1.AddMovieResponse, error) {
	s.last = req
	return &mgmntv1.AddMovieResponse{MovieId: "movie-1"}, nil
}

func (s *stubMovies) RefreshMetadata(ctx context.Context, req *mgmntv1.RefreshMetadataRequest) (*mgmntv1.RefreshMetadataResponse, error) {
	s.refreshIDs = append(s.refreshIDs, req.GetMovieId())
	return &mgmntv1.RefreshMetadataResponse{}, nil
}

type stubTV struct {
	tvmgmtv1.UnimplementedTvManagementServiceServer
	last *tvmgmtv1.AddTVShowRequest
}

func (s *stubTV) AddTVShow(ctx context.Context, req *tvmgmtv1.AddTVShowRequest) (*tvmgmtv1.AddTVShowResponse, error) {
	s.last = req
	return &tvmgmtv1.AddTVShowResponse{SeriesId: "series-1"}, nil
}

type stubAutomation struct {
	automationv1.UnimplementedAutomationServiceServer
	last  *automationv1.AddToQueueRequest
	queue []*automationv1.QueueItem
	hist  []*automationv1.DownloadRecord
}

func (s *stubAutomation) AddToQueue(ctx context.Context, req *automationv1.AddToQueueRequest) (*automationv1.AddToQueueResponse, error) {
	s.last = req
	return &automationv1.AddToQueueResponse{QueueId: "q-1"}, nil
}

func (s *stubAutomation) GetQueue(ctx context.Context, req *automationv1.GetQueueRequest) (*automationv1.GetQueueResponse, error) {
	return &automationv1.GetQueueResponse{Items: s.queue, Total: int32(len(s.queue)), Page: 1, PageSize: 100}, nil
}

func (s *stubAutomation) GetHistory(ctx context.Context, req *automationv1.GetHistoryRequest) (*automationv1.GetHistoryResponse, error) {
	return &automationv1.GetHistoryResponse{Records: s.hist, Total: int32(len(s.hist)), Page: 1, PageSize: 100}, nil
}

func startGRPC(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

func TestRequestMovie_AddMovieViaPreferredCap(t *testing.T) {
	m := testModule(t)
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
	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, Overview: "soap",
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "added" || resp.GetMovieId() != "movie-1" {
		t.Fatalf("resp = %+v", resp)
	}
	if stub.last == nil || stub.last.GetTitle() != "Fight Club" {
		t.Fatalf("AddMovie not called: %+v", stub.last)
	}
	st, err := m.GetStatus(context.Background(), &requestmedia.GetStatusRequest{RequestId: resp.GetRequestId()})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.GetStatus() != "added" || st.GetItemId() != "movie-1" {
		t.Fatalf("status = %+v", st)
	}
}

func TestRequestTV_AddTVShowFallback(t *testing.T) {
	m := testModule(t)
	stub := &stubTV{}
	addr := startGRPC(t, func(s *grpc.Server) {
		tvmgmtv1.RegisterTvManagementServiceServer(s, stub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		if capability == "media.library.tv" {
			return addr, nil
		}
		if capability == "workflow.engine" {
			return "", fmt.Errorf("no workflow")
		}
		return "", fmt.Errorf("no %s", capability)
	}
	resp, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: 1396, Title: "Breaking Bad", Year: 2008, Overview: "chem",
	})
	if err != nil {
		t.Fatalf("RequestTV: %v", err)
	}
	if resp.GetStatus() != "added" || resp.GetSeriesId() != "series-1" {
		t.Fatalf("resp = %+v", resp)
	}
	if stub.last == nil || stub.last.GetName() != "Breaking Bad" || stub.last.GetTmdbId() != 1396 {
		t.Fatalf("AddTVShow not called: %+v", stub.last)
	}
}

func TestDialAddrForModule(t *testing.T) {
	t.Setenv("MUXCORE_MESH_DIAL_LOCAL", "")
	if got := dialAddrForModule("indexer-a", "127.0.0.1:9401"); got != "127.0.0.1:9401" {
		t.Fatalf("explicit host: %s", got)
	}
	if got := dialAddrForModule("metadata-tmdb", ":9411"); got != "metadata-tmdb:9411" {
		t.Fatalf("docker dns: %s", got)
	}
	t.Setenv("MUXCORE_MESH_DIAL_LOCAL", "true")
	if got := dialAddrForModule("metadata-tmdb", ":9411"); got != "127.0.0.1:9411" {
		t.Fatalf("local dial: %s", got)
	}
}

func TestRequestMovie_QueuesAutomationAfterAdd(t *testing.T) {
	m := testModule(t)
	moviesStub := &stubMovies{}
	autoStub := &stubAutomation{}
	moviesAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, moviesStub)
	})
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
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
		TmdbId: 550, Title: "Fight Club", Year: 1999,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "added" {
		t.Fatalf("status = %q, want added", resp.GetStatus())
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.last == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.last == nil {
		t.Fatal("AddToQueue not called")
	}
	if autoStub.last.GetItemType() != "movie" || autoStub.last.GetItemId() != "movie-1" ||
		autoStub.last.GetTmdbId() != 550 || autoStub.last.GetTitle() != "Fight Club" {
		t.Fatalf("AddToQueue = %+v", autoStub.last)
	}
}

func TestRequestMovie_SucceedsWhenAutomationUnavailable(t *testing.T) {
	m := testModule(t)
	moviesStub := &stubMovies{}
	moviesAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, moviesStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		if capability == "media.library.movie" {
			return moviesAddr, nil
		}
		return "", fmt.Errorf("no %s", capability)
	}
	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "added" {
		t.Fatalf("status = %q, want added", resp.GetStatus())
	}
}

func TestRequestMovie_QueuesAutomationWhenLibraryUnavailable(t *testing.T) {
	m := testModule(t)
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
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatalf("RequestMovie: %v", err)
	}
	if resp.GetStatus() != "requested" {
		t.Fatalf("status = %q, want requested", resp.GetStatus())
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.last == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.last == nil {
		t.Fatal("AddToQueue not called")
	}
	if autoStub.last.GetItemId() != "tmdb_218" || autoStub.last.GetItemType() != "movie" {
		t.Fatalf("AddToQueue = %+v", autoStub.last)
	}
}

func TestRequestTV_QueuesAutomationAfterAdd(t *testing.T) {
	m := testModule(t)
	tvStub := &stubTV{}
	autoStub := &stubAutomation{}
	tvAddr := startGRPC(t, func(s *grpc.Server) {
		tvmgmtv1.RegisterTvManagementServiceServer(s, tvStub)
	})
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.tv":
			return tvAddr, nil
		case "media.automation":
			return autoAddr, nil
		case "workflow.engine":
			return "", fmt.Errorf("no workflow")
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}
	resp, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: 1396, Title: "Breaking Bad", Year: 2008, SeasonNumber: 1, EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("RequestTV: %v", err)
	}
	if resp.GetStatus() != "added" || resp.GetSeriesId() != "series-1" {
		t.Fatalf("resp = %+v", resp)
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.last == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.last == nil {
		t.Fatal("AddToQueue not called")
	}
	if autoStub.last.GetItemType() != "tv" || autoStub.last.GetItemId() != "series-1" ||
		autoStub.last.GetTmdbId() != 1396 || autoStub.last.GetSeasonNumber() != 1 ||
		autoStub.last.GetEpisodeNumber() != 1 {
		t.Fatalf("AddToQueue = %+v", autoStub.last)
	}
}

func TestRequestTV_SeasonZeroDummyQueuesPack(t *testing.T) {
	m := testModule(t)
	tvStub := &stubTV{}
	autoStub := &stubAutomation{}
	tvAddr := startGRPC(t, func(s *grpc.Server) {
		tvmgmtv1.RegisterTvManagementServiceServer(s, tvStub)
	})
	autoAddr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, autoStub)
	})
	m.findAddr = func(ctx context.Context, capability string) (string, error) {
		switch capability {
		case "media.library.tv":
			return tvAddr, nil
		case "media.automation":
			return autoAddr, nil
		case "workflow.engine":
			return "", fmt.Errorf("no workflow")
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}
	resp, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: 253, Title: "Star Trek", Year: 1966, SeasonNumber: 0, EpisodeNumber: 12,
	})
	if err != nil {
		t.Fatalf("RequestTV: %v", err)
	}
	if resp.GetStatus() != "added" {
		t.Fatalf("resp = %+v", resp)
	}
	deadline := time.Now().Add(2 * time.Second)
	for autoStub.last == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if autoStub.last == nil {
		t.Fatal("AddToQueue not called")
	}
	if autoStub.last.GetSeasonNumber() != 0 || autoStub.last.GetEpisodeNumber() != 0 {
		t.Fatalf("expected pack grain 0/0, got S%02dE%02d", autoStub.last.GetSeasonNumber(), autoStub.last.GetEpisodeNumber())
	}
}
