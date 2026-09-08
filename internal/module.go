package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/Muxcore-Media/core/pkg/contracts"
	workflowv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/workflow/v1"
	"github.com/Muxcore-Media/core/sdk/go/client"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
	metadatav1 "github.com/Muxcore-Media/metadata-tmdb/proto/metadatav1"
	"github.com/Muxcore-Media/request-media/internal/authz"
	"github.com/Muxcore-Media/request-media/internal/grpctls"
	"github.com/Muxcore-Media/request-media/internal/reqquality"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

type Module struct {
	requestmedia.UnimplementedRequestServiceServer

	mu               sync.RWMutex
	cfgMu            sync.RWMutex
	mc               *client.Client
	store            *reqstore.Store
	id               string
	grpcAddr         string
	httpAddr         string
	dataDir          string
	preferWorkflow   bool
	requireApproval  bool
	quotaMaxPending  int
	quotaMaxPerWeek  int
	autoApproveUsers []string
	authz            *authz.Checker
	requests         map[string]*requestRecord
	grpcSrv          *grpc.Server
	httpSrv          *http.Server
	lis              net.Listener
	httpLis          net.Listener

	// findAddr overrides capability discovery in tests.
	findAddr func(ctx context.Context, capability string) (string, error)

	automationClient automationv1.AutomationServiceClient
	automationConn   *grpc.ClientConn
}

type requestRecord struct {
	ID               string    `json:"id"`
	ItemType         string    `json:"itemType"`
	ItemID           string    `json:"itemId"`
	TMDBID           int32     `json:"tmdbId"`
	Title            string    `json:"title"`
	Year             int32     `json:"year"`
	Poster           string    `json:"poster"`
	Status           string    `json:"status"`
	RequestedBy      string    `json:"requestedBy"`
	DenyReason       string    `json:"denyReason"`
	SeasonNumber     int32     `json:"seasonNumber"`
	EpisodeNumber    int32     `json:"episodeNumber"`
	Overview         string    `json:"overview"`
	Genres           []string  `json:"genres"`
	QualityProfileID string    `json:"qualityProfileId,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type Config struct {
	ID              string
	GRPCAddr        string
	HTTPAddr        string
	DataDir         string
	PreferWorkflow  *bool
	RequireApproval *bool
	Authz           *authz.Checker
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "request-media"
	}
	if cfg.GRPCAddr == "" {
		if v := os.Getenv("REQUEST_GRPC_ADDR"); v != "" {
			cfg.GRPCAddr = v
		}
	}
	cfg.GRPCAddr = resolveGRPCAddr(cfg.GRPCAddr)
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = "127.0.0.1:9481"
	}
	if cfg.HTTPAddr == "" {
		if v := os.Getenv("REQUEST_HTTP_ADDR"); v != "" {
			cfg.HTTPAddr = v
		}
		if cfg.HTTPAddr == "" {
			cfg.HTTPAddr = ":9380"
		}
	}
	if cfg.DataDir == "" {
		if v := os.Getenv("REQUEST_DATA_DIR"); v != "" {
			cfg.DataDir = v
		}
		if cfg.DataDir == "" {
			cfg.DataDir = "data"
		}
	}
	prefer := true
	if cfg.PreferWorkflow != nil {
		prefer = *cfg.PreferWorkflow
	}
	if v := os.Getenv("REQUEST_PREFER_WORKFLOW"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			prefer = b
		}
	}
	requireApproval := true
	if cfg.RequireApproval != nil {
		requireApproval = *cfg.RequireApproval
	}
	if v := os.Getenv("REQUEST_REQUIRE_APPROVAL"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			requireApproval = b
		}
	}
	envPolicy := policyFromEnv()
	checker := cfg.Authz
	return &Module{
		id:               cfg.ID,
		grpcAddr:         cfg.GRPCAddr,
		httpAddr:         cfg.HTTPAddr,
		dataDir:          cfg.DataDir,
		preferWorkflow:   prefer,
		requireApproval:  requireApproval,
		quotaMaxPending:  envPolicy.MaxPendingPerUser,
		quotaMaxPerWeek:  envPolicy.MaxPerWeek,
		autoApproveUsers: envPolicy.AutoApproveUsers,
		authz:            checker,
		requests:         make(map[string]*requestRecord),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Request Media",
		Version:        "0.3.3",
		Roles:          []string{"media_request"},
		Description:    "Web UI and gRPC API for requesting movies and TV shows",
		Author:         "MuxCore",
		Capabilities:   []string{"media.request", "settings"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.httpAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	if err := os.MkdirAll(m.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir %s: %w", m.dataDir, err)
	}
	store, err := reqstore.Open(filepath.Join(m.dataDir, "requests.db"))
	if err != nil {
		return fmt.Errorf("open request store: %w", err)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		store.Close()
		return fmt.Errorf("load requests: %w", err)
	}
	m.store = store
	m.mu.Lock()
	for id, r := range loaded {
		m.requests[id] = fromStoreRecord(r)
	}
	m.mu.Unlock()
	m.loadPolicyFile()
	if m.authz == nil {
		m.authz = authz.New(authz.Config{
			FindAuthorizerAddr: func(ctx context.Context) (string, error) {
				return m.findModuleAddr(ctx, "authorizer")
			},
		})
	}

	grpcLis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.lis = grpcLis

	httpLis, err := net.Listen("tcp", m.httpAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP %s: %w", m.httpAddr, err)
	}
	m.httpLis = httpLis

	slog.Info("request-media initialized", "grpc", m.grpcAddr, "http", m.httpAddr, "data_dir", m.dataDir, "requests", len(loaded))
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	var grpcOpts []grpc.ServerOption
	tlsCfg, err := grpctls.ServerConfig()
	if err != nil {
		return fmt.Errorf("gRPC TLS: %w", err)
	}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
		slog.Info("request-media gRPC TLS enabled", "addr", m.grpcAddr)
	} else {
		slog.Warn("request-media gRPC listening without TLS (dev only)",
			"addr", m.grpcAddr,
			"hint", "unset MUXCORE_INSECURE_DISABLE_TLS for production",
		)
	}
	m.grpcSrv = grpc.NewServer(grpcOpts...)
	requestmedia.RegisterRequestServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", m.handleSearch)
	mux.HandleFunc("/api/request", m.handleRequest)
	mux.HandleFunc("/api/requests", m.handleRequests)
	mux.HandleFunc("/api/requests/", m.handleRequestAction)
	mux.HandleFunc("/api/watchlist", m.handleWatchlist)
	mux.HandleFunc("/api/watchlist/", m.handleWatchlistItem)
	mux.HandleFunc("/api/request-policy", m.handleRequestPolicy)
	mux.HandleFunc("/", m.handleIndex)

	m.httpSrv = &http.Server{Handler: mux}

	go m.dialCore(ctx)

	go func() {
		slog.Info("request-media gRPC service started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.lis); err != nil {
			slog.Error("request-media gRPC serve error", "error", err)
		}
	}()
	go func() {
		slog.Info("request-media HTTP service started", "addr", m.httpAddr)
		if err := m.httpSrv.Serve(m.httpLis); err != nil && err != http.ErrServerClosed {
			slog.Error("request-media HTTP serve error", "error", err)
		}
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.httpSrv != nil {
		m.httpSrv.Close()
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.mc != nil {
		m.mc.Close()
	}
	if m.automationConn != nil {
		m.automationConn.Close()
	}
	if m.store != nil {
		m.store.Close()
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
	insecureMode := os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"
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
	go m.runReadySubscriptions(ctx)
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

func (m *Module) findModuleAddr(ctx context.Context, capability string) (string, error) {
	if m.findAddr != nil {
		return m.findAddr(ctx, capability)
	}
	if m.mc == nil {
		return "", fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, capability)
	if err != nil {
		return "", fmt.Errorf("discover %s: %w", capability, err)
	}
	for _, mod := range modules {
		addr := dialAddrForModule(mod.Id, mod.HttpAddr)
		if addr == "" {
			continue
		}
		return addr, nil
	}
	return "", fmt.Errorf("no module with capability %q found", capability)
}

// resolveGRPCAddr prefers loopback when plaintext is explicitly enabled and the
// bind address would otherwise listen on all interfaces.
func resolveGRPCAddr(addr string) string {
	if !grpctls.InsecureAllowed() {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "127.0.0.1" + addr
		}
		return addr
	}
	if host == "" || host == "0.0.0.0" {
		return "127.0.0.1:" + port
	}
	return addr
}

// dialAddrForModule maps discovery HttpAddr to a dial target.
// Explicit hosts (e.g. 127.0.0.1) are preserved for host-process MVP.
// Empty / wildcard hosts rewrite to module ID for Docker DNS, unless
// MUXCORE_MESH_DIAL_LOCAL=true (then 127.0.0.1).
func dialAddrForModule(moduleID, httpAddr string) string {
	if httpAddr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil || port == "" {
		return httpAddr
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return net.JoinHostPort(host, port)
	}
	if os.Getenv("MUXCORE_MESH_DIAL_LOCAL") == "true" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	if moduleID != "" {
		return net.JoinHostPort(moduleID, port)
	}
	return httpAddr
}

func (m *Module) findModuleAddrPrefer(ctx context.Context, capabilities ...string) (string, error) {
	var lastErr error
	for _, cap := range capabilities {
		addr, err := m.findModuleAddr(ctx, cap)
		if err == nil {
			return addr, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no module found")
	}
	return "", lastErr
}

func (m *Module) saveRequest(rec *requestRecord) {
	now := time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	m.mu.Lock()
	m.requests[rec.ID] = rec
	m.mu.Unlock()
	if m.store == nil {
		return
	}
	if err := m.store.Put(toStoreRecord(rec)); err != nil {
		slog.Warn("persist request failed", "id", rec.ID, "error", err)
	}
}

func toStoreRecord(r *requestRecord) *reqstore.Record {
	return &reqstore.Record{
		ID: r.ID, ItemType: r.ItemType, ItemID: r.ItemID, TMDBID: r.TMDBID,
		Title: r.Title, Year: r.Year, Poster: r.Poster, Status: r.Status,
		RequestedBy: r.RequestedBy, DenyReason: r.DenyReason,
		SeasonNumber: r.SeasonNumber, EpisodeNumber: r.EpisodeNumber,
		Overview: r.Overview, GenresJSON: encodeGenres(r.Genres),
		QualityProfileID: r.QualityProfileID,
		CreatedAt:        r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func fromStoreRecord(r *reqstore.Record) *requestRecord {
	return &requestRecord{
		ID: r.ID, ItemType: r.ItemType, ItemID: r.ItemID, TMDBID: r.TMDBID,
		Title: r.Title, Year: r.Year, Poster: r.Poster, Status: r.Status,
		RequestedBy: r.RequestedBy, DenyReason: r.DenyReason,
		SeasonNumber: r.SeasonNumber, EpisodeNumber: r.EpisodeNumber,
		Overview: r.Overview, Genres: decodeGenres(r.GenresJSON),
		QualityProfileID: r.QualityProfileID,
		CreatedAt:        r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (m *Module) getRequireApproval() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.requireApproval
}

func (m *Module) ensureAutomation(ctx context.Context) error {
	m.mu.RLock()
	if m.automationClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()
	addr, err := m.findModuleAddr(ctx, "media.automation")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial automation: %w", err)
	}
	m.mu.Lock()
	m.automationConn = conn
	m.automationClient = automationv1.NewAutomationServiceClient(conn)
	m.mu.Unlock()
	return nil
}

type queueParams struct {
	ItemType         string
	ItemID           string
	TmdbID           int32
	Title            string
	Year             int32
	SeasonNumber     int32
	EpisodeNumber    int32
	QualityProfileID string
}

func (rec *requestRecord) asQueue(itemType, itemID string) queueParams {
	return queueParams{
		ItemType:         itemType,
		ItemID:           itemID,
		TmdbID:           rec.TMDBID,
		Title:            rec.Title,
		Year:             rec.Year,
		SeasonNumber:     rec.SeasonNumber,
		EpisodeNumber:    rec.EpisodeNumber,
		QualityProfileID: rec.QualityProfileID,
	}
}

func (m *Module) tryQueueForAcquisition(ctx context.Context, p queueParams) {
	// Detach from caller deadline — HTTP clients often cancel before automation
	// finishes clean-title fetch inside AddToQueue. Request success must not wait.
	go m.queueForAcquisitionAsync(p)
}

func (m *Module) queueForAcquisitionAsync(p queueParams) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.ensureAutomation(ctx); err != nil {
		slog.Warn("request-media: automation unavailable for queue", "title", p.Title, "error", err)
		return
	}
	m.mu.RLock()
	ac := m.automationClient
	m.mu.RUnlock()
	if ac == nil {
		slog.Warn("request-media: automation client nil", "title", p.Title)
		return
	}
	resp, err := ac.AddToQueue(ctx, &automationv1.AddToQueueRequest{
		ItemType:         p.ItemType,
		ItemId:           p.ItemID,
		TmdbId:           p.TmdbID,
		Title:            p.Title,
		Year:             p.Year,
		SeasonNumber:     p.SeasonNumber,
		EpisodeNumber:    p.EpisodeNumber,
		QualityProfileId: p.QualityProfileID,
	})
	if err != nil {
		slog.Warn("request-media: AddToQueue failed", "title", p.Title, "item_id", p.ItemID, "error", err)
		return
	}
	slog.Info("request-media: queued for acquisition",
		"title", p.Title, "item_type", p.ItemType, "item_id", p.ItemID, "queue_id", resp.GetQueueId())
}

func (m *Module) metadataClient(ctx context.Context) (metadatav1.MetadataServiceClient, *grpc.ClientConn, error) {
	addr, err := m.findModuleAddr(ctx, "metadata")
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial metadata: %w", err)
	}
	return metadatav1.NewMetadataServiceClient(conn), conn, nil
}

// --- HTTP Handlers ---

type searchResult struct {
	ID       int32   `json:"id"`
	Title    string  `json:"title"`
	Year     int32   `json:"year"`
	Overview string  `json:"overview"`
	Poster   string  `json:"poster"`
	VoteAvg  float64 `json:"voteAvg"`
}

func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		json.NewEncoder(w).Encode([]searchResult{})
		return
	}

	client, conn, err := m.metadataClient(r.Context())
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "metadata module unavailable", "results": []searchResult{},
		})
		return
	}
	defer conn.Close()

	resp, err := client.Search(r.Context(), &metadatav1.SearchRequest{
		Query: q,
		Type:  metadatav1.MediaType_MEDIA_TYPE_MOVIE,
		Page:  1,
	})
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(), "results": []searchResult{},
		})
		return
	}

	results := make([]searchResult, 0, len(resp.GetResults()))
	for _, r := range resp.GetResults() {
		year := extractYear(r.GetReleaseDate())
		results = append(results, searchResult{
			ID: r.GetId(), Title: r.GetTitle(), Year: year,
			Overview: r.GetOverview(), Poster: r.GetPosterPath(),
			VoteAvg: r.GetVoteAverage(),
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
	})
}

// httpCallerCtx propagates caller identity from the HTTP request.
// X-Caller-Id is only trustworthy when injected by the MuxCore mesh proxy; direct
// clients can spoof it. Missing identity leaves the caller empty so authz fails closed.
func httpCallerCtx(r *http.Request) context.Context {
	caller := strings.TrimSpace(r.Header.Get("X-Caller-Id"))
	if caller == "" {
		caller = strings.TrimSpace(r.Header.Get("X-MuxCore-User"))
	}
	return contracts.WithCallerID(r.Context(), caller)
}

func (m *Module) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TMDBID           int32  `json:"tmdbId"`
		Title            string `json:"title"`
		Year             int32  `json:"year"`
		Overview         string `json:"overview"`
		Poster           string `json:"poster"`
		MediaType        string `json:"mediaType"`
		QualityProfile   string `json:"qualityProfile"`
		QualityProfileID string `json:"qualityProfileId"`
		SeasonNumber     int32  `json:"seasonNumber"`
		EpisodeNumber    int32  `json:"episodeNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	qp := reqquality.Normalize(req.QualityProfileID)
	if qp == "" {
		qp = reqquality.Normalize(req.QualityProfile)
	}
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	if mediaType == "" || mediaType == "movie" {
		mediaType = "movie"
	}

	ctx := httpCallerCtx(r)
	var rec *requestRecord
	switch mediaType {
	case "tv":
		rec = &requestRecord{
			ID:       fmt.Sprintf("req_tv_%d", time.Now().UnixNano()),
			ItemType: "tv", Title: req.Title, Year: req.Year, TMDBID: req.TMDBID,
			Overview: req.Overview, Poster: req.Poster,
			SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber,
			QualityProfileID: qp,
		}
	default:
		rec = &requestRecord{
			ID:       fmt.Sprintf("req_mv_%d", time.Now().UnixNano()),
			ItemType: "movie", Title: req.Title, Year: req.Year, TMDBID: req.TMDBID,
			Overview: req.Overview, Poster: req.Poster,
			QualityProfileID: qp,
		}
	}

	statusOut, itemID, err := m.createPendingOrFulfill(ctx, rec)
	if err != nil {
		slog.Error("request media", "error", err, "mediaType", rec.ItemType)
		writeGRPCError(w, err)
		return
	}

	out := map[string]string{
		"requestId": rec.ID,
		"status":    statusOut,
	}
	if rec.ItemType == "tv" {
		out["seriesId"] = itemID
	} else {
		out["movieId"] = itemID
	}
	json.NewEncoder(w).Encode(out)
}

func (m *Module) handleRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := httpCallerCtx(r)
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireList(ctx, caller); err != nil {
		writeGRPCError(w, err)
		return
	}
	statusFilter := r.URL.Query().Get("status")
	m.mu.RLock()
	list := make([]*requestRecord, 0, len(m.requests))
	for _, rec := range m.requests {
		if statusFilter != "" && rec.Status != statusFilter {
			continue
		}
		list = append(list, rec)
	}
	m.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	json.NewEncoder(w).Encode(list)
}

func (m *Module) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/requests/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	requestID, action := parts[0], parts[1]
	ctx := httpCallerCtx(r)
	switch action {
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		resp, err := m.ApproveRequest(ctx, &requestmedia.ApproveRequestRequest{RequestId: requestID})
		if err != nil {
			writeGRPCError(w, err)
			return
		}
		json.NewEncoder(w).Encode(resp)
	case "deny":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		resp, err := m.DenyRequest(ctx, &requestmedia.DenyRequestRequest{RequestId: requestID, Reason: body.Reason})
		if err != nil {
			writeGRPCError(w, err)
			return
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.NotFound(w, r)
	}
}

func (m *Module) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := httpCallerCtx(r)
	var req struct {
		ItemType string `json:"itemType"`
		TMDBID   int32  `json:"tmdbId"`
		Title    string `json:"title"`
		Year     int32  `json:"year"`
		Poster   string `json:"poster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	resp, err := m.AddToWatchlist(ctx, &requestmedia.AddToWatchlistRequest{
		ItemType: req.ItemType, TmdbId: req.TMDBID, Title: req.Title, Year: req.Year, Poster: req.Poster,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	json.NewEncoder(w).Encode(resp)
}

func (m *Module) handleWatchlistItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/watchlist/")
	if id == "" {
		http.Error(w, "request id required", http.StatusBadRequest)
		return
	}
	ctx := httpCallerCtx(r)
	if _, err := m.RemoveFromWatchlist(ctx, &requestmedia.RemoveFromWatchlistRequest{RequestId: id}); err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	code := http.StatusInternalServerError
	switch st.Code() {
	case codes.Unauthenticated:
		code = http.StatusUnauthorized
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.FailedPrecondition:
		code = http.StatusConflict
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.ResourceExhausted:
		code = http.StatusTooManyRequests
	}
	if code == http.StatusTooManyRequests {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": st.Message(), "code": "request.quota"})
		return
	}
	http.Error(w, st.Message(), code)
}

func (m *Module) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, nil)
}

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Request Media</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#111;color:#eee;min-height:100vh}
header{background:#1a1a2e;padding:16px 24px;display:flex;align-items:center;gap:16px;border-bottom:2px solid #e66001}
header h1{font-size:20px;color:#e66001}
.search-bar{flex:1;max-width:600px;display:flex;gap:8px}
.search-bar input{flex:1;padding:10px 14px;border:1px solid #333;border-radius:6px;background:#222;color:#eee;font-size:14px}
.search-bar input:focus{outline:none;border-color:#e66001}
.search-bar button{padding:10px 20px;background:#e66001;color:#fff;border:none;border-radius:6px;cursor:pointer;font-weight:600}
.search-bar button:disabled{opacity:0.5}
main{display:flex;gap:24px;padding:24px;max-width:1400px;margin:0 auto}
.results{flex:1;min-width:0}
.results-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:16px}
.card{background:#1a1a2e;border-radius:8px;overflow:hidden;cursor:pointer;transition:transform .15s,border-color .15s;border:2px solid transparent}
.card:hover{transform:translateY(-2px);border-color:#e66001}
.card.selected{border-color:#e66001;box-shadow:0 0 12px rgba(230,96,1,0.3)}
.card-poster{width:100%;aspect-ratio:2/3;background:#222;display:flex;align-items:center;justify-content:center;color:#555;font-size:12px;overflow:hidden}
.card-poster img{width:100%;height:100%;object-fit:cover}
.card-info{padding:10px}
.card-info h3{font-size:13px;margin-bottom:4px;line-height:1.3;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.card-info .year{font-size:11px;color:#888}
.card-info .rating{font-size:11px;color:#e66001;margin-top:2px}
.detail-panel{width:380px;flex-shrink:0;background:#1a1a2e;border-radius:8px;padding:20px;position:sticky;top:24px;align-self:start;display:none}
.detail-panel.visible{display:block}
.detail-poster{width:100%;aspect-ratio:2/3;background:#222;border-radius:6px;overflow:hidden;margin-bottom:12px}
.detail-poster img{width:100%;height:100%;object-fit:cover}
.detail-panel h2{font-size:18px;margin-bottom:4px}
.detail-panel .meta{color:#888;font-size:13px;margin-bottom:8px}
.detail-panel .overview{font-size:13px;color:#bbb;line-height:1.5;margin-bottom:16px;max-height:120px;overflow-y:auto}
.detail-panel .rating{color:#e66001;font-size:14px;margin-bottom:12px}
.request-btn{width:100%;padding:12px;background:#e66001;color:#fff;border:none;border-radius:6px;font-size:15px;font-weight:600;cursor:pointer}
.request-btn:disabled{opacity:0.5;cursor:not-allowed}
.request-btn.requested{background:#2a6e2a}
.status-msg{margin-top:8px;font-size:13px;color:#888;text-align:center}
.history-section{margin-top:24px;padding-top:16px;border-top:1px solid #333}
.history-section h3{font-size:14px;color:#888;margin-bottom:8px}
.history-item{display:flex;align-items:center;gap:8px;padding:6px 0;font-size:13px}
.history-item .status{font-size:11px;padding:2px 6px;border-radius:4px}
.history-item .status.added{background:#1a4a1a;color:#4caf50}
.history-item .status.requested{background:#4a3a1a;color:#ff9800}
@media(max-width:900px){main{flex-direction:column}.detail-panel{width:100%;position:static}}
</style>
</head>
<body>
<header>
<h1>Request Media</h1>
<div class="search-bar">
<input id="searchInput" type="text" placeholder="Search for a movie..." autofocus>
<button id="searchBtn" onclick="search()">Search</button>
</div>
</header>
<main>
<div class="results">
<div class="results-grid" id="resultsGrid"></div>
</div>
<div class="detail-panel" id="detailPanel">
<div class="detail-poster" id="detailPoster"></div>
<h2 id="detailTitle"></h2>
<div class="meta" id="detailMeta"></div>
<div class="rating" id="detailRating"></div>
<div class="overview" id="detailOverview"></div>
<button class="request-btn" id="requestBtn" onclick="requestMovie()">Request Movie</button>
<div class="status-msg" id="statusMsg"></div>
<div class="history-section">
<h3>Recent Requests</h3>
<div id="historyList"></div>
</div>
</div>
</main>
<script>
let selectedMovie = null;
let searchTimeout = null;

document.getElementById('searchInput').addEventListener('input', function() {
clearTimeout(searchTimeout);
const q = this.value.trim();
if (q.length < 2) { document.getElementById('resultsGrid').innerHTML = ''; return; }
searchTimeout = setTimeout(search, 400);
});
document.getElementById('searchInput').addEventListener('keydown', function(e) { if (e.key === 'Enter') search(); });

async function search() {
const q = document.getElementById('searchInput').value.trim();
if (!q) return;
document.getElementById('searchBtn').disabled = true;
try {
const resp = await fetch('/api/search?q=' + encodeURIComponent(q));
const data = await resp.json();
if (data.error) { renderResults([]); showManualEntry(q, data.error); }
else { renderResults(data.results || []); hideManualEntry(); }
} catch(e) { console.error(e); }
document.getElementById('searchBtn').disabled = false;
}

let manualEntry = null;
function showManualEntry(query, error) {
manualEntry = { query };
const panel = document.getElementById('detailPanel');
panel.classList.add('visible');
document.getElementById('detailTitle').textContent = 'Search unavailable: ' + error;
document.getElementById('detailMeta').textContent = 'Enter TMDB ID manually to request';
document.getElementById('detailPoster').innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#555">Manual Entry</div>';
document.getElementById('detailRating').textContent = '';
document.getElementById('detailOverview').innerHTML =
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#888">TMDB ID</label>' +
'<input id="manualTmdbId" type="number" style="width:100%;padding:8px;border:1px solid #333;border-radius:4px;background:#222;color:#eee;font-size:14px;margin-top:4px" placeholder="e.g. 218 for The Terminator"></div>' +
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#888">Title</label>' +
'<input id="manualTitle" type="text" style="width:100%;padding:8px;border:1px solid #333;border-radius:4px;background:#222;color:#eee;font-size:14px;margin-top:4px" value="' + esc(query) + '"></div>' +
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#888">Year</label>' +
'<input id="manualYear" type="number" style="width:100%;padding:8px;border:1px solid #333;border-radius:4px;background:#222;color:#eee;font-size:14px;margin-top:4px" placeholder="e.g. 1984"></div>';
document.getElementById('requestBtn').textContent = 'Request Movie';
document.getElementById('requestBtn').className = 'request-btn';
document.getElementById('requestBtn').disabled = false;
document.getElementById('statusMsg').textContent = '';
document.getElementById('requestBtn').onclick = function() {
const tmdbId = parseInt(document.getElementById('manualTmdbId').value);
const title = document.getElementById('manualTitle').value.trim();
const year = parseInt(document.getElementById('manualYear').value) || 0;
if (!tmdbId || !title) { document.getElementById('statusMsg').textContent = 'TMDB ID and Title are required'; return; }
selectedMovie = { id: tmdbId, title, year, overview: '', poster: '' };
requestMovie();
};
}

function hideManualEntry() {
manualEntry = null;
document.getElementById('requestBtn').onclick = requestMovie;
}

function renderResults(results) {
const grid = document.getElementById('resultsGrid');
if (results.length === 0) {
grid.innerHTML = '<div style="grid-column:1/-1;text-align:center;color:#555;padding:40px;font-size:14px">No results found. Try a different search.</div>';
return;
}
grid.innerHTML = results.map(r => {
const poster = r.poster ? 'https://image.tmdb.org/t/p/w342' + r.poster : '';
return '<div class="card' + (selectedMovie && selectedMovie.id === r.id ? ' selected' : '') + '" onclick="selectMovie(' + r.id + ')">' +
'<div class="card-poster">' + (poster ? '<img src="' + poster + '" loading="lazy">' : 'No Poster') + '</div>' +
'<div class="card-info"><h3>' + esc(r.title) + '</h3>' +
'<div class="year">' + (r.year || '') + '</div>' +
'<div class="rating">★ ' + (r.voteAvg ? r.voteAvg.toFixed(1) : '') + '</div></div></div>';
}).join('');
}

function selectMovie(id) {
selectedMovie = { id };
document.querySelectorAll('.card').forEach(c => c.classList.remove('selected'));
document.querySelector('.card:nth-child(' + (document.querySelectorAll('.card').length - Array.from(document.querySelectorAll('.card')).findIndex(c => c.onclick.toString().includes('selectMovie(' + id + ')'))) + ')')?.classList.add('selected');
const card = Array.from(document.querySelectorAll('.card')).find(c => c.onclick && c.onclick.toString().includes('selectMovie(' + id + ')'));
card?.classList.add('selected');
const panel = document.getElementById('detailPanel');
panel.classList.add('visible');
document.getElementById('requestBtn').className = 'request-btn';
document.getElementById('requestBtn').disabled = false;
document.getElementById('statusMsg').textContent = '';

fetch('/api/search?q=' + encodeURIComponent(document.getElementById('searchInput').value.trim())).then(r => r.json()).then(data => {
const results = data.results || [];
const movie = results.find(r => r.id === id);
if (!movie) return;
selectedMovie = movie;
document.getElementById('detailTitle').textContent = movie.title;
document.getElementById('detailMeta').textContent = (movie.year || '') + ' • TMDB: ' + movie.id;
document.getElementById('detailRating').textContent = '★ ' + (movie.voteAvg ? movie.voteAvg.toFixed(1) : 'N/A');
document.getElementById('detailOverview').textContent = movie.overview || 'No overview available.';
const posterEl = document.getElementById('detailPoster');
if (movie.poster) { posterEl.innerHTML = '<img src="https://image.tmdb.org/t/p/w342' + movie.poster + '">'; }
else { posterEl.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#555">No Poster</div>'; }
loadHistory();
});
}

async function requestMovie() {
if (!selectedMovie) return;
const btn = document.getElementById('requestBtn');
btn.disabled = true;
document.getElementById('statusMsg').textContent = 'Requesting...';
try {
const resp = await fetch('/api/request', {
method: 'POST',
headers: {'Content-Type': 'application/json'},
body: JSON.stringify({tmdbId: selectedMovie.id, title: selectedMovie.title, year: selectedMovie.year, overview: selectedMovie.overview, poster: selectedMovie.poster})
});
const result = await resp.json();
if (result.status === 'added') {
btn.className = 'request-btn requested';
btn.textContent = '✓ Requested';
document.getElementById('statusMsg').textContent = 'Added to library! Automation will search and download.';
loadHistory();
} else {
document.getElementById('statusMsg').textContent = 'Requested (queued)';
}
} catch(e) {
document.getElementById('statusMsg').textContent = 'Error: ' + e.message;
btn.disabled = false;
}
}

async function loadHistory() {
try {
const resp = await fetch('/api/requests');
const list = await resp.json();
const el = document.getElementById('historyList');
el.innerHTML = list.map(r => '<div class="history-item">' +
'<span class="status ' + r.status + '">' + r.status + '</span>' +
esc(r.title) + ' (' + (r.year || '') + ')' +
'</div>').join('');
} catch(e) {}
}

function esc(s) { const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

loadHistory();
</script>
</body>
</html>`

// --- gRPC Handlers ---

func extractYear(date string) int32 {
	if len(date) >= 4 {
		var y int32
		fmt.Sscanf(date[:4], "%d", &y)
		return y
	}
	return 0
}

func (m *Module) tryRunWorkflow(ctx context.Context, definitionID string, input map[string]string) (runID string, ok bool) {
	addr, err := m.findModuleAddr(ctx, "workflow.engine")
	if err != nil {
		return "", false
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", false
	}
	defer conn.Close()
	wf := workflowv1.NewWorkflowServiceClient(conn)
	resp, err := wf.Run(ctx, &workflowv1.RunRequest{
		DefinitionId: definitionID,
		Input:        input,
	})
	if err != nil {
		slog.Warn("request-media: workflow Run failed", "definition", definitionID, "error", err)
		return "", false
	}
	return resp.GetRunId(), true
}

func (m *Module) RequestMovie(ctx context.Context, req *requestmedia.RequestMovieRequest) (*requestmedia.RequestMovieResponse, error) {
	requestID := fmt.Sprintf("req_mv_%d", time.Now().UnixNano())
	rec := &requestRecord{
		ID: requestID, ItemType: "movie", Title: req.GetTitle(),
		Year: req.GetYear(), TMDBID: req.GetTmdbId(), Overview: req.GetOverview(),
		Genres: append([]string(nil), req.GetGenres()...),
	}
	finalStatus, itemID, err := m.createPendingOrFulfill(ctx, rec)
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestMovieResponse{
		RequestId: requestID, MovieId: itemID, Status: finalStatus,
	}, nil
}

func (m *Module) RequestTV(ctx context.Context, req *requestmedia.RequestTVRequest) (*requestmedia.RequestTVResponse, error) {
	requestID := fmt.Sprintf("req_tv_%d", time.Now().UnixNano())
	rec := &requestRecord{
		ID: requestID, ItemType: "tv", Title: req.GetTitle(),
		Year: req.GetYear(), TMDBID: req.GetTmdbId(), Overview: req.GetOverview(),
		SeasonNumber: req.GetSeasonNumber(), EpisodeNumber: req.GetEpisodeNumber(),
	}
	finalStatus, itemID, err := m.createPendingOrFulfill(ctx, rec)
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestTVResponse{
		RequestId: requestID, SeriesId: itemID, Status: finalStatus,
	}, nil
}

func (m *Module) GetStatus(ctx context.Context, req *requestmedia.GetStatusRequest) (*requestmedia.GetStatusResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireViewStatus(ctx, caller); err != nil {
		return nil, err
	}
	m.mu.RLock()
	rec, ok := m.requests[req.GetRequestId()]
	m.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "request not found: %s", req.GetRequestId())
	}
	return &requestmedia.GetStatusResponse{
		RequestId: rec.ID, ItemType: rec.ItemType, ItemId: rec.ItemID,
		Title: rec.Title, Year: rec.Year, Status: rec.Status,
		CreatedAt: rec.CreatedAt.Format(time.RFC3339), UpdatedAt: rec.UpdatedAt.Format(time.RFC3339),
		DenyReason: rec.DenyReason, RequestedBy: rec.RequestedBy,
	}, nil
}

var _ contracts.Module = (*Module)(nil)
