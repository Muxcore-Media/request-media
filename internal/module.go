package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

type Module struct {
	requestmedia.UnimplementedRequestServiceServer

	mu       sync.RWMutex
	mc       *client.Client
	id       string
	grpcAddr string
	requests map[string]*requestRecord
	grpcSrv  *grpc.Server
	lis      net.Listener
}

type requestRecord struct {
	ID        string
	ItemType  string
	ItemID    string
	Title     string
	Year      int32
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Config struct {
	ID       string
	GRPCAddr string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "request-media"
	}
	if cfg.GRPCAddr == "" {
		if v := os.Getenv("REQUEST_GRPC_ADDR"); v != "" {
			cfg.GRPCAddr = v
		}
		if cfg.GRPCAddr == "" {
			cfg.GRPCAddr = ":9480"
		}
	}
	return &Module{
		id:       cfg.ID,
		grpcAddr: cfg.GRPCAddr,
		requests: make(map[string]*requestRecord),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:           m.id,
		Name:         "Request Media",
		Version:      "0.1.0",
		Roles:        []string{"media_request"},
		Description:  "Accepts media requests and dispatches them to the automation pipeline",
		Author:       "MuxCore",
		Capabilities: []string{"media.request"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:     m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", m.grpcAddr, err)
	}
	m.lis = lis
	slog.Info("request-media initialized", "addr", m.grpcAddr)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	requestmedia.RegisterRequestServiceServer(m.grpcSrv, m)

	go m.dialCore(ctx)

	go func() {
		slog.Info("request-media gRPC service started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.lis); err != nil {
			slog.Error("request-media gRPC serve error", "error", err)
		}
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.mc != nil {
		m.mc.Close()
	}
	slog.Info("request-media stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	return nil
}

func (m *Module) dialCore(ctx context.Context) {
	meshAddr := os.Getenv("MUXCORE_GRPC_ADDR")
	if meshAddr == "" {
		meshAddr = "localhost:9090"
	}
	insecureMode := os.Getenv("MUXCORE_GRPC_INSECURE") == "true"
	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}
	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Error("request-media: dial core", "error", err)
		return
	}
	m.mc = c
	slog.Info("request-media: connected to core mesh", "addr", meshAddr)
}

func (m *Module) publish(ctx context.Context, eventType string, payload map[string]interface{}) {
	if m.mc == nil {
		return
	}
	data, _ := json.Marshal(payload)
	if err := m.mc.Events.Publish(ctx, eventType, m.id, data); err != nil {
		slog.Warn("publish event failed", "type", eventType, "error", err)
	}
}

func (m *Module) findMediaMoviesAddr(ctx context.Context) (string, error) {
	if m.mc == nil {
		return "", fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, "media.library")
	if err != nil {
		return "", fmt.Errorf("discover media-movies: %w", err)
	}
	for _, mod := range modules {
		if mod.HttpAddr != "" {
			slog.Debug("found media-movies", "addr", mod.HttpAddr)
			return mod.HttpAddr, nil
		}
	}
	return "", fmt.Errorf("no media-movies module found")
}

func (m *Module) RequestMovie(ctx context.Context, req *requestmedia.RequestMovieRequest) (*requestmedia.RequestMovieResponse, error) {
	requestID := fmt.Sprintf("req_mv_%d", time.Now().UnixNano())

	addr, err := m.findMediaMoviesAddr(ctx)
	if err != nil {
		slog.Warn("cannot find media-movies, requesting via event", "error", err)
		go m.publish(context.Background(), "media.movie.requested", map[string]interface{}{
			"request_id": requestID,
			"tmdb_id":    req.GetTmdbId(),
			"title":      req.GetTitle(),
			"year":       req.GetYear(),
			"overview":   req.GetOverview(),
			"genres":     req.GetGenres(),
		})
		m.mu.Lock()
		m.requests[requestID] = &requestRecord{
			ID: requestID, ItemType: "movie", Title: req.GetTitle(),
			Year: req.GetYear(), Status: "requested", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		m.mu.Unlock()
		return &requestmedia.RequestMovieResponse{
			RequestId: requestID, MovieId: "", Status: "requested",
		}, nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial media-movies: %w", err)
	}
	defer conn.Close()

	moviesClient := mgmntv1.NewMovieManagementServiceClient(conn)
	addResp, err := moviesClient.AddMovie(ctx, &mgmntv1.AddMovieRequest{
		TmdbId:   req.GetTmdbId(),
		Title:    req.GetTitle(),
		Year:     req.GetYear(),
		Overview: req.GetOverview(),
		Genres:   req.GetGenres(),
	})
	if err != nil {
		return nil, fmt.Errorf("add movie: %w", err)
	}

	movieID := addResp.GetMovieId()
	m.mu.Lock()
	m.requests[requestID] = &requestRecord{
		ID: requestID, ItemType: "movie", ItemID: movieID,
		Title: req.GetTitle(), Year: req.GetYear(),
		Status: "added", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.mu.Unlock()

	go m.publish(context.Background(), "media.movie.requested", map[string]interface{}{
		"request_id": requestID,
		"movie_id":   movieID,
		"tmdb_id":    req.GetTmdbId(),
		"title":      req.GetTitle(),
		"year":       req.GetYear(),
	})

	slog.Info("movie requested", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId(), "movie_id", movieID)
	return &requestmedia.RequestMovieResponse{
		RequestId: requestID, MovieId: movieID, Status: "added",
	}, nil
}

func (m *Module) RequestTV(ctx context.Context, req *requestmedia.RequestTVRequest) (*requestmedia.RequestTVResponse, error) {
	requestID := fmt.Sprintf("req_tv_%d", time.Now().UnixNano())

	m.mu.Lock()
	m.requests[requestID] = &requestRecord{
		ID: requestID, ItemType: "tv", Title: req.GetTitle(),
		Year: req.GetYear(), Status: "requested", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.mu.Unlock()

	go m.publish(context.Background(), "media.tv.requested", map[string]interface{}{
		"request_id":    requestID,
		"tmdb_id":       req.GetTmdbId(),
		"title":         req.GetTitle(),
		"year":          req.GetYear(),
		"season_number": req.GetSeasonNumber(),
		"episode_number": req.GetEpisodeNumber(),
	})

	slog.Info("tv requested", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId())
	return &requestmedia.RequestTVResponse{
		RequestId: requestID, Status: "requested",
	}, nil
}

func (m *Module) GetStatus(ctx context.Context, req *requestmedia.GetStatusRequest) (*requestmedia.GetStatusResponse, error) {
	m.mu.RLock()
	rec, ok := m.requests[req.GetRequestId()]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("request not found: %s", req.GetRequestId())
	}
	return &requestmedia.GetStatusResponse{
		RequestId: rec.ID, ItemType: rec.ItemType, ItemId: rec.ItemID,
		Title: rec.Title, Year: rec.Year, Status: rec.Status,
		CreatedAt: rec.CreatedAt.Format(time.RFC3339), UpdatedAt: rec.UpdatedAt.Format(time.RFC3339),
	}, nil
}

var _ contracts.Module = (*Module)(nil)
