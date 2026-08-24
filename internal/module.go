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
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/pkg/tenant"
	workflowv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/workflow/v1"
	"github.com/Muxcore-Media/core/sdk/go/client"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

type Module struct {
	requestmedia.UnimplementedRequestServiceServer

	mu                  sync.RWMutex
	cfgMu               sync.RWMutex
	mc                  *client.Client
	store               *reqstore.Partitions
	id                  string
	grpcAddr            string
	httpAddr            string
	dataDir             string
	preferWorkflow      bool
	requireApproval     bool
	allowedRequestRoles string
	managerAutoApprove  bool
	requests            map[string]*requestRecord
	grpcSrv             *grpc.Server
	httpSrv             *http.Server
	lis                 net.Listener
	httpLis             net.Listener

	// findAddr overrides capability discovery in tests.
	findAddr func(ctx context.Context, capability string) (string, error)

	automationClient automationv1.AutomationServiceClient
	automationConn   *grpc.ClientConn
	statusCancel     context.CancelFunc
}

type requestRecord struct {
	ID             string    `json:"id"`
	ItemType       string    `json:"itemType"`
	ItemID         string    `json:"itemId"`
	TMDBID         int32     `json:"tmdbId"`
	Title          string    `json:"title"`
	Year           int32     `json:"year"`
	Overview       string    `json:"overview,omitempty"`
	Poster         string    `json:"poster"`
	Status         string    `json:"status"`
	StatusDetail   string    `json:"statusDetail,omitempty"`
	StatusLabel    string    `json:"statusLabel,omitempty"`
	RequestedBy    string    `json:"requestedBy,omitempty"`
	ApprovedBy     string    `json:"approvedBy,omitempty"`
	ApprovedAt     time.Time `json:"approvedAt,omitempty"`
	TenantID       string    `json:"tenantId,omitempty"`
	MusicBrainzID  string    `json:"musicbrainzId,omitempty"`
	ReleaseGroupID string    `json:"releaseGroupId,omitempty"`
	RecordingID    string    `json:"recordingId,omitempty"`
	ArtistName     string    `json:"artistName,omitempty"`
	AlbumTitle     string    `json:"albumTitle,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type Config struct {
	ID              string
	GRPCAddr        string
	HTTPAddr        string
	DataDir         string
	PreferWorkflow  *bool
	RequireApproval *bool
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
			cfg.GRPCAddr = ":9481"
		}
	}
	if cfg.HTTPAddr == "" {
		if v := os.Getenv("REQUEST_HTTP_ADDR"); v != "" {
			cfg.HTTPAddr = v
		}
		if cfg.HTTPAddr == "" {
			cfg.HTTPAddr = "127.0.0.1:9380"
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
	requireApproval := false
	if cfg.RequireApproval != nil {
		requireApproval = *cfg.RequireApproval
	}
	if v := os.Getenv("REQUEST_REQUIRE_APPROVAL"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			requireApproval = b
		}
	}
	managerAutoApprove := true
	if v := os.Getenv("REQUEST_MANAGER_AUTO_APPROVE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			managerAutoApprove = b
		}
	}
	return &Module{
		id:                  cfg.ID,
		grpcAddr:            cfg.GRPCAddr,
		httpAddr:            cfg.HTTPAddr,
		dataDir:             cfg.DataDir,
		preferWorkflow:      prefer,
		requireApproval:     requireApproval,
		allowedRequestRoles: parseAllowedRolesEnv(),
		managerAutoApprove:  managerAutoApprove,
		requests:            make(map[string]*requestRecord),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Request Media",
		Version:        "0.3.0",
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
	store, err := reqstore.OpenPartitions(m.dataDir)
	if err != nil {
		return fmt.Errorf("open request store: %w", err)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("load requests: %w", err)
	}
	m.store = store
	m.mu.Lock()
	for id, r := range loaded {
		m.requests[id] = fromStoreRecord(r)
	}
	m.mu.Unlock()

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
	m.grpcSrv = grpc.NewServer()
	requestmedia.RegisterRequestServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", m.handleSearch)
	mux.HandleFunc("/api/discover/", m.handleDiscover)
	mux.HandleFunc("/api/request", m.handleRequest)
	mux.HandleFunc("/api/requests/", m.handleRequestAction)
	mux.HandleFunc("/api/requests", m.handleRequests)
	mux.HandleFunc("/", m.handleIndex)

	m.httpSrv = moduleHTTPServer(tenant.Middleware(mux))

	go m.dialCore(ctx)
	statusCtx, cancel := context.WithCancel(context.Background())
	m.statusCancel = cancel
	go m.statusLoop(statusCtx)

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
	if m.statusCancel != nil {
		m.statusCancel()
		m.statusCancel = nil
	}
	if m.httpSrv != nil {
		if err := m.httpSrv.Shutdown(ctx); err != nil {
			slog.Warn("request-media HTTP shutdown", "error", err)
		}
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.mc != nil {
		_ = m.mc.Close()
	}
	if m.automationConn != nil {
		_ = m.automationConn.Close()
	}
	if m.store != nil {
		_ = m.store.Close()
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

func requestDedupeKey(itemType string, tmdb int32, title string, year int32, tenantID string) string {
	kind := strings.ToLower(strings.TrimSpace(itemType))
	prefix := strings.TrimSpace(tenantID) + ":"
	if tmdb > 0 {
		return fmt.Sprintf("%s%s:tmdb:%d", prefix, kind, tmdb)
	}
	return fmt.Sprintf("%s%s:title:%s:%d", prefix, kind, strings.ToLower(strings.TrimSpace(title)), year)
}

func (m *Module) resolveTenant(ctx context.Context, headers http.Header) string {
	if !tenant.Enabled() {
		return ""
	}
	if id := tenant.IDFrom(ctx); id != "" {
		return id
	}
	if headers != nil {
		if id := strings.TrimSpace(headers.Get("X-Tenant-ID")); id != "" {
			return id
		}
		if id := strings.TrimSpace(headers.Get("X-Auth-Claims-Tenant")); id != "" {
			return id
		}
	}
	return "default"
}

func (m *Module) findExisting(itemType string, tmdb int32, title string, year int32, tenantID string) *requestRecord {
	key := requestDedupeKey(itemType, tmdb, title, year, tenantID)
	if key == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var found *requestRecord
	for _, rec := range m.requests {
		if rec == nil {
			continue
		}
		if requestDedupeKey(rec.ItemType, rec.TMDBID, rec.Title, rec.Year, rec.TenantID) != key {
			continue
		}
		if found == nil {
			found = rec
			continue
		}
		_, loser := reqstorePrefer(found, rec)
		if loser == found {
			found = rec
		}
	}
	return found
}

func reqstorePrefer(a, b *requestRecord) (keep, drop *requestRecord) {
	if a == nil {
		return b, a
	}
	if b == nil {
		return a, b
	}
	aHas := strings.TrimSpace(a.ItemID) != ""
	bHas := strings.TrimSpace(b.ItemID) != ""
	if aHas != bHas {
		if aHas {
			return a, b
		}
		return b, a
	}
	if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID <= b.ID) {
		return a, b
	}
	return b, a
}

func uniqueRequestList(in []*requestRecord) []*requestRecord {
	best := make(map[string]*requestRecord, len(in))
	order := make([]string, 0, len(in))
	for _, rec := range in {
		if rec == nil {
			continue
		}
		k := requestDedupeKey(rec.ItemType, rec.TMDBID, rec.Title, rec.Year, rec.TenantID)
		prev, ok := best[k]
		if !ok {
			best[k] = rec
			order = append(order, k)
			continue
		}
		keep, _ := reqstorePrefer(prev, rec)
		best[k] = keep
	}
	out := make([]*requestRecord, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
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
		RequestedBy: r.RequestedBy, ApprovedBy: r.ApprovedBy, ApprovedAt: r.ApprovedAt,
		TenantID: r.TenantID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func fromStoreRecord(r *reqstore.Record) *requestRecord {
	return &requestRecord{
		ID: r.ID, ItemType: r.ItemType, ItemID: r.ItemID, TMDBID: r.TMDBID,
		Title: r.Title, Year: r.Year, Poster: r.Poster, Status: r.Status,
		RequestedBy: r.RequestedBy, ApprovedBy: r.ApprovedBy, ApprovedAt: r.ApprovedAt,
		TenantID: r.TenantID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
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

func (m *Module) tryQueueForAcquisition(ctx context.Context, p queueParams) {
	// Detach from caller deadline — HTTP clients often cancel before automation
	// finishes clean-title fetch inside AddToQueue. Request success must not wait.
	go m.queueForAcquisitionAsync(p)
}

func tvAcquisitionGrain(season, episode int32) (int32, int32) {
	if season == 0 && episode >= 1 {
		return 0, 0
	}
	return season, episode
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
	season, episode := p.SeasonNumber, p.EpisodeNumber
	if p.ItemType == "tv" {
		season, episode = tvAcquisitionGrain(p.SeasonNumber, p.EpisodeNumber)
	}
	resp, err := ac.AddToQueue(ctx, &automationv1.AddToQueueRequest{
		ItemType:         p.ItemType,
		ItemId:           p.ItemID,
		TmdbId:           p.TmdbID,
		Title:            p.Title,
		Year:             p.Year,
		SeasonNumber:     season,
		EpisodeNumber:    episode,
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
	ID             int32   `json:"id"`
	MusicBrainzID  string  `json:"musicbrainzId,omitempty"`
	ReleaseGroupID string  `json:"releaseGroupId,omitempty"`
	RecordingID    string  `json:"recordingId,omitempty"`
	ArtistName     string  `json:"artistName,omitempty"`
	AlbumTitle     string  `json:"albumTitle,omitempty"`
	Title          string  `json:"title"`
	Year           int32   `json:"year"`
	Overview       string  `json:"overview"`
	Poster         string  `json:"poster"`
	VoteAvg        float64 `json:"voteAvg"`
	MediaType      string  `json:"mediaType"`
}

func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusOK, []searchResult{})
		return
	}

	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if isMusicAlbumSearch(kind) {
		m.handleMusicAlbumSearch(w, r, q)
		return
	}
	if isMusicTrackSearch(kind) {
		m.handleMusicTrackSearch(w, r, q)
		return
	}
	if isMusicRequest(kind) {
		m.handleMusicSearch(w, r, q)
		return
	}

	client, conn, err := m.metadataClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "metadata module unavailable", "results": []searchResult{},
		})
		return
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := handlerContext(r)
	defer cancel()

	reqType := searchTypeFromQuery(r.URL.Query().Get("type"))
	resp, err := client.Search(ctx, &metadatav1.SearchRequest{
		Query: q,
		Type:  reqType,
		Page:  1,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "results": []searchResult{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": mapSearchResults(q, reqType, resp.GetResults()),
	})
}

func (m *Module) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TMDBID         int32  `json:"tmdbId"`
		MusicBrainzID  string `json:"musicbrainzId"`
		ReleaseGroupID string `json:"releaseGroupId"`
		RecordingID    string `json:"recordingId"`
		ArtistName     string `json:"artistName"`
		AlbumTitle     string `json:"albumTitle"`
		Title          string `json:"title"`
		Year           int32  `json:"year"`
		Overview       string `json:"overview"`
		Poster         string `json:"poster"`
		MediaType      string `json:"mediaType"`
		ItemType       string `json:"itemType"`
		Type           string `json:"type"`
		RequestedBy    string `json:"requestedBy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.RequestedBy == "" {
		req.RequestedBy = strings.TrimSpace(r.Header.Get("X-MuxCore-User"))
	}
	roles := splitRoles(r.Header.Get("X-MuxCore-Roles"))
	isAdmin := isPrivilegedRequestor(roles, m.getManagerAutoApprove()) || containsRole(roles, "admin")
	if !m.rolesCanRequest(roles) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: your role cannot request media"})
		return
	}
	tenantID := m.resolveTenant(r.Context(), r.Header)
	ctx := tenant.WithID(r.Context(), tenantID)
	ctx = withRequestPoster(ctx, req.Poster)

	kind := req.MediaType
	if kind == "" {
		kind = req.ItemType
	}
	if kind == "" {
		kind = req.Type
	}
	if isMusicAlbumRequest(kind) {
		gResp, err := m.RequestAlbum(ctx, &requestmedia.RequestAlbumRequest{
			ReleaseGroupId: req.ReleaseGroupID, ArtistMusicbrainzId: req.MusicBrainzID,
			ArtistName: req.ArtistName, Title: req.Title, Year: req.Year, Overview: req.Overview,
			RequestedBy: req.RequestedBy, IsAdmin: isAdmin,
		})
		if err != nil {
			slog.Error("request music album", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"requestId": gResp.GetRequestId(),
			"albumId":   gResp.GetAlbumId(),
			"artistId":  gResp.GetArtistId(),
			"status":    gResp.GetStatus(),
			"itemType":  "music_album",
			"type":      "music_album",
		})
		return
	}
	if isMusicTrackRequest(kind) {
		gResp, err := m.RequestTrack(ctx, &requestmedia.RequestTrackRequest{
			RecordingId: req.RecordingID, ReleaseGroupId: req.ReleaseGroupID,
			ArtistMusicbrainzId: req.MusicBrainzID, ArtistName: req.ArtistName,
			Title: req.Title, AlbumTitle: req.AlbumTitle,
			RequestedBy: req.RequestedBy, IsAdmin: isAdmin,
		})
		if err != nil {
			slog.Error("request music track", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"requestId": gResp.GetRequestId(),
			"albumId":   gResp.GetAlbumId(),
			"artistId":  gResp.GetArtistId(),
			"status":    gResp.GetStatus(),
			"itemType":  "music_track",
			"type":      "music_track",
		})
		return
	}
	if isMusicRequest(kind) {
		gResp, err := m.RequestMusic(ctx, &requestmedia.RequestMusicRequest{
			MusicbrainzId: req.MusicBrainzID, Title: req.Title, Overview: req.Overview,
			RequestedBy: req.RequestedBy, IsAdmin: isAdmin,
		})
		if err != nil {
			slog.Error("request music", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"requestId": gResp.GetRequestId(),
			"artistId":  gResp.GetArtistId(),
			"status":    gResp.GetStatus(),
			"itemType":  "music",
			"type":      "music",
		})
		return
	}
	if isTVRequest(kind) {
		gResp, err := m.RequestTV(ctx, &requestmedia.RequestTVRequest{
			TmdbId: req.TMDBID, Title: req.Title, Year: req.Year, Overview: req.Overview,
			RequestedBy: req.RequestedBy, IsAdmin: isAdmin,
		})
		if err != nil {
			slog.Error("request tv", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"requestId": gResp.GetRequestId(),
			"seriesId":  gResp.GetSeriesId(),
			"status":    gResp.GetStatus(),
			"itemType":  "tv",
			"type":      "tv",
		})
		return
	}

	gResp, err := m.RequestMovie(ctx, &requestmedia.RequestMovieRequest{
		TmdbId: req.TMDBID, Title: req.Title, Year: req.Year, Overview: req.Overview,
		RequestedBy: req.RequestedBy, IsAdmin: isAdmin,
	})
	if err != nil {
		slog.Error("request movie", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"requestId": gResp.GetRequestId(),
		"movieId":   gResp.GetMovieId(),
		"status":    gResp.GetStatus(),
		"itemType":  "movie",
		"type":      "movie",
	})
}

func (m *Module) handleRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	roles := splitRoles(r.Header.Get("X-MuxCore-Roles"))
	if len(roles) == 0 && !allowAnonymousRequest() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
		return
	}
	if len(roles) > 0 && !m.rolesCanRequest(roles) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
		return
	}
	statusFilter := r.URL.Query().Get("status")
	resp, err := m.ListRequests(r.Context(), &requestmedia.ListRequestsRequest{Status: statusFilter})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list := make([]*requestRecord, 0, len(resp.GetRequests()))
	for _, info := range resp.GetRequests() {
		rec := &requestRecord{
			ID: info.GetRequestId(), ItemType: info.GetItemType(), ItemID: info.GetItemId(),
			TMDBID: info.GetTmdbId(), Title: info.GetTitle(), Year: info.GetYear(),
			Status: info.GetStatus(), StatusDetail: info.GetStatusDetail(), StatusLabel: info.GetStatusLabel(),
			RequestedBy: info.GetRequestedBy(), ApprovedBy: info.GetApprovedBy(),
			Poster: info.GetPoster(),
		}
		if info.GetCreatedAt() != "" {
			rec.CreatedAt, _ = time.Parse(time.RFC3339, info.GetCreatedAt())
		}
		if info.GetUpdatedAt() != "" {
			rec.UpdatedAt, _ = time.Parse(time.RFC3339, info.GetUpdatedAt())
		}
		if info.GetApprovedAt() != "" {
			rec.ApprovedAt, _ = time.Parse(time.RFC3339, info.GetApprovedAt())
		}
		list = append(list, rec)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (m *Module) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/requests/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], strings.ToLower(parts[1])
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		By string `json:"by"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.By == "" {
		body.By = strings.TrimSpace(r.Header.Get("X-MuxCore-User"))
	}

	roles := splitRoles(r.Header.Get("X-MuxCore-Roles"))
	if len(roles) == 0 && !allowAnonymousRequest() {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !rolesCanApprove(roles) {
		http.Error(w, "forbidden: admin or manager required", http.StatusForbidden)
		return
	}
	claimTenant := strings.TrimSpace(r.Header.Get("X-Auth-Claims-Tenant"))
	headerTenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if claimTenant != "" && headerTenant != "" {
		if err := tenant.GuardCrossTenant(claimTenant, headerTenant, roles); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	m.mu.RLock()
	rec := m.requests[id]
	m.mu.RUnlock()
	if rec != nil && tenant.Enabled() {
		actorTenant := m.resolveTenant(r.Context(), r.Header)
		if err := tenant.GuardCrossTenant(actorTenant, rec.TenantID, roles); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	switch action {
	case "approve":
		ctx := r.Context()
		if rec != nil && tenant.HasAdminRole(roles) && tenant.Enabled() {
			ctx = tenant.WithID(ctx, rec.TenantID)
		}
		resp, err := m.ApproveRequest(ctx, &requestmedia.ApproveRequestRequest{
			RequestId: id, ApprovedBy: body.By,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if resp.GetError() != "" {
			http.Error(w, resp.GetError(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	case "deny":
		ctx := r.Context()
		if rec != nil && tenant.HasAdminRole(roles) && tenant.Enabled() {
			ctx = tenant.WithID(ctx, rec.TenantID)
		}
		resp, err := m.DenyRequest(ctx, &requestmedia.DenyRequestRequest{
			RequestId: id, DeniedBy: body.By,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if resp.GetError() != "" {
			http.Error(w, resp.GetError(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	default:
		http.NotFound(w, r)
	}
}

func splitRoles(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *Module) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, nil)
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
body{font-family:"Inter","IBM Plex Sans","Segoe UI",system-ui,sans-serif;color:#f5f5f7;min-height:100vh;
background:
  radial-gradient(1200px 600px at 8% -10%, rgba(61,184,168,.12) 0%, transparent 55%),
  radial-gradient(900px 500px at 100% 0%, rgba(245,166,35,.08) 0%, transparent 50%),
  #0b0c0f;
}
header{background:rgba(11,12,15,.85);backdrop-filter:blur(12px);padding:16px 24px;display:flex;align-items:center;gap:16px;border-bottom:1px solid #23252b;position:sticky;top:0;z-index:10}
header h1{font-size:18px;font-weight:700}
header h1 .accent{color:#3db8a8}
.search-bar{flex:1;max-width:600px;display:flex;gap:8px}
.search-bar input{flex:1;padding:10px 14px;border:1px solid #23252b;border-radius:10px;background:#1c1e26;color:#f5f5f7;font-size:14px;font:inherit}
.search-bar input::placeholder{color:#6b6b73}
.search-bar input:focus{outline:2px solid #3db8a8;outline-offset:1px;border-color:#3db8a8}
.search-bar button{padding:10px 20px;background:#3db8a8;color:#0b0c0f;border:none;border-radius:10px;cursor:pointer;font-weight:600;font-size:14px;transition:background-color .15s}
.search-bar button:hover{background:#56d0c0}
.search-bar button:disabled{opacity:0.5;cursor:not-allowed}
main{display:flex;gap:24px;padding:24px;max-width:1400px;margin:0 auto}
.results{flex:1;min-width:0}
.results-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:16px}
.card{background:#15161b;border-radius:10px;overflow:hidden;cursor:pointer;transition:transform .15s,border-color .15s,box-shadow .15s;border:2px solid transparent}
.card:hover{transform:translateY(-2px);border-color:#3db8a8;box-shadow:0 8px 20px -4px rgba(0,0,0,.5)}
.card.selected{border-color:#3db8a8;box-shadow:0 0 12px rgba(61,184,168,0.35)}
.card-poster{width:100%;aspect-ratio:2/3;background:#1c1e26;display:flex;align-items:center;justify-content:center;color:#6b6b73;font-size:12px;overflow:hidden}
.card-poster img{width:100%;height:100%;object-fit:cover}
.card-info{padding:10px}
.card-info h3{font-size:13px;margin-bottom:4px;line-height:1.3;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;font-weight:500}
.card-info .year{font-size:11px;color:#a1a1aa}
.card-info .rating{font-size:11px;color:#f5a623;margin-top:2px}
.detail-panel{width:380px;flex-shrink:0;background:#15161b;border:1px solid #23252b;border-radius:16px;padding:20px;position:sticky;top:88px;align-self:start;display:none}
.detail-panel.visible{display:block}
.detail-poster{width:100%;aspect-ratio:2/3;background:#1c1e26;border-radius:10px;overflow:hidden;margin-bottom:12px}
.detail-poster img{width:100%;height:100%;object-fit:cover}
.detail-panel h2{font-size:18px;margin-bottom:4px;font-weight:700}
.detail-panel .meta{color:#a1a1aa;font-size:13px;margin-bottom:8px}
.detail-panel .overview{font-size:13px;color:#c7c8cc;line-height:1.5;margin-bottom:16px;max-height:120px;overflow-y:auto}
.detail-panel .rating{color:#f5a623;font-size:14px;margin-bottom:12px}
.request-btn{width:100%;padding:12px;background:#3db8a8;color:#0b0c0f;border:none;border-radius:10px;font-size:15px;font-weight:600;cursor:pointer;transition:background-color .15s}
.request-btn:hover{background:#56d0c0}
.request-btn:disabled{opacity:0.5;cursor:not-allowed}
.request-btn.requested{background:#3ddc84}
.status-msg{margin-top:8px;font-size:13px;color:#a1a1aa;text-align:center}
.history-section{margin-top:24px;padding-top:16px;border-top:1px solid #23252b}
.history-section h3{font-size:14px;color:#a1a1aa;margin-bottom:8px;font-weight:600}
.history-item{display:flex;align-items:center;gap:8px;padding:6px 0;font-size:13px}
.history-item .status{font-size:11px;padding:2px 6px;border-radius:6px;font-weight:500}
.history-item .status.added{background:rgba(61,220,132,.15);color:#3ddc84}
.history-item .status.requested{background:rgba(245,166,35,.15);color:#f5a623}
.history-item .status.searching{background:rgba(245,166,35,.15);color:#f5a623}
.history-item .status.queued{background:rgba(245,166,35,.15);color:#f5a623}
.history-item .status.downloading{background:rgba(61,184,168,.15);color:#56d0c0}
.history-item .status.available{background:rgba(61,220,132,.15);color:#3ddc84}
a{color:#3db8a8}
:focus-visible{outline:2px solid #3db8a8;outline-offset:2px}
@media(max-width:900px){main{flex-direction:column}.detail-panel{width:100%;position:static}}
</style>
</head>
<body>
<header>
<h1>MuxCore <span class="accent">Request</span></h1>
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
document.getElementById('detailPoster').innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#6b6b73">Manual Entry</div>';
document.getElementById('detailRating').textContent = '';
document.getElementById('detailOverview').innerHTML =
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#a1a1aa">TMDB ID</label>' +
'<input id="manualTmdbId" type="number" style="width:100%;padding:8px;border:1px solid #23252b;border-radius:8px;background:#1c1e26;color:#f5f5f7;font-size:14px;margin-top:4px;font:inherit" placeholder="e.g. 218 for The Terminator"></div>' +
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#a1a1aa">Title</label>' +
'<input id="manualTitle" type="text" style="width:100%;padding:8px;border:1px solid #23252b;border-radius:8px;background:#1c1e26;color:#f5f5f7;font-size:14px;margin-top:4px;font:inherit" value="' + esc(query) + '"></div>' +
'<div style="margin-bottom:12px"><label style="font-size:13px;color:#a1a1aa">Year</label>' +
'<input id="manualYear" type="number" style="width:100%;padding:8px;border:1px solid #23252b;border-radius:8px;background:#1c1e26;color:#f5f5f7;font-size:14px;margin-top:4px;font:inherit" placeholder="e.g. 1984"></div>';
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
grid.innerHTML = '<div style="grid-column:1/-1;text-align:center;color:#6b6b73;padding:40px;font-size:14px">No results found. Try a different search.</div>';
return;
}
grid.innerHTML = results.map(r => {
const poster = r.poster ? 'https://image.tmdb.org/t/p/w342' + r.poster : '';
return '<div class="card' + (selectedMovie && selectedMovie.id === r.id ? ' selected' : '') + '" onclick="selectMovie(' + r.id + ')">' +
'<div class="card-poster">' + (poster ? '<img src="' + poster + '" loading="lazy">' : 'No Poster') + '</div>' +
'<div class="card-info"><h3>' + esc(r.title) + '</h3>' +
'<div class="year">' + (r.year || '') + (r.mediaType === 'tv' ? ' • TV' : ' • Movie') + '</div>' +
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
document.getElementById('detailMeta').textContent = (movie.year || '') + ' • ' + (movie.mediaType === 'tv' ? 'TV' : 'Movie') + ' • TMDB: ' + movie.id;
document.getElementById('requestBtn').textContent = movie.mediaType === 'tv' ? 'Request Series' : 'Request Movie';
document.getElementById('detailRating').textContent = '★ ' + (movie.voteAvg ? movie.voteAvg.toFixed(1) : 'N/A');
document.getElementById('detailOverview').textContent = movie.overview || 'No overview available.';
const posterEl = document.getElementById('detailPoster');
if (movie.poster) { posterEl.innerHTML = '<img src="https://image.tmdb.org/t/p/w342' + movie.poster + '">'; }
else { posterEl.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#6b6b73">No Poster</div>'; }
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
body: JSON.stringify({tmdbId: selectedMovie.id, title: selectedMovie.title, year: selectedMovie.year, overview: selectedMovie.overview, poster: selectedMovie.poster, mediaType: selectedMovie.mediaType || 'movie'})
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
		_, _ = fmt.Sscanf(date[:4], "%d", &y)
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
	defer func() { _ = conn.Close() }()
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
	tenantID := m.resolveTenant(ctx, nil)
	if existing := m.findExisting("movie", req.GetTmdbId(), req.GetTitle(), req.GetYear(), tenantID); existing != nil {
		slog.Info("movie already requested", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId(), "request_id", existing.ID, "tenant", tenantID)
		return &requestmedia.RequestMovieResponse{
			RequestId: existing.ID, MovieId: existing.ItemID, Status: existing.Status,
		}, nil
	}
	requestID := fmt.Sprintf("req_mv_%d", time.Now().UnixNano())
	requestedBy := strings.TrimSpace(req.GetRequestedBy())
	posterPath := requestPosterFromContext(ctx)

	if m.needsApproval(requestedBy, rolesFromIsAdmin(req.GetIsAdmin())) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "movie", Title: req.GetTitle(),
			Year: req.GetYear(), TMDBID: req.GetTmdbId(), Overview: req.GetOverview(),
			Poster: posterPath, Status: "pending",
			RequestedBy: requestedBy, TenantID: tenantID,
		})
		slog.Info("movie request pending approval", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId(), "by", requestedBy, "tenant", tenantID)
		return &requestmedia.RequestMovieResponse{RequestId: requestID, Status: "pending"}, nil
	}

	movieID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "movie", TMDBID: req.GetTmdbId(),
		Title: req.GetTitle(), Year: req.GetYear(), Overview: req.GetOverview(),
		Genres: req.GetGenres(), Poster: posterPath, RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestMovieResponse{
		RequestId: requestID, MovieId: movieID, Status: status,
	}, nil
}

func (m *Module) RequestTV(ctx context.Context, req *requestmedia.RequestTVRequest) (*requestmedia.RequestTVResponse, error) {
	tenantID := m.resolveTenant(ctx, nil)
	if existing := m.findExisting("tv", req.GetTmdbId(), req.GetTitle(), req.GetYear(), tenantID); existing != nil {
		slog.Info("tv already requested", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId(), "request_id", existing.ID, "tenant", tenantID)
		return &requestmedia.RequestTVResponse{
			RequestId: existing.ID, SeriesId: existing.ItemID, Status: existing.Status,
		}, nil
	}
	requestID := fmt.Sprintf("req_tv_%d", time.Now().UnixNano())
	requestedBy := strings.TrimSpace(req.GetRequestedBy())
	posterPath := requestPosterFromContext(ctx)

	if m.needsApproval(requestedBy, rolesFromIsAdmin(req.GetIsAdmin())) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "tv", Title: req.GetTitle(),
			Year: req.GetYear(), TMDBID: req.GetTmdbId(), Overview: req.GetOverview(),
			Poster: posterPath, Status: "pending",
			RequestedBy: requestedBy, TenantID: tenantID,
		})
		slog.Info("tv request pending approval", "title", req.GetTitle(), "tmdb_id", req.GetTmdbId(), "by", requestedBy, "tenant", tenantID)
		return &requestmedia.RequestTVResponse{RequestId: requestID, Status: "pending"}, nil
	}

	seriesID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "tv", TMDBID: req.GetTmdbId(),
		Title: req.GetTitle(), Year: req.GetYear(), Overview: req.GetOverview(),
		Poster: posterPath, SeasonNumber: req.GetSeasonNumber(), EpisodeNumber: req.GetEpisodeNumber(),
		RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestTVResponse{
		RequestId: requestID, SeriesId: seriesID, Status: status,
	}, nil
}

func (m *Module) RequestMusic(ctx context.Context, req *requestmedia.RequestMusicRequest) (*requestmedia.RequestMusicResponse, error) {
	tenantID := m.resolveTenant(ctx, nil)
	if existing := m.findExistingMusic(req.GetMusicbrainzId(), req.GetTitle(), tenantID); existing != nil {
		slog.Info("music already requested", "title", req.GetTitle(), "mbid", req.GetMusicbrainzId(), "request_id", existing.ID)
		return &requestmedia.RequestMusicResponse{
			RequestId: existing.ID, ArtistId: existing.ItemID, Status: existing.Status,
		}, nil
	}
	requestID := fmt.Sprintf("req_mu_%d", time.Now().UnixNano())
	requestedBy := strings.TrimSpace(req.GetRequestedBy())
	posterPath := requestPosterFromContext(ctx)
	if posterPath == "" {
		posterPath = strings.TrimSpace(req.GetPoster())
	}

	if m.needsApproval(requestedBy, rolesFromIsAdmin(req.GetIsAdmin())) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "music", Title: req.GetTitle(),
			MusicBrainzID: req.GetMusicbrainzId(), Overview: req.GetOverview(),
			Poster: posterPath, Status: "pending",
			RequestedBy: requestedBy, TenantID: tenantID,
		})
		slog.Info("music request pending approval", "title", req.GetTitle(), "mbid", req.GetMusicbrainzId(), "by", requestedBy)
		return &requestmedia.RequestMusicResponse{RequestId: requestID, Status: "pending"}, nil
	}

	artistID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "music", MusicBrainzID: req.GetMusicbrainzId(),
		Title: req.GetTitle(), Overview: req.GetOverview(), Poster: posterPath,
		RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestMusicResponse{
		RequestId: requestID, ArtistId: artistID, Status: status,
	}, nil
}

func (m *Module) findExistingMusic(mbid, title, tenantID string) *requestRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rec := range m.requests {
		if rec.ItemType != "music" {
			continue
		}
		if tenantID != "" && rec.TenantID != "" && rec.TenantID != tenantID {
			continue
		}
		if mbid != "" && rec.MusicBrainzID == mbid {
			return rec
		}
		if mbid == "" && title != "" && strings.EqualFold(rec.Title, title) {
			return rec
		}
	}
	return nil
}

func (m *Module) GetStatus(ctx context.Context, req *requestmedia.GetStatusRequest) (*requestmedia.GetStatusResponse, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.refreshAcquisitionStatus(refreshCtx)
	m.mu.RLock()
	rec, ok := m.requests[req.GetRequestId()]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("request not found: %s", req.GetRequestId())
	}
	if tenant.Enabled() {
		want := m.resolveTenant(ctx, nil)
		if rec.TenantID != "" && rec.TenantID != want {
			return nil, fmt.Errorf("request not found: %s", req.GetRequestId())
		}
	}
	resp := &requestmedia.GetStatusResponse{
		RequestId: rec.ID, ItemType: rec.ItemType, ItemId: rec.ItemID,
		Title: rec.Title, Year: rec.Year, Status: rec.Status,
		CreatedAt: rec.CreatedAt.Format(time.RFC3339), UpdatedAt: rec.UpdatedAt.Format(time.RFC3339),
		RequestedBy: rec.RequestedBy, ApprovedBy: rec.ApprovedBy,
	}
	if !rec.ApprovedAt.IsZero() {
		resp.ApprovedAt = rec.ApprovedAt.Format(time.RFC3339)
	}
	if isInProgressRequestStatus(rec.Status) {
		if snap, err := m.fetchAutomationSnapshot(refreshCtx); err == nil {
			if detail, label := applyHistoryStatusFields(rec, snap); detail != "" || label != "" {
				resp.StatusDetail = detail
				resp.StatusLabel = label
			}
		}
	}
	return resp, nil
}

var _ contracts.Module = (*Module)(nil)
